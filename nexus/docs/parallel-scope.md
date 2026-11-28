# Parallel execution: one concurrent scope per body

[中文](parallel-scope.zh.md) | English

**Status: shipped.** This document replaces the `fork` / `wait` pair. Shipped:
`data.concurrent`, `config.parallel` (`max_concurrency`, `timeout`, `join` = collect / drain / abort), the body scope
(dispatch / join / snapshot) on `process`, the six validation codes (four errors, two warnings), `concurrent` on `create_node` /
`insert_node` / `update_node`, and `blocks`
in `read_flow`, `loop` as a host (a fresh scope per iteration, one snapshot per round), and the removal of
`fork` / `wait` / mono.
There is deliberately no migration path and no compatibility layer: the two node types are removed outright (§9), and diagrams that still carry them are not a
constraint on this design. The decision record and the alternatives that were rejected are in §11 and §12 — read them before
reopening the design.

## 1. Why the current pair is being replaced

`fork` is a marker that owns nothing; `wait` is an optional node that carries every guarantee the model needs. That inversion
is the source of all five defects below.

| # | Defect | Evidence |
|---|---|---|
| 1 | Run status comes only from the main chain's error, so a member's failure is invisible unless a `wait` exists | `runner.go` — `persistRun` uses `runErr`; the dispatched goroutine discards the error (`_ = executeNode`) |
| 2 | A `fork` group is not joined at the end of the run (only mono groups are), so unjoined members are cancelled after the run has already been recorded as successful | `runner.go` — `execCtx.MonoWGWait()`; `defer execCtx.Cancel()` |
| 3 | Mono concurrent nodes have the same hole and no observation path at all | same two places |
| 4 | A group's results have no channel: `wait` declares no outputs, so members can only write shared flow variables (unsynchronised, last write wins) | `wait.go`; `wait.md` "No output variables" |
| 5 | The join collapses every member failure into `other` and only supports `skip`, so per-code handling at the join is impossible — and `skip` is the one action that lets the main chain run beside still-running members | `wait.go` — `failf(nr, ErrCodeOther, …)`, `Capabilities{ Actions: []ErrorAction{ErrorActionSkip} }` |

Structure follows: because a group is a one-shot barrier attached to a node result (`ForkState` active → waiting → done), a
second join is a `config` error (`nodeResult.go`), and membership is discovered by "whoever calls `Add`", so a member that is
never dispatched is simply absent from the count. `flowcheck` never asks whether a group has a join at all.

## 2. The model

> **A concurrent scope is one execution of a body. A member is a node that leaves that body's chain and is collected when the
> body ends.**

Vocabulary, frozen here (UI, code and database use these words):

| Term | Meaning |
|---|---|
| **host** | A container whose body is a scope: `process` or `loop`. Only these two. |
| **body** | The chain a host runs, starting at its `entry` child |
| **scope** | One execution of that body (a `BodyScope` value, created per execution — a `loop` creates one per iteration) |
| **member** | An **executable** direct child of a host that carries `data.concurrent: true` and lies on the body's entry chain. The `entry`, the control signals (`break` / `continue` / `return` / `exit`), the annotation nodes, the output nodes (`result` / `progress`) and the instant nodes (`assign` / `memory`) cannot be members — control flow stays on the chain, result / progress append to the run in call order, and assign / memory never wait, so a slot buys nothing |
| **join** | The implicit collection performed when the body ends; there is no `wait` node |

Two words carry the model into identifiers, and they are **not** interchangeable: `concurrent` is the member flag on the node
itself (`data.concurrent`, the `concurrent` tool argument, the canvas tick), while `parallel` is the host's block and record
(`config.parallel`, `ParallelConfig`, `ParallelResult`, `ProcessResult.Parallel`). Validation codes follow the same split:
`concurrent_*` validates the flag, `parallel_*` validates the scope's runtime shape. Prose — UI sections, help topics, this
document — says "concurrency", which is the concept and never a field name.

`process` and `loop` are hosts **and** can be members. Hosts may be placed anywhere a node can be placed — inside a
`branch` sub-chain, at the top level, inside another host — so concurrency can appear anywhere, but always through a host.

## 3. Data model

The member flag is a **node structure attribute**, not a `config` key. `update_node` replaces `config` wholesale; a flag
living there can be lost, which is exactly why `keepConcurrency` exists today.

