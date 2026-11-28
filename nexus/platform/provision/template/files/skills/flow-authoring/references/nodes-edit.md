# Changing structure: `create_node` / `insert_node` / `update_node` / `delete_node`

Supplying all `steps` to `create_flow` in one go is the least work. For a missed node, an insertion mid-chain, a `config`
change or a move into a container, use these four. They touch only `nodes` (plus the `edges` that are necessarily affected) and
nothing outside the flow diagram.

## Which one to use

| Goal | Tool |
|---|---|
| Add a node (top level or inside a container) | `create_node` |
| Insert a node somewhere in an existing chain | `insert_node` (atomic: delete the old edge + connect A→new→B) |
| Change the label / config / move into a container / move it | `update_node` |
| Delete a node (with its edges and subtree) | `delete_node` |

A newly created node has no edges yet — **the next step is `create_edge`** (the `handles` in the return value state the
positions available).

## `create_node`

```json
{"flow": "daily-sales-report", "type": "code", "label": "clean"}
{"flow": "daily-sales-report", "type": "http", "parent_node": "7"}
```

Arguments: `flow`, `type`, `label?`, `config?`, `parent_node?`.

- Without `config` the **type's default template is used** (the same one the front end's "New node" writes: `http` comes with
  outputs, `assign` with empty `inputs/outputs/targets/assignments`, `loop` with `loop` + `loop_vars`, `branch`/`intent`
  automatically with one `children` entry). If you supply it, it is used as-is and missing keys are not filled in — the one
  exception is `config.inputs` on a type that reads no `inputs` (`process` / `function` / `call` / `memory` / control
  signals): it is removed with a warning.
- `parent_node` may only be the id of a `loop` / `process` / `function`. Passing it builds the node inside that container
  (coordinates are relative to the parent, with `extent` constraining it inside the parent).
- **Containers get an `entry` automatically**; if the parent container had none, one is added along the way (returning
  `parent_entry` and a warning) — a container without an entry "succeeds but runs nothing".
- `function` can only be created at the top level; `return` only inside a `function` container; `start` / `entry` cannot be created manually.
- Returns: `node` `entry` (of the container just created) `parent_entry` (the one added) `handles` `warnings` `node_count` `edge_count`.

## `insert_node`

```json
{"flow": "daily-sales-report", "edge_id": "04958", "type": "code", "label": "audit"}
```

- `edge_id` comes from `read_flow`'s `diagram.edges[].id`.
- The new node lands under the same `parent` as that edge; the old edge is deleted and replaced by two: `A→new` and `new→B`.
- Only a node that ends up with **both an incoming and an outgoing edge** can be inserted: `function` (no edges) and
  `exit`/`break`/`continue`/`return` (no outgoing edge) are rejected.
- Returns: `node` `edges` (the two edges) `handles` `warnings`.

## `update_node`

```json
{"flow": "daily-sales-report", "node_id": "3", "label": "new title"}
{"flow": "daily-sales-report", "node_id": "3", "config": {...}}          // full replacement
{"flow": "daily-sales-report", "node_id": "3", "config_patch": {"limit": 5, "window": null}}  // top-level merge, null = delete the key
{"flow": "daily-sales-report", "node_id": "4", "parent_node": "7"}       // move into a container
{"flow": "daily-sales-report", "node_id": "4", "parent_node": ""}        // move back to the top level
{"flow": "daily-sales-report", "node_id": "3", "position": {"x": 1240, "y": 320}}  // move it
```

- `config` and `config_patch` are **mutually exclusive**. The return value carries the final `config`; glance at it afterwards.
- **`config_patch` deletes only a top-level key.** `{"args": {"c": null}}` does not remove `c` from `args` — the nested object
  is merged, `c` survives as null, and the run then reports `unknown arg "c"`. To drop something inside a nested object, pass
  that whole object (`{"args": {"a": {...}, "b": {...}}}`), or replace `config` wholesale.
- **Node references accept a label or an id**, in all four places that hold one: `config.inputs.*.source_ref`,
  `config.targets.*.source_ref`, `config.args.*.source_ref` and `config.target`. A value that
  matches no node id is rewritten to the label's node; an ambiguous label is left alone. Ids are still safer: read them back
  with `read_flow` once the graph has branches or containers.
