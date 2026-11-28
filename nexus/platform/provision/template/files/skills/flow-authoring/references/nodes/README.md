# Node reference (read on demand, not all at once)

The main body covers the main line only; **how to configure a specific node is in its own file, by type**:

```
read_file("/skills/flow-authoring/references/nodes/<type>.md")
```

Each file is 2–6 KB. Do not read the whole directory at once — reading everything is just copying the body again and wastes
context.

## Index

| type | Name | Creatable by `create_flow` | In one line | File |
|---|---|---|---|---|
| `llm` | LLM | ✅ | call the model for text (streaming supported) | `llm.md` |
| `http` | HTTP request | ✅ | send HTTP, SSE supported | `http.md` |
| `code` | Code | ✅ | run a script in the sandbox (languages per the "workflow script languages" list in the system prompt; default lua) | `code.md` |
| `mcp` | MCP | ✅ | call one MCP tool | `mcp.md` |
| `rpc` | RPC | ✅ | call an RPC connection tool, or an endpoint this tenant has exposed | `rpc.md` |
| `branch` | Conditional branch | ⚠️ | which outgoing edge is taken when the condition holds (needs edges) | `branch.md` |
| `intent` | Intent | ⚠️ | let the model pick one of several outgoing edges (needs edges) | `intent.md` |
| `loop` | Loop | ⚠️ | repeat its own child nodes (needs children) | `loop.md` |
| `process` | Flow segment | ⚠️ | inline sub-flow container (needs children) | `process.md` |
| `function` | Function | ⚠️ | inline function callable by `call` only (needs children; outside its body nothing else may reference it) | `function.md` |
| `call` | Call | ✅ | call another `function` in this flow (`target` must be the right id) | `call.md` |
| `assign` | Assign | ✅ | write values into outputs / container variables | `assign.md` |
| `raise` | Raise error | ✅ | fail this step on purpose with a code the caller can classify; terminal, so no outlet | `raise.md` |
| `memory` | Memory | ✅ | fetch conversation memory messages | `memory.md` |
| `result` | Result output | ✅ | emit one result outwards | `result.md` |
| `progress` | Progress output | ✅ | emit one progress update outwards | `progress.md` |
| `break` / `continue` / `return` / `exit` | Control signals | ✅ | break a loop / skip a round / return from a function / terminate | `control-signals.md` |
| `start` / `entry` | Start / entry | — | canvas structure nodes, no executor | `visual.md` |
| `comment` / `layout` | Comment / layout | — | comment is never connected; layout can be connected and the runner passes through it, so `create_flow` can create it as a line-break point | `visual.md` |
| `knowledge` | Knowledge | ❌ | **no backend executor; do not use, and the tools refuse to create it** (kept only to render existing diagrams) | `visual.md` |

> ✅ = runs as soon as it is created; ⚠️ = `create_flow` allows it, but without the right structure it no-ops
> (`branch`/`intent` without `branches`, `loop`/`process`/`function` without children), and the returned `warnings` explain
> each case; whatever the tools cannot cover requires `read_flow` to fetch the diagram and finish it on the front-end canvas.
> `create_flow`'s `steps` describe the nodes themselves, plus `branches` for `branch`/`intent` (one `source<childId>` outgoing
> edge per branch); container children are still not created.

## `config` keys shared by all nodes