```go
// flow.NodeData
Concurrent bool `json:"concurrent,omitempty"` // leave the enclosing body's chain; valid only for process/loop children
```

Why `data` and not a sibling of `data`: `position`, `parent_node` and `extent` sit at the top level because the canvas library
models them; `concurrent` is our own concept, and `data` is the field the canvas round-trips untouched. It stays out of
`config` for the reason above — `config` is behaviour, and behaviour is replaced wholesale by `update_node`.

**`data` must be read-modify-write everywhere.** `buildNodeForTool` builds a fresh `map[string]any{"label": …, "config": …}`;
that path has to carry an existing flag over, or the same class of silent loss simply moves up one level. One unit test
covers it.

Host settings (optional block on `process` / `loop`):

```go
type ParallelConfig struct {
    MaxConcurrency int         `json:"max_concurrency"` // <= 0 means no author limit; the tenant ceiling applies
    Timeout        TimeoutConf `json:"timeout"`         // the existing {enabled, seconds} shape
    Join           string      `json:"join"`            // "collect" (default) | "drain" | "abort"
    Ceiling        int         `json:"system_ceiling,omitempty"` // filled in by the executor, always positive
}
```

`max_concurrency` is a **request**; the tenant ceiling (`flow_configs.max_concurrency`, code fallback 16, seeded system row
32) is the answer. The executor lowers the value before the scope is built — `0` resolves to the ceiling rather than to
"unbounded" — logs one line when it lowers an explicit number, and keeps both numbers in the snapshot. Unbounded fan-out is
what turns one badly shaped diagram into sandbox `busy` failures and provider 429s instead of a queue, and a ceiling is a
queue: members beyond it wait for a slot. That is also why a new `process` node is created with `max_concurrency: 8` — the
number is visible and editable, where "no limit" was invisible and easy to leave alone.

Snapshot, written into the host's `NodeResult` on every exit path:

```go
type ParallelResult struct {
    MaxConcurrency int         `json:"max_concurrency"`              // what actually ran
    SystemCeiling  int         `json:"system_ceiling,omitempty"`     // the ceiling that bounded it
    Join           string      `json:"join"`
    Members        []MemberRun `json:"members"`
    Failed         []string    `json:"failed"`
    Abandoned      bool        `json:"abandoned"`
    ExitedBy       string      `json:"exited_by,omitempty"` // the exit node that ended the run, "" otherwise
}

type MemberRun struct {
    NodeID     string     `json:"node_id"`
    ProcessID  string     `json:"process_id"`
    Status     NodeStatus `json:"status"`
    ErrCode    string     `json:"err_code,omitempty"`
    ErrMsg     string     `json:"err_msg,omitempty"`
    StartAt    time.Time  `json:"start_at"`
    EndAt      time.Time  `json:"end_at"`
    DurationMs int64      `json:"duration_ms"`
}
```

This snapshot is a hard requirement, not a nicety: without it "did it really run concurrently" can only be answered by an
author-written timing harness, which is how the measuring bugs this design replaces came about.

## 4. Execution semantics

| # | Rule |
|---|---|
| S1 | The host creates a body process and walks the chain from `entry` |
| S2 | An ordinary node executes inline, then the walk follows its outgoing edge |
| S3 | A member is dispatched (own child process + goroutine, registered in the scope) and the walk **continues along its outgoing edge without waiting**. The member runs only itself and never follows its own edge |
| S4 | When the chain ends, the body **joins** every member of this scope |
| S5 | `max_concurrency` limits members only; the semaphore is taken inside the member's goroutine, so a limit never blocks the walk |
| S6 | The host's own execution ends when the chain has ended **and** every member has reached a terminal state |
| S7 | Every scope is a fresh instance: a `loop` body run three times creates three scopes that never share members |

Members need not be contiguous: `A(concurrent) → C(ordinary) → D(concurrent)` is valid and `C` runs while `A` is still
running.

`branch` / `intent` may be a member. Because they terminate the chain, a branch member is necessarily the last node the walk
dispatches, and its sub-chain runs inside that lane — the join waits for the whole sub-chain, not just the condition. When a
lane should be a conditional sub-flow, or when a host needs two conditional lanes, wrap each in a `process`.

Failure aggregation at the join:

| Situation | Block error |
|---|---|
| every failed member reports the same code `X` | `X`, propagated as is |
| failed members report different codes | `other`, with one line per member in the message |
| a member never finished before the scope timeout | `timeout`, plus each member's state at that moment |
| the body's own chain failed | that node's code (the chain wins over members) |

## 5. Cancellation and leaving a body early

| Situation | Behaviour |
|---|---|
| A member fails, `join: "collect"` (default) | Every member finishes; the join decides (§4) |
| A member fails, `join: "drain"` | Stop starting new work: queued members and later chain nodes never start (recorded `skipped`, reason `scope draining`); members already running finish, so their outcomes stay real |
| A member fails, `join: "abort"` | Same as `drain`, plus cancel the members still running (`cancelled`, reason `aborted by scope`). A chain node already executing is **not** interrupted — the walk stops at the next node boundary, so "unknown outcome" stays confined to the member side |
| An ordinary node fails | Cancel the scope, return the error |
| `break` / `continue` / `return` | **Join the scope first**, then propagate the signal — leaving a body means its concurrency has been collected |
| `exit`, on the body's chain or inside a member's own body | Stop what the scope started instead of waiting for it: queued members never start (`skipped`, reason `scope terminated by exit at node X`), members in flight are cancelled (`cancelled`, reason `stopped by exit at node X`), then the join lifts the signal and the host records `success`. `exit` is global and immediate — a run that has ended can never read those results, so making it wait would only delay the end. The `exit` node itself is never a member (`concurrent_member_type`): control flow stays on a chain, and a nested host is what carries the signal out of a member |
| `branch` / `intent` terminating the body | The sub-chain finishes inside the branch lane, then the body's join runs |
| Scope timeout | Cancel members, interrupt the walk, `timeout` with per-member states |
| Run cancelled | Propagates down through `StdCtx` |

`abort` rests on `ExecContext.WithContext(ctx)`: a member runs on a shallow copy of the run context that shares the variable
store and its lock by pointer but carries its own cancellation tree, so cancelling one member cannot cancel its siblings or
the body's own chain. The scope derives that context once per body (`BindMembers`) and cancels it on the first member
failure; the scope `timeout` uses the same primitive with a deadline instead of a failure as the trigger, reports
`timeout` rather than the failures it cancelled, and marks the snapshot `abandoned`. A deadline that fires after every
member finished is ignored - a completed body has not timed out.

Three details that must be pinned down, because each is easy to get subtly wrong:

- **Which member is "the first".** The failure that triggers the policy is chosen under the scope lock: deterministic within
  one run, not reproducible across runs (two members failing in the same instant can swap places). A different "primary"
  member on the next run is not a bug.
- **Members stopped by the policy are not failures.** A member cancelled or never started is recorded `cancelled` / `skipped`
  with the reason and excluded from the failure list. Reporting "6 of 6 members failed" when one failed and five were stopped
  turns one fault into six.
- **A skipped host publishes partial output.** With `on_error: {skip}` on the host, values already written into its `outputs`
  remain readable downstream, so downstream sees gaps. That follows from variables being written directly rather than
  collected at the end; skip only where downstream tolerates missing values.
- **The host's `on_error` is the only scope-level handler.** `process` / `loop` declare `skip` and `handler` and nothing else:
  `skip` continues the main chain past an optional section, `handler` writes the fallback outputs the chain reads. `retry` is
  absent on purpose — re-running a body repeats every member's side effects, so "retry the failed member" has no meaning at the
  host. A `config` / `internal` code still bypasses `on_error`, so the mask cannot hide a platform failure.

## 6. Visibility

A node may read a variable only if its producer is already finished. The readable set (`done(R)`) is:

```
done(R) =
    { n ∈ chain(body(R)) | index(n) < index(R) ∧ n ∉ members(body(R)) }   // earlier chain nodes, not members
  ∪ { n ∈ chain(ancestorBody) | n precedes the node that contains R }      // ancestors already executed
  ∪ { every member of an inner host that has already joined }              // an inner scope that is not a member
```

Everything else is `parallel_visibility` (a static error): a member never reads a sibling member or a later chain node, and a
chain node never reads a member. Results cross the body boundary only through the host's `outputs`, which the members write
through `targets` and which are readable after the join. **No mapping syntax is introduced**: `targets` already reaches an
ancestor container's outputs (`setVariableNoLock` → `SetLocalVar`, falling back to `SetOutputVar`, resolved through
`FindNodeResultByProcessChain`), so the existing mechanism is the whole channel.

