# Structural and purely visual nodes

These kinds have **no execution logic**, but wiring them wrongly stops the whole chain from running.

## `start`

- In a new flow the convention is **id = `"1"`**, `type: "start"`, `data: {"label": "start"}`, **no `config`**.
- At runtime the entry point is recognised by **`type == "start"`** (`id == "1"` is only a preferred hit, with the type as a
  fallback when it is absent), and the node is skipped outright (it never reaches an executor), execution starting from its
  first outgoing edge; skipping is decided by type in the same way.
- The flow's **argument schema and limits** (`arguments` / `timeout` / `max_*`) are not in the diagram; they are edited in the
  front end's "start" node property panel and land on the flow entity. `create_flow` **does not write `arguments`** — which is
  why flows built by the tools have no argument documentation on their endpoints.

## `entry` (container entry)

- The first node inside a container (`process` / `loop` / `function`): `type: "entry"`, `parentNode: <container id>`,
  `data.label` is uniformly `entry`, no `config`.
- Generated automatically when the front end creates a container; at runtime it is skipped too, serving only as the starting
  point of the chain inside the container.
- **Every container has exactly one**; without an entry the container simply succeeds and is skipped.

## `comment` and `layout`

- `comment`: purely visual, has no Handle, **never connect it** (once an imported edge connects it, it reports
  `no executor registered for node type: comment`).
- `layout`: a placeholder / grouping container, **may be connected**; the runner skips the node itself and continues along its
  first outgoing edge, which makes it suitable for framing a run of nodes without affecting execution.
- `layout` can be created directly in `create_flow`'s `steps` (`type: "layout"`, a 32px dot, counted as 60 wide); the common
  use is a **line-break point**: when the main chain reaches 1500px, insert a layout and place the following nodes on the next
  row, keeping the diagram squarer instead of a 3000px strip.
- Neither has a backend **executor**.

## `knowledge`

- The front end has a component, but **no executor is registered in the backend** (`RegisterExecutor` has no `knowledge`).
  Wiring it into a chain yields exactly one thing: `no executor registered for node type: knowledge`.
- **`create_node` / `insert_node` refuse to create it too** (the front-end palette dropped the entry long ago; the mapping is
  kept only to render existing diagrams).
- For knowledge retrieval use `mcp` / `rpc` nodes calling the relevant service, or let the `llm` node carry the context itself.

Source: `service/flow/executors/runner.go` (skips `start` / `entry` / `layout` by `type`), the `registry` registration table
