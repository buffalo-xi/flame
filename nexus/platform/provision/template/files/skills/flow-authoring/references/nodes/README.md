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
| `process` | Embedded flow | ⚠️ | inline sub-flow container (needs children) | `process.md` |
| `function` | Function | ⚠️ | inline function callable by `call` (needs children) | `function.md` |
| `call` | Call | ✅ | call another `function` in this flow (`target` must be the right id) | `call.md` |
| `assign` | Assign | ✅ | write values into outputs / container variables | `assign.md` |
| `memory` | Memory | ✅ | fetch conversation memory messages | `memory.md` |
| `wait` | Concurrency join | ⚠️ | wait for a concurrency group (needs a `fork` on the same chain) | `wait.md` |
| `fork` | Concurrency group | ✅ | marks a concurrency group | `fork.md` |
| `result` | Result output | ✅ | emit one result outwards | `result.md` |
| `progress` | Progress output | ✅ | emit one progress update outwards | `progress.md` |
| `break` / `continue` / `return` / `exit` | Control signals | ✅ | break a loop / skip a round / return from a function / terminate | `control-signals.md` |
| `start` / `entry` | Start / entry | — | canvas structure nodes, no executor | `visual.md` |
| `comment` / `layout` | Comment / layout | — | comment is never connected; layout can be connected and the runner passes through it, so `create_flow` can create it as a line-break point | `visual.md` |
| `knowledge` | Knowledge | ❌ | **no backend executor; do not use, and the tools refuse to create it** (kept only to render existing diagrams) | `visual.md` |

> ✅ = runs as soon as it is created; ⚠️ = `create_flow` allows it, but without the right structure it no-ops
> (`branch`/`intent` without `branches`, `loop`/`process`/`function` without children, `wait` without a concurrency group),
> and the returned `warnings` explain each case; whatever the tools cannot cover requires `read_flow` to fetch the diagram
> and finish it on the front-end canvas.
> `create_flow`'s `steps` describe the nodes themselves, plus `branches` for `branch`/`intent` (one `source<childId>` outgoing
> edge per branch); container children and concurrency groups are still not created.

## `config` keys shared by all nodes

| Key | Who reads it | Meaning |
|---|---|---|
| `inputs` | everyone except `process` / `function` / `call` / `memory` / `wait` / `fork` / control signals | variable declarations `{type, source_type, source_key, source_ref}`; use `{{ .input.<name> }}` in templates |
| `outputs` | everyone except `branch` / `fork` / `wait` / control signals / `result` / `progress` | output variable declarations `{type, description?, binding?}`; downstream `source_key` may only use names declared here |
| `locals` | `code` / `loop` / `llm` / `process` / `function` | node-local variables |
| `targets` | `code` (return-value dispatch), `assign`; any node inside a container | writable target declarations: ancestor container outputs/locals (`source_type: node`, `source_ref: <container id>`) or flow variables (`source_type: flow`); `env`/`argument`/`builtin` are read-only |
| `on_error` | all execution nodes | retry / skip / fallback script by error code, see `../on-error.md`. **The action must be within that node's declared capabilities** (`post` only for six types, `intent` supports `retry` only, `wait` only `skip`, `branch`/`fork`/`assign` support none), out-of-declaration config is dropped with a warning |
| `concurrency` | execution nodes (llm / code / http / mcp / rpc / call, etc.) | **Off by default: do not write this key** (the tools strip it and return a warning). Write it only when the user explicitly asks for concurrency, choosing as they ask: `{"mode":"mono"}` mono concurrency (one node processes several records concurrently), `{"mode":"fork","fork_node_id":"<id>"}` concurrency group (several nodes run at once, a `wait` joins them). Also declare `concurrency=mono\|group` in the tool arguments, or it is stripped again. **`loop` / `process` containers never carry this key** |

## Scripts in `config`

`code` / `branch` / `loop` / `result` / `progress` nodes, plus every `handler`, `on_error` script and binding transform, run
a script — and **the return form depends on the language**: `return` in lua and js, **`__return__ = ...` in python** (a
`return` statement there is a `SyntaxError: 'return' outside function`). What the value has to be differs per surface: an
object of declared variable names for `code` and handlers, a boolean for `branch` / `loop` conditions, a string for
`result` / `progress` and binding transforms.

