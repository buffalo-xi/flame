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