Consequences, stated so they are not discovered later as bugs:

- two members cannot read each other;
- a chain node after a member cannot read that member;
- nodes after the host can read everything the host produced.

What to do instead, so the rule is not just a wall:

| You want | Write |
|---|---|
| member B consumes member A's result | two hosts in series: `process { A } → process { B }` — parallel inside each stage, explicit between them |
| one summary over every member | collect into the host's `outputs`, summarise **after** the host |
| a serial step that runs beside the members | leave it on the chain (it may run alongside them), but it must not read them |

The rule is deliberately not configurable. A knob here restores "it looks fine, it passed once, then the timing changed" —
precisely the failure mode this document exists to remove.

## 7. Validation

Codes are split by what they validate: `concurrent_*` is about the member flag itself (where it may sit, what the node it marks may reach), `parallel_*` is about the scope's runtime shape.

| Code | Level | Condition |
|---|---|---|
| `concurrent_member_parent` | error | `concurrent: true` on a node whose parent is not `process` / `loop` (top level, inside `function`, inside a `branch` sub-chain) |
| `concurrent_member_not_on_chain` | error | Parent is a host, but the node is not on the body's entry chain (branch sub-chain, dangling child) |
| `parallel_visibility` | error | Violates §6 |
| `concurrent_member_write` | error | A member's `targets` names a flow variable or another node's variable; a member writes only its own outputs / locals and the host's outputs. Without this rule the channel is not unique: `done(R)` resolves `source_type: node` only, so a chain node reading a flow variable a member wrote would slip through |
| `parallel_output_conflict` | error | Two nodes write the same host output key and at least one of them is a member (two ordinary chain nodes keep today's deterministic last-write-wins) |
| `parallel_degenerate` | warning | A scope with one member, or `max_concurrency: 1` |
| `parallel_fanout` | warning | Nesting depth ≥ 3, or same-level concurrency product > 8 |
| `container_entry` | error | Existing rule, reused |

Removed: `wait_fork`, `concurrency_shape`, `concurrency_fork`, `concurrency_group`.

`parallel_fanout` counts a host itself as depth 1 and multiplies `max_concurrency` up the enclosing chain. The product only
applies when every level sets a positive limit: an unlimited level has no ceiling to multiply, so warning there would fire on
the default of every nested host. Both halves are warnings rather than errors — a deep diagram can be exactly what the author
wants, it just should not be discovered from a bill after the run.

## 8. Tooling, canvas, documents

- `create_flow` / `create_node` / `insert_node`: accept `concurrent` on a step; accept `config.parallel` on a host. The
  `concurrency=mono|group` argument, `applyConcurrencyPolicy`, `parseConcurrencyPolicy` and `keepConcurrency` are removed —
  there is no longer a per-node concurrency block to strip or preserve.
- Moving a node across a container boundary must clear `concurrent` and say so in `warnings` (a move must never change
  concurrency silently).
- `read_flow` returns `blocks: [{host, members, max_concurrency, join}]` so that "which scope am I in" is answerable from the
  tool output rather than inferred.
- `read_flow_run` shows the host's `ParallelResult` (member states, codes, durations).
- Canvas: a toggle for members, a settings panel on the host, a visual region for the scope, and a badge on a member's
  header (hover shows the tick's own label). The `?` help topic for `process` / `loop` gains the host section.
- Node reference: `process.md` and `loop.md` gain a concurrency section; `fork.md` and `wait.md` are deleted;
  `nodes/README.md` index updated; `terminology.md` gains the host / body / scope / member row.
- Tool hints and the help topic state that `branch` / `intent` terminate the chain: as a member they can only be the last node
  dispatched, so a lane that should be a conditional sub-flow (or a host needing two of them) wraps each in a `process`.

## 9. Removed code

`fork.go`, `wait.go` (executors and node types), `ForkResult` / `WaitResult` / `ForkState` / `ForkMember`,
`dispatchConcurrent` / `getConcurrency` / `concurrencyConfig`, the mono dispatch branch and `MonoWGWait` /
`ExecContext.MonoWGAdd|Done|Wait`, `config.concurrency` (`mode`, `fork_node_id`, `max_concurrency`), the
"concurrency is off by default" tooling policy, and the four `concurrency_*` / `wait_*` validation codes.

`mono` is **removed without a replacement**. As implemented it only dispatches one node to a background goroutine and joins
it when the run ends (`MonoWGAdd` / `MonoWGWait`), discarding its error; there is no batching mechanism at all, so the node
reference's "one node processes several records concurrently" is not what the code does. Dynamic batch parallelism (N loop
iterations in flight) is a separate design: the loop's loop variables, outputs, iteration counter and `break` / `continue`
semantics are all single-slot today, so it cannot be added as a config field.

Removal is unconditional, and the `knowledge` precedent — drop the palette entry, keep the renderer for old diagrams — is
deliberately **not** followed. Nothing is kept for a diagram that still carries them: no deprecated alias, no renderer, no
migration script, and no dedicated diagnostic — such a diagram lands on the existing "no executor registered" config error,
like any other unknown type.

It lands in two steps, and the order is part of the design:

1. **Creation path (done).** `creatableNodeTypes`, `flowStepTypes`, the `concurrency` argument of the node, edge and flow
   tools, and the node reference stopped offering `fork` / `wait`. The stripping of `config.concurrency` on a written node went
   with them: no diagram carries the block any more, so an ordinary write is no longer rewritten.
2. **Runtime (done).** `fork.go` / `wait.go`, the executor registry, `knownTypes`, the palette entries, `ForkNode` /
   `ForkNodeProps` / `WaitNode` / `WaitNodeProps` / `concurrencyConfig.js`, `ForkResult` / `WaitResult` / `ForkState` /
   `ForkMember`, `dispatchConcurrent` / `getConcurrency`, the mono branch, the node-level concurrency UI on the canvas nodes and
   the four validation codes went together, with the node reference pages (`fork.md` / `wait.md`) and `terminology.md`.

Removing the runtime first would leave the palette offering nodes that fail with "no executor registered" — the same
half-kept state this section is cleaning up.

## 10. Test matrix

**Execution**

1. Two members succeed → host `success`; `ParallelResult.Members` has both with timestamps.
2. One member times out → host `failed` with `err_code: timeout` (propagated, not `other`).
3. Two members fail with different codes → `other` with one line per member.
4. `abort`: the first failure cancels the running members, the queued ones are `skipped`, and the walk stops at the next node boundary; the running chain node itself finishes.
5. `collect`: the first failure lets the rest finish.
6. `drain`: the first failure leaves running members to finish, but queued members and later chain nodes never start (recorded `skipped`, reason `scope draining`).
7. `max_concurrency: 1`, three members → a node after the host runs before the first member finishes (the walk is not blocked).
8. A `loop` with two members, three iterations → three independent joins; snapshots never mix (re-entry).
9. Non-contiguous members `A → C → D` → `C` runs while `A` is running; the join waits for `A` and `D`.
10. `branch` terminating the body → every member, including the branch lane, is joined before the host returns.
11. `exit` inside a body, on the chain or inside a member's body → in-flight members are cancelled and queued ones never start; the run stops without waiting, and the host records `success`. The flag on an `exit` node itself is refused (`concurrent_member_type`).
12. Host inside host → inner joins first.
13. A host at the top level → works (a `process` wrapping the whole flow).
14. Member failure plus a chain failure → the chain's code wins.

**Validation**

15. `concurrent` at the top level → `concurrent_member_parent`.
16. `concurrent` on a node in a `branch` sub-chain inside a process → `concurrent_member_not_on_chain`.
17. A chain node reading a member's output, and one member reading another → `parallel_visibility`.
18. Two members writing one host output → `parallel_output_conflict`.
19. One member / `max_concurrency: 1` → warning.
20. A member's `targets` names a flow variable → `concurrent_member_write`.

**Regression**: a failed member never leaves a half-written host output.

## 11. Phases

**Phase 1 — usable and stops the false green.** Struct attribute and `config.parallel`; `BodyScope` (dispatch / join /
snapshot); `runBody`; the `execute` signature; `process` and `loop` as hosts (the loop gets one scope per iteration, with a
snapshot per round); the validation codes `concurrent_member_parent`, `concurrent_member_not_on_chain`, `parallel_visibility`,
`concurrent_member_write`, `parallel_output_conflict` and the `parallel_degenerate` warning; tests 1, 2, 5, 6, 7, 8, 9, 13, 15,
17, 18, 19. Acceptance: a failed member fails the run without any
`wait`; the host record carries member timings; a limit of 1 never prevents the walk from reaching the join.

**Phase 2 — shipped.** Scope cancellation, `abort` (test 4) and the scope `timeout` (test 2) landed on
`ExecContext.WithContext`: the variable store and its lock are shared by pointer, so a member gets its own cancellation tree
without a copy of the run, and the deadline is one timer per body. `fork` / `wait` / mono are deleted, all seven validation
codes ship (`parallel_fanout` included). The palette entry was withdrawn again once the naming settled: `process` is called
**Flow segment** in the UI, and its concurrency settings live on the node itself — a second palette entry for the same type
read as a second node type, which it never was (§13).

## 12. Decisions taken, and the alternatives that were rejected

Taken:

- `join` defaults to `collect`. `abort` is opt-in: cancelling members mid-flight leaves half-finished side effects, so it must
  be a deliberate choice, not the default.
- `max_concurrency` has no author default, but it is never unbounded: the tenant ceiling (`flow_configs.max_concurrency`,
  fallback 16) is applied at run time and `parallel_fanout` still warns about nesting; a new `process` node is created with 8
  so the number is visible. The `-1` / absent spelling means "no author limit", not "no limit".
- `mono` is removed with no replacement; batch parallelism stays out of scope (it needs per-iteration results and a parallel
  `break` / `continue` definition first).
- No compatibility layer for the removed pair: no deprecated alias, no renderer kept for old diagrams, no migration script. A
  type kept alive only for rendering also keeps the fork/join model in the code base and in the docs, which is the thing being
  replaced.
- Hosts are `process` and `loop` only, which also means **the top level is not a host**: a flow that is parallel from end to
  end wraps its body in a `process`. A scope needs an owner to carry `max_concurrency` / `timeout` / `join`, and "global
  concurrency" is exactly what used to go green because nobody owned it.
- `branch` / `intent` may be a member. They are legal children on the entry chain and forbidding them would be an arbitrary
  restriction; because they terminate the chain they can only be the last node dispatched, so a conditional lane wraps itself
  in a `process`.
- Results leave a host through its existing `outputs` written by `targets`; no `outputs: {from: …}` mapping syntax.
- The member flag is `data.concurrent` — neither a `config` key (replaced wholesale) nor a sibling of `data` (the canvas only
  promises to round-trip `data`).
- Visibility is a hard rule with no escape hatch. The alternatives are chaining hosts or moving the consumer after the host.

Rejected, with the reason that killed each:

- **Keep `fork` + `wait`, enforce the pair.** Fixes the missing join but leaves membership as a per-node declaration, leaves
  the group's results without a channel, and leaves a node type whose only job is a boolean.
- **Derive the group from the chain interval `[fork, wait)`.** Correct as a static model, but the edges inside the interval
  become layout-only, which has to be explained to every reader forever, and a member outside the interval is still
  expressible.
- **Any node may carry `concurrent` as long as its parent is a `parallel` block.** Rejected after review: it makes "I want
  two nodes of a loop body to run concurrently" cost a structural edit (create a container, move the nodes), and adds a node
  type with no meaning beyond a scope.
- **Any container may carry `concurrent`.** Same reasoning, one step further: the body already has a beginning and an end, so
  the scope needs no owner other than the body itself.

## 13. Open questions and out-of-scope boundaries

- Batch parallelism (N loop iterations in flight) is out of scope. If it is wanted later it needs its own design: a result
  channel that files each iteration's output in order, and a definition of `break` / `continue` while other iterations are
  still running.
- Should a host record also persist the join verdict when the scope was abandoned (timeout), or is `Abandoned` plus the member
  states enough? Currently the latter.
- How should `process` be named in the UI now that it is also the concurrency host? Settled: **Flow segment** (Chinese: 流程段,
  paired with the "concurrency scope" settings block), and the palette carries exactly one entry for it. The earlier "insert
  concurrent section" preset was removed — two entries that create one node type, one of them silently pre-filling `parallel`,
  read as two types and hid where the settings actually live.
