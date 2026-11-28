# `on_error`: retry / skip / fallback script by error code

`config.on_error` has four optional blocks, and **matching is by error code** (`codes` is mandatory; without it nothing is enabled):

```json
{
  "on_error": {
    "retry":   {"codes": ["timeout", "connection"], "patterns": [], "max": 2, "delay_ms": 1000},
    "skip":    {"codes": ["not_found"], "patterns": []},
    "handler": {"codes": ["other"], "patterns": ["%rate limit%"], "lang": "lua",
                "script": "return 'skip'", "inputs": {}},
    "post":    {"set_outputs": {"text": "temporarily unavailable"},
                "handler": {"lang": "lua", "script": "return { text = 'fallback' }", "inputs": {}}}
  }
}
```

## Matching order

`retry` → `skip` → `handler` → if none matches, the node fails.

## retry

| Key | Required | Default | Meaning |
|---|---|---|---|
| `codes` | ✅ | — | list of error codes; **an empty array means no retry**. Available codes below |
| `patterns` | — | — | only effective when `codes` contains `other`: match the message, `%x%` contains, `x%` prefix, `%x` suffix, no `%` also means contains |
| `max` | — | `0` | number of retries (excluding the first attempt); `<=0` means no retry |
| `delay_ms` | — | `0` | **fixed** interval in milliseconds, no backoff |

A retry re-runs the whole node, side effects included: the request is sent again, the script writes again. Only retry what is
idempotent, or gate the side effect on `retry_count == 0`.

## skip

| Key | Meaning |
|---|---|
| `codes` / `patterns` | on a match the node is recorded as `skipped` (continues as success), the error code stays inspectable; downstream receives **empty outputs** |

Use it for "even if the lookup fails, keep going"; add `post.set_outputs` when a default value is needed. The key has to be
declared in the node's `outputs` first — `set_outputs` cannot create a variable.

**What downstream sees after a skip** — a skip is invisible at run time, so it is a promise that nobody depends on the result:

- every declared output sits at its zero value (`""` / `0` / `false` / empty array) unless the node wrote something before
  it failed. So `status_code == 200` on a skipped `http` step is just false: downstream reads it as "the call failed", not
  as "the call never happened";
- a skipped **host** (`process` / `loop`) publishes whatever its members wrote before the failure, and with `join: "abort"`
  those can be half-written - a reader gets a partial value that looks complete;
- nothing carries the status into the data channel: there is no builtin meaning "the producer was skipped", and the run
  reports success either way. The status lives only in the record (canvas, run panel, `read_flow_run`).

When downstream really does need to tell, leave a value that says so - a business key, not a framework one:

```json
"on_error": {
  "skip": {"codes": ["not_found"]},
  "post": {"set_outputs": {"ok": false, "text": ""}}
}
```

`ok` is then an ordinary output: readable anywhere, testable with `branch`, visible in the canvas. `validate_flow` reports
`skipped_output_read` (a warning) for the case this protects against: a skip without `post.set_outputs` / `post.handler` /
`on_error.handler`, plus a downstream node reading that node's outputs.

## handler

| Key | Meaning |
|---|---|
| `codes` / `patterns` | the script runs only on a match |
| `lang` | default `lua`; **`template` is not supported** |
| `script` | inputs: `error_code` / `error_message` / `error_node_id` / `error_node_type` / `retry_count` + `inputs` |
| `inputs` | extra variable declarations |

`error_node_id` / `error_node_type` describe **the node whose `on_error` fired**, not the cause: on a `process` / `loop`
that is the container itself. Which child failed is in `error_message`:

- a concurrent member failure reads `member <node id>: <message>`;
- a failure on the body's own chain reads `node <node id>: <message>`, and a `loop` puts `iteration <n> failed:` in front;
- several members failing with different codes make the container's code `other` and the message a list, one line per
  member (`node <id> (process <pid>): <code> <message>`).

If a decision depends on *which* child failed, put the `on_error` on that child instead of on the container: its
`error_node_id` is then the node itself, the code is its own, and the container never sees the failure at all.

The script returns:

- the string `"skip"` (legacy form) or `{action: "skip"}` → treated as skipped;
- a map carrying `error_code` / `error_message` → the code and text are **rewritten** and the node still fails;
- anything else → the node still fails.

**The return form is per language**, and the `"return 'skip'"` above is lua / js only — in python the same handler is
`__return__ = "skip"` (a `return` statement is a `SyntaxError`). See [`script-languages.md`](script-languages.md).

A handler **cannot write outputs**: its return value only rewrites the error, so a fallback value belongs in `post`.
It also runs **after** `skip` — a code listed in both blocks is claimed by `skip` first, so that handler never runs for it
(`validate_flow` reports `on_error_shadowed`).

If the handler script itself fails, the node still fails with the **original** `error_code` / `error_message`, and `post`
still runs. The reason is recorded as `handler_error` on the node record (and in its logs) instead of being appended to the
message, so the code `on_error` matches on stays the one the root cause produced.

## post (fallback, only effective on certain nodes)

| Key | Meaning |
|---|---|
| `set_outputs` | static outputs, written straight into the node's output variables |
| `handler` | fallback script `{lang, script, inputs}`; the returned map overwrites the output variables — `return { text = 'fallback' }` in lua / js, `__return__ = {"text": "fallback"}` in python |

- runs on **every failure path inside `on_error`**: `skip` matched, `handler` returned skip, `handler` failed, nothing matched.
  A successful node never runs it — post is cleanup, not a hook;