| Key | Who reads it | Meaning |
|---|---|---|
| `inputs` | everyone except `process` / `function` / `call` / `memory` / control signals | variable declarations `{type, source_type, source_key, source_ref}`; use `{{ .input.<name> }}` in templates. A `source_type: node` row's `source_key` must be an output the source node really declares (its own `outputs` plus the fixed ones its type writes, for `call` the target function's `outputs`) — the diagram check reports `source_key` otherwise, because the value would be empty with no error at run time. **The excluded types have no reader for the key at all**: `create_flow` / `create_node` / `update_node` delete a stray `config.inputs` there and return a warning, and `validate_flow` reports `ignored_inputs` on a diagram that already carries one |
| `outputs` | everyone except `branch` / control signals / `result` / `progress` | output variable declarations `{type, description?, binding?}`; downstream `source_key` may only use names declared here |
| `locals` | `code` / `loop` / `llm` / `process` / `function` | node-local variables |
| `targets` | `code` (return-value dispatch), `assign`; any node inside a container | writable target declarations: ancestor container outputs/locals (`source_type: node`, `source_ref: <container id>`) or flow variables (`source_type: flow`); `env`/`argument`/`builtin` are read-only |
| `on_error` | all execution nodes | retry / skip / fallback script by error code, see `../on-error.md`. **The action must be within that node's declared capabilities** (`post` for `http` / `llm` / `code` / `mcp` / `rpc` / `call` / `process` / `loop`, `intent` supports `retry` only, `process`/`loop` support `skip`/`handler`/`post` only, `branch`/`assign`/`raise` support none), out-of-declaration config is dropped with a warning |
| `parallel` | `process` / `loop` (the host) | concurrency scope settings `{max_concurrency, join, timeout}`; the members are direct children carrying `data.concurrent: true` on the body's entry chain, and the body's end joins them — see `nodes/process.md` |

## Scripts in `config`

`code` / `branch` / `loop` / `result` / `progress` nodes, plus every `handler`, `on_error` script and binding transform, run
a script — and **the return form depends on the language**: `return` in lua and js, **`__return__ = ...` in python** (a
`return` statement there is a `SyntaxError: 'return' outside function`). What the value has to be differs per surface: an
object of declared variable names for `code` and handlers, a boolean for `branch` / `loop` conditions, a string for
`result` / `progress` and binding transforms.

Full table, examples and the two silent failures (`print` is not a return; `__return__` inside a function needs
`global`): [`../script-languages.md`](../script-languages.md).

A branch / loop **condition** is the one script the static check also reads as text, because a broken one fails the node on
its first evaluation instead of just misbehaving: no return token at all is `condition_no_return` (an error in lua — a bare
expression is a syntax error — and a warning in js / python, where it runs and is simply never true), and an `input.<name>`
the node does not hand in is `condition_input_ref` (an error: the value is nil, so the comparison itself fails). It is a text
check, not a parser: a name reached through a local alias, or a `return` inside a comment, is not judged.

## What creation checks, per type

The tools check the **shape** of a step's `config` hard and its **completeness** only softly. Which is which decides whether a
missing key stops you now or at run time:

| Level | What happens |
|---|---|
| shape | **refused**: a behaviour block (`code` / `http` / `llm` / `loop` / `mcp` / `rpc` / `branch` / `raise`) that is not an object, `children` that is not an array, a `loop.type` outside `count` / `each` / `while` / `do_while` |
| completeness | **the node is still created, but it cannot run**: `code` / `result` / `progress` without `code.script`; `http` without `http.url`; `llm` without `config.llm` or `llm.messages`; `loop` without `config.loop`, without `loop.type`, or with a non-numeric `total_times` / `max_times`; `branch` / `intent` with empty `children`; `call` without `target`; `mcp` without `mcp.connection_id` / `mcp.tool_name`; `rpc` without `rpc.connection_id` / `rpc.tool_name` (`rpc.endpoint` instead in `endpoint` mode); a `lang` the sandbox does not list; a `response_mode` outside `complete` / `sse_stream`. Reported as a returned warning by the tools (a skeleton is allowed), and as an **error** by `validate_flow` / the canvas badge, because the step fails the moment the run reaches it. Three cases stay warnings everywhere, because the step runs: a missing `config.outputs` (the variables the script returns are dropped), an `assign` with no `assignments` (it reports success and changes nothing), and a `branch` child with no condition (the runner skips it, so that path never runs) |
| not at creation | **the per-node check has no neighbours to look at**: a container (`loop` / `process` / `function`) without its `entry` child; `loop each` whose `source_key` is not declared in `config.inputs`; a branch child without an id; a `break` / `continue` that no loop can reach (`control_scope`); `call.args` that does not match the target's `params` (`call_args`); a `source_key` the source node does not produce (`source_key`); duplicate labels; cross-container edges; dangling references; a `source_ref` that names a `function` node from outside its body (`function_ref` — read the `call` node instead); an edge touching a `function` node (`function_edge`); a `function` nested inside another container (`nested_function`); a `targets` entry writing a variable the container does not declare (`target_var`). `validate_flow` / the canvas badge reports these — creation does not |