- **A node never references itself**, with one exception: a `while` / `do_while` loop's `config.inputs` is re-resolved
  before every condition check, so its condition may read the loop's own `outputs` / `locals` / `loop_vars` — that is how an
  accumulator is written (`references/nodes/loop.md` has the pattern). Everywhere else — any non-loop node, and `count` /
  `each`, which snapshot their inputs once before the first round — `config.inputs.*` / `config.args.*` is resolved before
  the node runs, so its own `outputs` / `locals` are still empty and the run fails with `failed to get variable …: var path …
  not found`. Read an upstream node instead; for a value that has to change between rounds use a flow variable, or write into
  the enclosing container's `outputs` / `locals` (`targets`). `validate_flow` reports `self_ref` for those cases, and the
  same scan covers `args` for `dangling_ref` / `function_ref`.
- `position` is canvas coordinates: absolute for top-level nodes, **relative to the parent container** for nodes inside one.
  Use it to fine-tune when automatic layout looks off, following the same rules as "Layout" in `SKILL.md`: `x`/`y` are
  multiples of 20, the main chain shares one `y` with an `x` step of 200 between adjacent leaves (380 after `branch` /
  containers), branches go to the right of the branch node — the first one on the node's own row, the rest below it (rows ≥
  72 apart) — and nothing overlaps an existing node. The returned `node.position` is the coordinate as stored.
- When changing `parent_node`: **edges crossing between the old and new container are deleted** (cross-`parent_node` edges were
  never valid), returned in `removed_edges`; reconnect them inside the new container with `create_edge`.
- A node cannot be moved into its own subtree; `function` cannot be moved into a container; `start` / `entry` cannot be moved;
  a `return` cannot be moved **out** of a function (outside one it would end the whole flow). The node keeps its id; without an
  explicit `position` it is placed at a free spot inside the new container, so pass `position` when the default is not where you
  want it.

## `delete_node`

```json
{"flow": "daily-sales-report", "node_id": "4"}
```

- Deletes every edge connected to that node along with it.
- Deleting a container = deleting its whole subtree (including its own `entry`); `deleted_nodes` lists them all.
- **Refused while referenced**, listing the referrers:
  - `config.inputs.<key>.source_ref`
  - `config.target` (the `call` target)

  Change those references with `update_node` first, or the run fails to resolve values right after the delete.
- `start` cannot be deleted (the whole graph could not run); `entry` cannot be deleted on its own (it goes with the container).

## Labels must be unique across the graph

`label` is the **writable form** of `config.inputs.*.source_ref` and `call.config.target` (the tool replaces it with the real
id), so a duplicate makes those references point at the wrong node. Labels must be unique within the whole flow graph:

| Situation | What happens on a collision |
|---|---|
| `steps[].label` written explicitly in `create_flow` | **error**, naming the step and the node id that holds it |
| `label` on `create_node` / `insert_node` | **error** |
| Renaming via `update_node` | **error** (renaming to its own current name is not a collision) |
| Omitted `label` (default name), container-generated `entry` | **a `-2` / `-3` suffix is appended automatically**; no failure |
| Dragging a node in on the canvas / renaming in the panel | **a 3-character alphanumeric suffix is appended** (e.g. `code-x7k`), with a notice that it was renamed |
| The save endpoint (canvas "Save") | **refuses to save**: duplicate node name ("Code" used by nodes 2 and 3)…; rename before saving |

Therefore:

- to reference a step reliably, **give it a distinct name**; duplicates are blocked, so it never silently points elsewhere;
- the second "Code" node of the same type is automatically called `code-2`; do not assume both are called "Code";
- the second container's entry is called `entry-2` (`entry` participates in uniqueness too).

## Container structure conventions

A container (`loop` / `process` / `function`) executes from the node where `parent_node == container id && type == "entry"`.
When no entry is found it reports a `config` error at runtime (`... body entry node not found`), no longer a silent skip. So:

- after adding a node into a container, confirm the container has an `entry` and that `entry` has an outgoing edge to the first
  child node (`create_edge`);
- nodes inside a container may be connected freely, but **never across containers**: two nodes with different `parent_node`
  cannot be connected;
- containers can nest (`process` inside `loop`); `return` must be inside a `function` at any ancestor level.

## Division of work with the other tools

| Tool | Boundary |
|---|---|
| `create_flow` | builds the whole main chain at once (including `branches` / `children`); least work for structure within the steps |
| `create_node` / `insert_node` | create nodes only and do not connect them for you (`insert_node` excepted — it brings its two edges) |
| `create_edge` / `update_edge` / `delete_edge` | edges only; create no nodes, change no `config` |
| `update_node` | changes only `label` / `config` / `parent_node` / `position`, never edges (except cleaning up cross-container edges) |
| `delete_node` | deletes the node plus its edges; never rebuilds |