- **`post.set_outputs` keys must be declared in the node's `outputs`**: an undeclared key cannot be written — the runner
  records `failed to write post output ...` in `post_error` (and the logs) instead of creating the variable, and the value
  downstream reads stays the type default. A post can fail twice over (an unwritten key and a failing script); `post_error`
  keeps both reasons;
- **only `http` / `llm` / `code` / `mcp` / `rpc` / `call` / `process` / `loop` support post**; on other nodes it is ignored even if written (the
  executor declares `HasPost=false` and the runner blanks `post`);
- the front-end `on_error` panel also offers it only for those node types.

## Three hard rules (code behaviour, not advice)

1. **`config` / `internal` / cancellation / control signals always bypass `on_error`**: retrying a configuration error is
   useless, fix the configuration; `break` / `continue` / `return` / `exit` are control flow, not errors.
2. **A node enters `on_error` only when its `nr.Status` is `failed`**: errors in a `branch` / `intent` sub-chain propagate
   unchanged per container semantics (that node's status is not failed), so they **do not trigger its own on_error**.
   `process` / `loop` / `function` are the opposite: a sub-chain error marks the container itself `failed`, so the container's
   on_error applies — with the actions it declares: `process` / `loop` declare **`skip` / `handler` / `post`** (see below), `function`
   declares none (its failure belongs to the `call` node that invoked it).
3. **An error code is only valid within the node's declared set**: an undeclared code is downgraded to `other`, so writing that
   code in `codes` no longer matches. On downgrade the message is suffixed with the original code (`… (original error code var)`);
   copy it into `codes`.
4. **Actions a node does not declare support for are simply ignored**: like `post`, `retry` / `skip` / `handler` must be within
   that node's capability declaration, or the configuration is dropped with a warning. For example `intent` declares only
   `retry`, so configuring `skip` on it does nothing; `branch` / `assign` support no action.

## Scope hosts (`process` / `loop`)

A failed member fails the host, so the host's `on_error` is where a scope failure is handled — **once per run, with one
aggregated error**, never once per member. Members run their own `on_error` first (a member that skips never reaches the
host at all), and the host sees only the outcome: one code, one message, its own outputs. The two hosts declare
**`skip` / `handler` / `post`** and nothing else — `retry` is deliberately absent: re-running the body would repeat
every member's side effects, which is not what "retry the failed member" means.

| action | effect on a scope failure |
|---|---|
| `skip` | the host becomes `skipped` and the main chain continues past it; every member keeps its own record, and the skipped host still carries the scope snapshot (`parallel.failed`, `abandoned`) |
| `handler` | the script may **rewrite `error_code` / `error_message`**, or return `action="skip"` to turn the failure into a skip; unless it skips, the run still fails. It cannot write outputs — use `post` for that |
| `post` | writes the host's own outputs (`set_outputs`) on the failure / skip path. This is the marker: with `join: "abort"` the members' outputs are half-written, so `skip` + `post` (`ok = false`) is what tells downstream "this section did not finish" |

Which code arrives — it decides whether `codes` can match at all:

- **a body that runs as a chain**: the failing node's own code propagates unchanged, with nothing to aggregate;
- **concurrent members** (`data.concurrent`): one shared code propagates as is (the message keeps a `member <id>: ` prefix),
  **mixed codes collapse into `other`** with one line per member. So a host-side `codes: ["not_found"]` only holds for a
  chain body or for members that failed the same way — with mixed failures the host only ever sees `other`. Catch those with
  `other` + `patterns`, or put the `on_error` on the member itself, which is the more precise place anyway;
- a member that was **cancelled or abandoned** (`join: "abort"`, a stopped scope) is `cancelled` / `skipped`, **not** a
  failure: a casualty is not a cause, and one fault must not be reported as many;
- `timeout` when the scope deadline fired — the deadline outranks the members it cancelled;
- a failure of the body's **own chain** outranks the member aggregate (the chain's error is the one that stopped the body);
- with `join: drain` the chain nodes after the failure are `skipped`, so a `skip` on the host is the way to keep going at the
  graph level;
- a member that tries `break` / `continue` / `return` records `config` ("cannot leave its body"), and a member panic
  records `internal`; both bypass `on_error` (rule 1), so a `skip` can never mask a platform failure. `exit` is the
  exception: it is global by contract, so from a member it ends the whole run — the member succeeds, the siblings are
  cancelled, and the host reports `exited_by` in its `parallel` snapshot.

## What retries leave behind

Every attempt is a separate record (the first attempt plus each retry). `skip` / `handler` / `post` apply to **the last
attempt**: the node's final `status`, `error_code`, and the outputs written by `post.set_outputs`, all land in that last record.

- so with "all retries failed but skip is configured" the node is `skipped`, not `failed`;
- the `error_code` / `retry_count` seen by a handler script are also from the last attempt, not the first.

## Error codes

`timeout` / `busy` (sandbox concurrency quota exhausted, the script never ran) / `cancel` / `config` / `script` /
`capability` (not enabled on the platform) / `auth` / `not_found` / `connection` (network or sandbox worker unreachable,
**retryable**) / `var` / `internal` (platform-side failure, **bypasses on_error**) / `other`.

Source: `service/flow/executors/runner.go` (`parseOnErrorConfig` / `applyOnError` / `shouldBypassOnError`),
`service/flow/core/nodeResult.go` (error code constants).