Full table, examples and the two silent failures (`print` is not a return; `__return__` inside a function needs
`global`): [`../script-languages.md`](../script-languages.md).

## What creation checks, per type

The tools check the **shape** of a step's `config` hard and its **completeness** only softly. Which is which decides whether a
missing key stops you now or at run time:

| Level | What happens |
|---|---|
| shape | **refused**: a behaviour block (`code` / `http` / `llm` / `loop` / `mcp` / `rpc` / `branch`) that is not an object, `children` that is not an array, a `loop.type` outside `count` / `each` / `while` / `do_while` |
| completeness | **the node is still created, but it cannot run**: `code` / `result` / `progress` without `code.script`; `http` without `http.url`; `llm` without `config.llm` or `llm.messages`; `loop` without `config.loop`, without `loop.type`, or with a non-numeric `total_times` / `max_times`; `branch` / `intent` with empty `children`; `call` without `target`; `wait` without `fork_node_id`; `mcp` without `mcp.connection_id` / `mcp.tool_name`; `rpc` without `rpc.connection_id` / `rpc.tool_name` (`rpc.endpoint` instead in `endpoint` mode); a `lang` the sandbox does not list; a `response_mode` outside `complete` / `sse_stream`. Reported as a returned warning by the tools (a skeleton is allowed), and as an **error** by `validate_flow` / the canvas badge, because the step fails the moment the run reaches it. Three cases stay warnings everywhere, because the step runs: a missing `config.outputs` (the variables the script returns are dropped), an `assign` with no `assignments` (it reports success and changes nothing), and a `branch` child with no condition (the runner skips it, so that path never runs) |
| not at creation | **the per-node check has no neighbours to look at**: a container (`loop` / `process` / `function`) without its `entry` child; `loop each` whose `source_key` is not declared in `config.inputs`; a branch child without an id; a `break` / `continue` that no loop can reach (`control_scope`); `call.args` that does not match the target's `params` (`call_args`); duplicate labels; cross-container edges; dangling references. `validate_flow` / the canvas badge reports these — creation does not |

A successful `create_flow` is therefore **not** a complete node. Close the "not at creation" gaps with `validate_flow`, and
treat every returned warning as a TODO rather than as advice.

`validate_flow` fills in what creation leaves unchecked: it runs the same rule set over a saved flow (or an inline diagram) and
reports missing entries, cross-container edges, dangling `source_ref` / `call.target` / `fork_node_id`, branch children with no
routing edge, `loop each` sources that `inputs` does not declare, unreachable nodes, and an `on_error` block the node cannot
honour (an action it does not declare is dropped at run time; a code it can never produce never matches) — with a `node_id` and
the config path for each. Call it after creating and after every structural edit; run the flow only when it reports
`error_count: 0`.

`update_node` re-runs the same per-node check on the config it just wrote (a `config` replacement or a `config_patch`
merge) and returns the findings as warnings: a wrong shape is refused before anything is stored, and a key the run cannot
do without is reported (the tool still writes it, the next `validate_flow` calls it an error). Editing one node does not
re-check its neighbours — that is what `validate_flow` is for.

## Three facts about edges

1. Except for `branch` / `intent`, a node **can have only one outgoing edge**: more than one reports a `config` error at
   runtime (it used to silently follow only `out_edges[0]`, and the other edges never ran). Branching requires `branch` /
   `intent`, which pick their own edges.
2. A container (`process` / `loop` / `function`) must contain exactly one `entry` child node (`parentNode = container id`,
   `type = "entry"`), and the container runs from that entry along the first outgoing edge to the end; when no entry is found,
   all three containers report a `config` error (no longer a silent skip).
3. After a container (`process` / `loop` / `function`) finishes, execution continues along the container node's own outgoing
   edge; `branch` / `intent` are the exception: the matched branch's sub-chain runs to the end after its own edge, and the
   branch node itself **no longer follows its own outgoing edge** (unmatched branch edges are all skipped).

## Evidence

The facts in this section come from `service/flow/executors/runner.go` (outgoing edges and `concurrency`),
`execContext.go` (`inputs` / `targets`), each executor's `Execute` (key names and defaults), and the front end's
`desktop/.../flows/nodeFactory.js` (the default structure a new node writes).