A successful `create_flow` is therefore **not** a complete node. Close the "not at creation" gaps with `validate_flow`, and
treat every returned warning as a TODO rather than as advice.

`validate_flow` fills in what creation leaves unchecked: it runs the same rule set over a saved flow (or an inline diagram) and
reports missing entries, cross-container edges, dangling `source_ref` / `call.target`, branch children with no
routing edge (an error: the child can never run, and the run fails with a `config` error the moment its condition matches), an
**input row the runner cannot resolve** (`input_source`: no `source_type`, an unknown `source_type`, no `source_key`, a builtin
that does not exist, a `node` row without `source_ref` — `GetVariables` fails the whole block, so the step never runs and the
node reports the failure on its first line rather than with a nil), `loop each` sources that `inputs` does not declare,
unreachable nodes, an `on_error` block the node cannot honour (an action it does not declare is dropped at run time; a code it
can never produce never matches), and — for a saved flow — the **entry-argument declaration against what the diagram reads**
(`argument_undeclared`: a node reads an argument the declaration does not have; `argument_optional`: it is declared but not in
`required`, so a caller may omit it) — with a `node_id` and the config path for each. Call it after creating and after every
structural edit; run the flow only when it reports `error_count: 0`.

`update_node` re-runs the same per-node check on the config it just wrote (a `config` replacement or a `config_patch`
merge) and returns the findings as warnings: a wrong shape is refused before anything is stored, and a key the run cannot
do without is reported (the tool still writes it, the next `validate_flow` calls it an error). Editing one node does not
re-check its neighbours — that is what `validate_flow` is for.

## Three facts about edges

1. Except for `branch` / `intent`, a node **can have only one outgoing edge**: more than one reports a `config` error at
   runtime (it used to silently follow only `out_edges[0]`, and the other edges never ran). Branching requires `branch` /
   `intent`, which pick their own edges.
2. Every node takes **exactly one incoming edge**, and `start` / `entry` / `function` take **none** — the runner enters the
   flow at `start`, a container body at its `entry` child, and a function through a `call` node. `exit` / `break` /
   `continue` / `return` have **no outlet** either, and neither does `raise`: the first four hand control to an enclosing
   scope, while a raise never succeeds, so nothing after it could ever run. `create_edge` refuses all three (a different
   inlet position displaces the previous edge instead of adding a second one); a stored diagram is reported as
   `multi_incoming` / `no_incoming` / `no_outgoing`.
3. A container (`process` / `loop` / `function`) must contain exactly one `entry` child node (`parentNode = container id`,
   `type = "entry"`), and the container runs from that entry along the first outgoing edge to the end; when no entry is found,
   all three containers report a `config` error (no longer a silent skip).
4. After a container (`process` / `loop` / `function`) finishes, execution continues along the container node's own outgoing
   edge; `branch` / `intent` are the exception: the matched branch's sub-chain runs to the end after its own edge, and the
   branch node itself **no longer follows its own outgoing edge** (unmatched branch edges are all skipped).

## Evidence

The facts in this section come from `service/flow/executors/runner.go` (outgoing edges and the scope dispatch),
`execContext.go` (`inputs` / `targets`), each executor's `Execute` (key names and defaults), and the front end's
`desktop/.../flows/nodeFactory.js` (the default structure a new node writes).
