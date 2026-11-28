# Workflow Authoring Guide

## Purpose

Turn a **fixed-order, repeatedly executed** process (LLM / script / HTTP / MCP / RPC wired together) into a tenant-level
workflow, and when needed expose it as an external **mcp / rpc endpoint**.

This skill covers: deciding whether a workflow is the right shape, what the tools can build, what each node type's
`config` looks like, how to pass one step's result to the next, and how to expose and grant endpoints.

## Two ways to get it done

| Who | How | What you get |
|---|---|---|
| Super agent | `create_flow` → `run_flow` test run → (optional) `grant_agent_tool` | linear flows in one shot; run it, then expose |
| A person | "Workflows → New" and build on the canvas | every node type (branch / loop / sub-flow) |

Twenty meta tools (category `flow-admin`, super agent only). **Building a complex flow = these tools working together**:

| Tool | Role |
|---|---|
| `create_flow` | create the entity and assemble the "start → steps" main chain (including `branches` sub-chains and `children` container sub-chains); may declare entry arguments (`arguments`) and expose mcp / rpc endpoints along the way |
| `update_flow_arguments` | add / update / remove entry arguments: `set` replaces one by name, `remove` deletes, `required` resets the required list |
| `list_flows` | list all workflows in this tenant (including the endpoint protocols already exposed) |
| `read_flow` | fetch the diagram source by id or name |
| `run_flow` | **test run**: really execute once, returning the overall status plus per-node success or failure (failed nodes carry an error code, stack and logs) |
| `read_flow_run` | **inspect a run**: per-node inputs / outputs / local variables and error details by `run_id` |
| `delete_flow` | delete the definition **and everything derived from it** (endpoints, version history and run records all go; unrelease / retract first); echoes the deleted `flow_id` + `flow_name` |
| `create_flow_endpoint` | add the workflow's **MCP** endpoint slot (`protocol=mcp`; a newly created slot is not exposed unless `exposed=true` — and while the flow has no released version the slot is still created unexposed, the answer carrying `release_required`). `protocol=rpc` changes nothing: the RPC slot is created with the flow and only has an exposure switch |
| `delete_flow_endpoint` | delete the **MCP** slot (retract it first). `protocol=rpc` is refused — it is managed by the flow, so take it offline with `retract_flow_endpoint` or by deprecating the flow |
| `expose_flow_endpoint` | expose the workflow's `mcp` / `rpc` endpoints to callers outside the tenant; **needs a released version**, so a flow you just created must be released by the user in the console first |
| `retract_flow_endpoint` | retract them (omit `protocols` for all); never refused, and the prerequisite for deleting an exposed MCP slot |
| `create_node` | **add a node** (`parent_node` builds a child node inside a container); containers get an `entry` automatically |
| `insert_node` | insert a node **in the middle of an existing edge**: delete the old edge + connect A→new→B, atomically |
| `update_node` | change `label` / `config` (full replacement or `config_patch` merge) / `parent_node` (move into a container) |
| `delete_node` | delete a node and its edges; a container takes its whole subtree; refused while referenced |
| `create_edge` | **connect an edge** in an existing flow (both handles required); useful for wiring container children or reconnecting the main chain |
| `update_edge` | change one edge's source / target / handle (by `edge_id`) |
| `delete_edge` | delete one edge (by `edge_id`), echoing the deleted edge |

**Pass `flow` as an id or a name?** They are equivalent: a UUID-shaped value is looked up as an id, anything else as a name
(case-insensitive, surrounding whitespace ignored). Default to the **name** — fewer characters, no copy mistakes, and it
saves a `list_flows` call; once you have a `flow_id`, use the id if you need to pin the same workflow (a series of
operations, where a rename in between would be a problem). **Copy ids only from tool returns, never assemble them yourself**:
a made-up id has a valid shape and only earns a "not found", wasting a turn. Both `run_flow` / `read_flow_run` return
`flow_id`, so you can chain straight from them.
`read_flow_run`'s `run_id` is the exception: a run has only an id, never a name.

Agents other than the super agent do not have these tools; they can only hand the orchestration design to the user to build
in the front end.

## When to use

When these intents appear:

- "pull X every day / week, summarize it, then send it out" — fixed order and repeated execution
- "make this process an MCP tool so other agents can call it" — build the flow + expose the endpoint
- "the results this flow produces are wrong" — `read_flow` for the structure, the front end for run records

**Decide the shape first; do not default to a workflow:**

| Need | Use | Rule of thumb |
|---|---|---|
| One-off task | just do it (existing tools / `exec`) | runs once, no reuse |
| Pure function: model supplies arguments, structured result | custom tool (`custom-tool-authoring`) | stateless, single step |
| Knowledge / conventions / phrasing | a skill (`skill-authoring`) | nothing executable |
| Needs container children (loop / sub-flow / function body), or a concurrent body | **workflow** (the tools can build it) | `create_flow`'s `children` or `create_node(parent_node=…)`; `entry` is added automatically |
| Fixed order + several node kinds + an external entry point | **workflow** | this skill |

## Hard constraints (read first, to avoid wasted work)

| Constraint | Details |
|---|---|
| Name | letters, digits and dashes only (no leading dash, ≤64), **Chinese is not allowed**. It *is* the endpoint name → the MCP `tool.name`, the agent-side tool name `local__<name>`, and the grant ref `local:rpc:<name>`; the latter two accept ASCII only. Put the Chinese purpose in `description`. Unique across the tenant, and **must not collide with an agent name on the same protocol** — the endpoint name always equals the entity name, and `svc_endpoints` is unique on `(name, protocol)`, so the collision fails the insert that creates the slot, not a later expose: the RPC slot goes in when the flow is created (`create_flow` itself fails), the MCP slot when `create_flow_endpoint` runs |
| Description | required: lists distinguish purposes by it, and the front-end editor rejects an empty description |
| Shape | the top level is a **linear main chain**: `start → step 1 → step 2 → …`; `branch` / `intent` pull sub-chains out with `branches`, and `loop` / `process` / `function` hold sub-chains in `children` (`entry` is added automatically). For anything else (inserting a node mid-chain, moving into a container, changing config, deleting a node) use `insert_node` / `update_node` / `delete_node` |
| Supported nodes | every type the executor registers can be created: `llm` `code` `http` `mcp` `rpc` (single in, single out, runnable as-is), `branch` `intent` `loop` `process` `function` `call` `assign` `memory` `raise` `result` `progress`, `break` `continue` `return` `exit`. `raise` is terminal: it takes an incoming edge and no outgoing one, because it never succeeds. `branch`/`intent` build their outgoing edges from `branches` (omit it and they no-op). `start` (generated automatically), `entry` (added by containers) and `knowledge` (no backend executor) **cannot be created**; `comment` (a pure note, not connected) and `layout` (runner passes straight through) have no executor but can be created |
| Coordinates | either way: give `position` and the layout is yours (all top-level nodes or none, see "Layout" below); omit it and the tool lays it out (main chain left to right, the branches of `branch` / `intent` opening to the right of their card — the first branch row on the card's own row, the rest one row each below it — containers sized by content). Position does not affect execution |
| Concurrency | One model: the **scope**, and it is set in two places only — `concurrent: true` on a **direct child** of a `process` / `loop` (node data, so the `concurrent` argument), and the host's `config.parallel` (`max_concurrency`, `join`, `timeout`). The body dispatches the members and joins them at its end. A member failure fails the host, and the host's own `on_error` is where that is handled: `skip` / `handler` / `post` (no `retry` — see `references/on-error.md`) — see `references/nodes/process.md` |
| `inputs` on a type that reads none | **never** write `config.inputs` on `process` / `function` / `call` / `memory`, or on `break` / `continue` / `return` / `exit`. Nothing reads it: a `process` body reaches the outer nodes directly (a `source_type: node` reference resolves up the process chain) and writes back through `targets`; a `function` gets its values from a `call` node's `args`; a `call` node's arguments go in `config.args`. The tools remove the block on save and return a warning, and `validate_flow` reports `ignored_inputs` on diagrams that already carry one — never "fix" that warning by moving the values somewhere else, the block itself was the mistake |
| Control signals | `break` / `continue` are consumed by the nearest **loop** (a function boundary ends that scope). `return` is consumed by the nearest **function**: inside a function body, nested containers included, it returns from the function (which succeeds) and `call` receives its outputs. **A return must have a function above it** — at the top level or in a `process` / `loop` body the same node ends the whole flow while the run reports success, so `create_flow` / `create_node` refuse it and `validate_flow` reports `return_scope` as an error. Use `exit` to end the whole flow; to leave only a container, route around the rest of the body with a `branch` |

### Layout (you may place coordinates yourself)

Every step of `create_flow` can carry `position: {x, y}`, and **either every top-level node has one or none does**:

- all given → laid out exactly at your coordinates; the tool only detects overlaps (conflicts go into `warnings`, your coordinates are not changed);
- one missing → the whole flow falls back to automatic layout, and `warnings` names what was missing;
- container children need none (coordinates are relative to the parent container; the tool lays them out and writes `config.size`).
- `function` nodes are **not** part of the main row: the tool places them below the flow (a function is entered by a `call`,
  not by an edge) and sizes the card to its body. A function body is laid out like the main flow — `entry` at the left, the
  chain to the right, branch rows opening further right — while `loop` / `process` children are stacked vertically, one
  iteration per row.

Aesthetic baseline (following it matches the flow's intent better than automatic layout):

| Item | Requirement |
|---|---|
| Grid | `x` / `y` are multiples of 20 |
| Main chain | same row, same `y`, left to right |
| Horizontal spacing | a leaf node is only 100–160 wide in practice (`min-width: 100px`; close to 300 only with very long labels): use an `x` step of **200** between adjacent main-chain nodes (leaving roughly 60–100 of gap); `branch` and containers are 300 wide, so use a step of **380** after them. Do not stretch past 300 "to be safe" — the canvas looks emptily ugly |
| Vertical spacing | ≥ 40 (a leaf is 32 tall) |
| Branches | placed to the right of the branch node — the first branch on the node's own row (so its right outlet feeds the sub-chain's left inlet straight), the rest one row each below it; rows ≥ 72 apart and each row runs left to right |
| Wrapping | wrap once a row is wider than 1500, measured from that row's own start (a branch row begins at the branch card's right edge and still gets a full row); to control where it turns, insert a `type: "layout"` step there |
| Edges | avoid crossings; on conflict reorder the branches or widen the gap, do not route around with diagonal lines. Choose handles from the two nodes' relative positions (same row → left/right ends; big vertical offset → vertical ends) — the exact positions and suggested values are in `create_edge`'s parameter description, and a missing handle makes the tool state the value to use |

**Reuse vertical space with `layout` nodes**: `layout` is a 32px dot that the runner skips, continuing along its first outgoing
edge (it does not affect execution order), so it can sit in the main chain as a "line-break point" or a "group anchor":

```json
[
  {"type": "code", "label": "fetch", "position": {"x": 1200, "y": 60}, "config": {...}},
  {"type": "layout", "label": "wrap", "position": {"x": 1560, "y": 60}, "config": {}},
  {"type": "code", "label": "clean", "position": {"x": 120, "y": 220}, "config": {...}}
]
```

It is purely a visual turn; you can also use it to "frame" a run of nodes (place it above or to the left of the group as an
anchor).

After creation the returned `nodes` carry `position`; if you dislike it, move a node on its own with `update_node`'s
`position`.
| Status | "normal" on creation; deprecating is console-only, meta tools cannot change it |
| Endpoints | only an exposed endpoint can be referenced by a "service credential" or an agent grant; a flow that is not exposed is invisible outside |
| Run records | deleting a flow **deletes its run records too**: `flow_run_records` / `flow_node_records` of that flow go in the same transaction as the row, and the version history cascades by foreign key. To drop old runs but keep the flow, use the run history page's clean-up — deleting the flow is not that tool |

## Variables: three concepts, do not mix them

1. **`config.inputs`** — this node's input declarations (variable name → where the data comes from)
2. **`{{ }}` templates** — reference resolved inputs and node outputs inside url / body / messages / script
3. **`config.outputs`** — this node's output variable declarations (script nodes must declare them; other nodes have fixed outputs)
4. **`config.targets`** — where a step **writes** a variable (a container's output / local, or a flow variable)

### Writing `config.inputs`

```json
"inputs": {
  "question": {"type": "string", "source_type": "argument", "source_key": "question"},
  "prev":     {"type": "string", "source_type": "node", "source_ref": "2", "source_key": "content"},
  "token":    {"type": "string", "source_type": "global", "source_key": "CRM_TOKEN"}
}
```

| source_type | Taken from | Key fields |
|---|---|---|
| `argument` | this run's arguments | `source_key` = the argument name, supports `a.b` / `a[0]` |
| `node` | an earlier node's output | `source_ref` = **node id**; `source_key` = output variable name. **Never a `function` node seen from outside its body** — see the rule below |
| `env` | flow environment variables (`flows.env_vars`) | `source_key` = variable name |
| `global` | tenant-level global variables ("Settings → Global variables") | `source_key` = variable name |
| `flow` | flow variables (writable at runtime) | `source_key` = variable name |
| `builtin` | run context (read-only) | `session_id` / `run_id` / `user_id` / `schema` / `flow_id` / `language` (the run's output language, empty when the caller specified none) |

A missing value **fails outright** (the node fails); it never silently yields empty — when debugging, first check whether
`source_key` is misspelled.
`global` behaves the same: a variable name that does not exist, or cannot be read at runtime, fails the node, and the error
names the variable.

The one case that reads as empty instead: a **declared** variable (a container's `outputs` / `locals`, a `call`'s outputs, a
flow variable) is initialised to its type's zero value before the run. A member that never wrote it back therefore yields
`""` / `0` / `{}` — no error at all. So "did the concurrent member actually produce something" has to be checked by testing
the value (a `branch`, an `assign`, or a `code` script that fails on a blank), never by expecting a failure.

An `argument` row is only readable when the flow's entry-argument declaration declares that name (`arguments` on
`create_flow`, `set` on `update_flow_arguments`), and it is only **guaranteed to be there** when the declaration also lists it
in `required`: that declaration is the endpoint's `input_schema`, and nothing on the server rejects a call that omits a
parameter the schema calls optional — the node then fails with `var path <name> not found`. So declare every argument a node
reads, and mark it required unless the node tolerates an absent value. `validate_flow` reports `argument_undeclared` (error:
the name is not declared at all) and `argument_optional` (warning: declared, but a caller may omit it).

**A `function` node is not a variable source.** Its `params` and `outputs` belong to the body and are readable only by the
steps inside that body. From outside, read the **`call` node** instead — its `outputs` are copied from the function's — and
never put a function's id in `source_ref` (in `inputs`, `targets` or `args`): the value is empty at run time and
`validate_flow` reports `function_ref`. The same rule for edges: nothing wires into a function, only `call` reaches it.

### Writing `config.targets`

A step **writes** through `targets` (a container's output / local, or a flow variable) — the mirror of `config.inputs`:

```json
"targets": {"sum": {"type": "number", "source_type": "node", "source_ref": "<container node id>", "source_key": "sum"}}
```

| Field | Meaning |
|---|---|
| `type` | the variable's type, same vocabulary as `outputs` |
| `source_type` | `node` = the container holding this step (`source_ref` = its node id); `flow` = a flow variable |
| `source_ref` | required for `node`: the container node's id |
| `source_key` | the variable name to write; **omitted → the outer key** (what the step returns). It must exist in the container's `outputs` / `locals`, or as a flow variable, or the run fails with `output var X not defined` |

The outer key is what the step returns, and the two may differ — that is how a script key is mapped onto a differently named
variable. A name may not appear in both `outputs` and `targets` (`declared in both outputs and targets`).

### Template syntax

- `{{ .input.xxx }}` — the inputs resolved from `config.inputs` above (global variables land here too)
- `{{ .output.xxx }}` / `{{ .local.xxx }}` — this node's existing outputs / local variables (used inside a handler)
- functions: `json` / `truncate` / `default` / `upper` / `lower` / `trim`

A wrong field name in a template **does not error** — it renders as `<no value>`; when you see that, come back and check the
inputs.

**There is no `.global_vars` context**: global variables only enter through input variables (`source_type: "global"`), so
always write `{{ .input.xxx }}` in templates. The old `{{ .global_vars.XXX }}` was removed — leaving it in a config makes the
flow **fail at load time** (it does not silently render `<no value>`).

### External configuration: environment variables and global variables

Both keep configuration out of the flow definition, but their **scope and creation entry point differ**, while the way to
reference them is the same (both through `config.inputs`):

| | Environment variables | Global variables |
|---|---|---|
| Scope | a single flow (`flows.env_vars`) | every flow / agent / skill in this tenant |
| Who creates it | only a person, by hand in the canvas "Variables" panel; tools cannot, so you can only reference an existing one | "Settings → Global variables", maintained by an administrator / operator |
| Reference | `{"source_type": "env", "source_key": "<name>"}` | `{"source_type": "global", "source_key": "<name>"}` |

Environment variables have three types: `string` / `number` / `secret`
(`secret` is never shown in plaintext by the editor; submitting an empty value keeps the old one; it is not exported with an
asset pack; overwrite installs keep the target tenant's value).
**The `type: "global"` bridge is removed** — reference global variables directly with `source_type: "global"`.

Global variables have four kinds: `text` / `secret` / `number` / `bool`, and enter `{{ .input.xxx }}` as their native type
after resolution:

```json
"inputs": {
  "token": {"type": "string", "source_type": "global", "source_key": "CRM_TOKEN"}
},
"headers": [{"key": "Authorization", "value": "Bearer {{ .input.token }}"}]
```

This is the **recommended** form: the value never lands in the flow definition, an asset export carries only the reference
name, and after installing into another tenant the target tenant's value fills it in.
A misspelled name **fails hard** and names the variable; it never silently runs empty.

When you do not know which variables this tenant has, call `list_global_vars` first (names / types / descriptions only,
**never values**) — do not guess.

**Two things to watch**:

1. **Referencing a `secret` global variable inside `llm.messages` or `intent.llm.system_prompt` is not blocked, but it is
   logged as a warning.** The value enters the model context and run records, so the node log shows "the prompt references a
   secret-type global variable". Prefer using secrets in connection / tool configuration (`http.headers`, `mcp`/`rpc`
   connection parameters), not in prompts.
2. **Never write secret strings directly into config.** On listing, values are cleared by key name (api_key / token / secret /
   password / credential / authorization), the installer has to fill them in again, and carrying them across tenants is a leak.

## Iron rule: node config must be complete (only shapes are refused, completeness is merely warned)

The tools decide two different things about a step's `config`. Knowing which is which tells you what a successful call
actually proved:

- **shape is refused** — the call fails: a behaviour block (`code` / `http` / `llm` / `loop` / `mcp` / `rpc` / `branch` / `raise`) that
  is not an object, `children` that is not an array, a `loop.type` outside `count` / `each` / `while` / `do_while`;
- **completeness is warned** — the node **is created** and the findings come back as warnings. The same findings are errors
  on the canvas / in `validate_flow`, because the step cannot run: `code` / `result` / `progress` without `code.script`,
  `http` without `http.url`, `llm` without `config.llm` or `llm.messages`, `loop` without `config.loop` / `loop.type` or with
  a non-numeric count, `branch` / `intent` with empty `children`, `call` without `target`,
  `mcp` without its connection, `rpc` without its endpoint or connection, a `raise` without `raise.code` / `raise.message`,
  a `lang` the sandbox does not list, a
  `response_mode` outside `complete` / `sse_stream`. Three findings stay warnings everywhere because the step still runs: a
  missing `config.outputs`, an `assign` with no `assignments`, a `branch` child with no condition.
  Measured: `config: {}` for `http` / `llm` / `code` / `mcp` / `rpc` still returns success — the findings are in `warnings`.

So `create_flow` returning success proves the shape and nothing else, and a returned warning is a TODO rather than advice. The
per-level table is in `references/nodes/README.md` ("What creation checks, per type").

**`validate_flow` covers the half that needs neighbours, without running anything.** It applies the same rule set to a saved
flow (or to a diagram passed inline) and returns findings with a severity, a `node_id`, the config path and a hint: nothing is
executed, no request is sent, no model is billed. Besides the per-node findings it checks the structure creation cannot see —
container `entry` children, cross-container edges, dangling `source_ref` / `call.target`, a `source_key` the
source node does not produce, `call.args` that do not match the target's `params`, branch children with no routing edge,
`loop each` sources missing from `inputs`, a `break` / `continue` no loop can reach, unreachable nodes — and it validates
`on_error` against each node's declared capabilities: an action the node does not declare is dropped by the runner, and a code
it cannot produce never matches, so both are config that looks present and does nothing.

The loop is **create → `validate_flow` → fix → `run_flow`**, and the run is worth starting only when `error_count` is 0. It
still cannot see what needs real data (an enabled connection, an existing model, a template rendered against real inputs, the
JSON-Schema arguments) — those stay with `run_flow`.

Go through this list first:

1. **The sub-object must exist, named after `type`**: `type: "http"` → `config.http`, and so on. Without it the node is empty.
2. **The required fields inside the sub-object must be present**: see the table below; none may be omitted.
3. **`outputs` must be declared as a set** (the front end writes the whole set into `config.outputs` when creating a node). The consequence of omitting depends on the node type:
   `mcp` / `rpc` have **no fallback** — a missing one reports `output var <name> not defined` at write time and the node fails;
   `http` / `llm` executors fill in their fixed outputs (it still runs); `code` drops any key the script returns that was not declared.
4. **`inputs` must be given as `{}` even when empty**; do not omit the key.
5. **`read_flow` right after creating**, checking node by node that `config` really landed and the required fields are there.
   Do not take `create_flow` returning `steps: 1` as success.

### Required fields and fixed outputs

The "fixed outputs" column comes from node configs the **front end actually generates** in this tenant (`read_flow` verifies it).

| type | Sub-object required | Sub-object optional | `config.outputs` should declare |
|---|---|---|---|
| `http` | `url` | `method`(GET) / `headers` / `params` / `body_type` / `body` / `timeout` / `response_mode` / `handler` | `body` `mode` `status` `headers` `sse_events` `stream_finished` |
| `llm` | `messages` (non-empty array) | `params` / `gateway_model` / `response_mode` / `handler` | `content` `reasoning_content` `tool_calls` |
| `code` | `script`, plus `outputs` for every key the script returns | `lang`(lua) / `timeout` | **custom**: every key the script returns must be declared |
| `mcp` | `connection_id`, `tool_name` | `arguments` / `progress` / `handler` | `result` `progress` `error` |
| `rpc` | by `target_type`: `endpoint` → `endpoint` (**the endpoint name**, not an id); `connection` → `connection_id` + `tool_name` | `target_type` / `arguments` / `progress` / `handler` | `result` `progress` `error` |

`on_error` is a **top-level sibling** of the behaviour block (`config.on_error`), never a key inside `http` / `llm` / `mcp` /
`rpc` / `code`; an `on_error` placed inside the sub-object is silently ignored.

Four easy traps:

- `headers` / `params` are `[{key,value}]` **arrays**, not objects
- `mcp.arguments` is stored by the front end as a **JSON string** (`"{\"a\":6,\"b\":7}"`), not an object
- for `code`, a variable not declared in `outputs` makes the script **fail outright** when returned (the runtime rejects it as an "undeclared variable"), it is not silently dropped
- for `code`, the return syntax is **per language**: `lua` returns a table from the top level, `python` assigns **`__return__`** (a `return` statement is a `SyntaxError`), `js` returns an object. Copying the Lua form into a Python script produces a dead node — see `references/script-languages.md`

### Node reference: read only when needed

Each node type's full fields, defaults, outgoing-edge rules and common errors live in its own file — **read on demand, not
all at once**:

```
read_file("/skills/flow-authoring/references/nodes/<type>.md")
```

| Read what first | File |
|---|---|
| Node index + `config` keys shared by all nodes (`inputs` / `outputs` / `locals` / `targets` / `on_error`) | `references/nodes/README.md` |
| Edge shapes / examples / return values / troubleshooting (handle rules are authoritative in `create_edge`'s parameters, not repeated here) | `references/edges.md` |
| Add / insert / update / delete nodes (`create_node` `insert_node` `update_node` `delete_node`), containers and entry rules | `references/nodes-edit.md` |
| Retry / skip / fallback script shapes and precedence on failure | `references/on-error.md` |
| Script conventions shared by every surface: how `lua` / `python` / `js` return a value, and what the `code` node, conditions, handlers, `on_error`, binding transforms and `result` / `progress` each expect back | `references/script-languages.md` |
| Execution nodes: `llm` `http` `code` `mcp` `rpc` | `references/nodes/<type>.md` |
| Control / containers: `branch` `intent` `loop` `process` `function` `call` `assign` `memory` `result` `progress` | `references/nodes/<type>.md` |
| Control signals (`break` / `continue` / `return` / `exit`), structural kinds (`start` / `entry` / `comment`) | `references/nodes/control-signals.md`, `references/nodes/visual.md` |

Suggested order: read the index in `README.md` (one screen), then only the file for the node you are configuring. Writing a
linear flow usually needs `README.md` plus one or two of the five execution nodes — do not read the whole directory at once,
that is just copying the body again.

## How to build it

```text
create_flow(
  name="daily-sales-report",
  description="Summarize yesterday's sales every day and produce a Chinese briefing",
  steps=[
    {"type": "http", "label": "fetch data",
     "config": {
       "http": {"url": "https://api.example.com/sales", "method": "GET",
                "headers": [], "params": [], "body": "", "body_type": "json",
                "timeout": {"enabled": true, "seconds": 30}, "response_mode": ""},
       "inputs": {},
       "outputs": {
         "body": {"type": "string", "description": "response body"},
         "mode": {"type": "string", "description": "response mode"},
         "status": {"type": "number", "description": "HTTP status code"},
         "headers": {"type": "string", "description": "response headers"},
         "sse_events": {"type": "array[string]", "description": "SSE event list"},
         "stream_finished": {"type": "boolean", "description": "whether streaming finished"}
       }
     }},
    {"type": "llm", "label": "write the briefing",
     "config": {
       "inputs": {"data": {"type": "string", "source_type": "node", "source_ref": "2", "source_key": "body"}},
       "llm": {"messages": [{"role": "user", "content": "Write 3 key points from this data: {{ .input.data }}"}]}
     }}
  ],
  external_protocols=["mcp"])
```

Returns `{flow_id, flow_name, steps, nodes, endpoints}`; `nodes` is the node list `[{id, type, label}]`, which you use to check
`source_ref`, branch directions and `call` targets.

To run something inside a container, add `children` to that step (only `loop` / `process` / `function`, and they can nest
further):

```text
{"type": "loop", "label": "retry three times",
 "config": {"loop": {"type": "count", "total_times": 3}},
 "children": [
   {"type": "http", "label": "fetch again", "config": {...}},
   {"type": "break", "label": "break when we have it",
    "config": {"condition": "return input.status == 200"}}
 ]}
```

The tool automatically creates an `entry` inside that container and connects the first `children` node to it — without `entry`
the container "succeeds but runs nothing", without an error.

**Node ids are generated by the tool**: `1` is the start node, then they increase in build order (parents first, then
sub-chains in the `branches` order they are declared). In a purely linear graph the first step is `2`, the second `3`… **branches
make ids stop matching step order**, so:

- `config.inputs`'s `source_ref` and `call`'s `config.target` may both be **written as the node label** (e.g. `"fetch data"`);
  the tool replaces it with the real id;
- when unsure, `read_flow` right after creating and read the diagram back; ids always follow the returned `nodes` / diagram.

Suggested order:

1. `list_flows` — confirm there is no duplicate name and it was not already built
2. `create_flow` — supply all `steps` at once; when the user mentions passing arguments, declare entry arguments with `arguments`; pass `external_protocols` **only when the user explicitly asks for exposure** — a flow created here has no released version, so this prepares those slots unexposed and returns `release_required`; nothing failed, the user releases, then you call `expose_flow_endpoint`
3. `read_flow` — read back to confirm the structure: a linear graph has "node count = step count + 1, edge count = step count"; with `branches` each sub-branch adds one edge carrying `source<childId>`, so check ids against `nodes`
4. `run_flow` — **run it once**, and only move on when you see `status: success` (see the next section)
5. to make it available to an agent: `grant_agent_tool(agent="<target>", ref="local:rpc:<flow name>")`

### Conditional branches: `branch` / `intent` use `branches`

A branch node **needs one outgoing edge per sub-branch** (the executor finds its way by the `source<sub-branch id>` prefix), so
it cannot just be another link in the main chain. On the tool side, give that step `branches`:

```json
{
  "type": "code", "label": "compute 5*9",
  "config": {"code": {"lang": "lua", "script": "return { result = 45 }"},
             "outputs": {"result": {"type": "number"}}}
},
{
  "type": "branch", "label": "is it 45?",
  "config": {
    "inputs": {"result": {"type": "number", "source_type": "node", "source_key": "result", "source_ref": "compute 5*9"}},
    "children": [
      {"id": "c1", "label": "equals 45",    "condition": "return input.result == 45",  "exclusive": true},
      {"id": "c2", "label": "not 45",       "condition": "return input.result ~= 45",  "exclusive": true}
    ]
  },
  "branches": [
    {"key": "c1", "steps": [{"type": "result", "label": "correct", "config": {"code": {"lang": "template", "script": "correct"}}}]},
    {"key": "c2", "steps": [{"type": "result", "label": "wrong",   "config": {"code": {"lang": "template", "script": "wrong"}}}]}
  ]
}
```

Rules:

- `branches[].key` must be a `config.children[].id`; use `key: "default"` for the **unconditional fallback** (may be omitted;
  without it there is no default branch, and when no condition matches the node simply ends having run nothing).
- Each sub-chain generates one edge: an ordinary branch `source<key>right`, the default branch `source<the branch node's own id>right`.
- **A step that declares `branches` must be the last step in its chain** — after a branch runs, only the default edge is taken;
  continuing the main chain after a branch has no meaning. If you need "continue after the branch", write the following steps
  into each sub-chain.
- A sub-branch present in `children` but missing from `branches` is skipped when matched, and the tool names it in `warnings`.
- Condition scripts must use `return` (`return input.result == 45`), not a bare expression.

### Connecting / rewiring: `create_edge` / `update_edge` / `delete_edge`

`branches` can only express "a branch node's sub-chains". For everything else — child nodes inside a container, and any "move
this edge somewhere else" — use these three tools:

```json
{"flow": "daily-sales-report", "source": "2", "target": "3",
 "source_handle": "source2right", "target_handle": "target3left"}
```

- **The tool parameter descriptions are authoritative**: handle shapes, usable positions per type, hard blocks and per-position
  suggestions are all in `create_edge` / `update_edge`'s descriptions; this document and `references/edges.md` do not enumerate
  them again, so the two can never disagree.
- When unsure about handles, **just call it**: a missing handle makes the error state the value to use based on the two nodes'
  relative positions; a poor combination returns `suggested_edge` (the edge is already stored — apply it with `update_edge` to
  avoid routing around).
- `update_edge` changing `source` must also pass `source_handle`, changing `target` must also pass `target_handle`
  (the handles embed node ids); `delete_edge` only needs `edge_id`.
- After changing, `read_flow` once more: the edge tools touch `edges` only — they create no nodes and change no `config`.

### Add / insert / update / delete nodes: `create_node` `insert_node` `update_node` `delete_node`

Supplying all `steps` to `create_flow` in one go is the least work; for a missed node, an insertion mid-chain, a config change or
a move into a container, use these four.

```json
// add a node (no parent_node = top level)
{"flow": "daily-sales-report", "type": "code", "label": "clean", "config": {"code": {"lang": "lua", "script": "..."}}}

// add into a container: pass the loop / process / function id as parent_node
{"flow": "daily-sales-report", "type": "http", "parent_node": "7"}

// insert a node in the middle of an existing edge (atomic: delete the old edge + connect A→new→B)
{"flow": "daily-sales-report", "edge_id": "04958", "type": "code", "label": "audit"}

// change one config entry only (top-level merge; a null value deletes the key)
{"flow": "daily-sales-report", "node_id": "3", "config_patch": {"on_error": {"action": "retry"}}}

// move into a container: edges crossing between the old and new container are deleted (cross-parent edges are invalid), returned in removed_edges
{"flow": "daily-sales-report", "node_id": "4", "parent_node": "7"}

// delete a node (and its edges; a container takes its whole subtree)
{"flow": "daily-sales-report", "node_id": "4"}
```

Key points:

- **A container must have an `entry`**: `create_node` / `create_flow` add it automatically; a hand-written diagram or one copied
  from elsewhere may lack it, and then the container "succeeds but runs nothing", without an error.
- **Node labels are unique across the graph**: `source_ref` / `call.target` can both be written as labels, and a
  duplicate points at the wrong node. An explicit duplicate is an error; leaving `label` empty makes the tool append a `-2`
  suffix (`entry` included — the second container would be called `entry-2`).
- **`config` is a full replacement, `config_patch` is a top-level merge** — pick one. The return value carries the final
  `config`; check it once.
- **`function` can only be created at the top level, and `return` only inside a `function`**; `insert_node` can only insert a node
  that ends up with both an incoming and an outgoing edge.
- **Deleting a node is blocked while it is referenced**: when `config.inputs.*.source_ref` or `config.target` (call) points at
  it, the delete is refused and the sources are listed; change the references with `update_node` first.
- Full rules in `references/nodes-edit.md`.

## Before the first run: three checks that save a rebuild

1. **Function nodes stay off the main chain** — no in-edge, no out-edge; only `call` enters them, and only `call` may name
   them from outside (a `source_ref` pointing at a function from outside its body is `function_ref`). Wired in, the body runs
   with empty params and dies on the first param read.
2. **`read_flow` first, then ids** — every node reference (`inputs` / `targets` / `args` `source_ref`, `call.target`) accepts a
   label, but ids are what the diagram stores, and the step index stops predicting them once the graph has branches or
   containers. Read the real ids back before wiring values between nodes.
3. **`validate_flow` is not the run** — a finding names a config key, not a proven failure. Fix what it points at, then
   confirm with one `run_flow`; do not restructure a working graph because of a single finding.

## Test run right after building (`run_flow`)

```json
{"flow": "daily-sales-report", "inputs": {"q": "yesterday"}}
```

Return shape:

```json
{
  "flow_name": "daily-sales-report",
  "status": "success",              // success / failed / cancelled
  "result": "final output text",    // what result nodes / return collected
  "duration_ms": 3210,
  "err_code": "timeout",            // only on failure: the error code of the earliest failing node
  "run_id": "0190f...",             // use read_flow_run for details later
  "nodes": [                        // in execution order, successful nodes included
    {"node_id": "2", "type": "http", "status": "success", "duration_ms": 1200},
    {"node_id": "3", "type": "llm", "status": "failed", "err_code": "timeout",
     "err_msg": "...", "err_stack": "(traceback, failed nodes only)", "logs": "(log excerpt)"}
  ]
}
```

Four facts you must know:

- **This is a real execution**: http / rpc / mcp really send requests, llm really bills, code / handler really run scripts.
  Side effects and cost are real — run it only to verify the flow works, not to probe arguments.
- **A single call is bound by the tool timeout (60 seconds by default)**: a long flow is cancelled at the timeout and returns
  `status: cancelled`. `nodes` then stops at the interruption point and nothing after it ran — do not read that as "the flow
  logic is broken".
- **Which version a sub-flow reads is decided by `follow_drafts`** (default `true`): `true` means the sub-flows it calls also
  read drafts (change A and B and see the effect together, without publishing B first); `false` means sub-flows run the
  **published version**, which verifies the production chain — a sub-flow that was never published errors outright instead of
  quietly falling back to the draft. The main flow itself always runs the current draft.
- **Node output variables are not returned** (they can be tens of KB): `result` is the final output; fetch intermediate data per
  node with `read_flow_run`. On failure, look first at the earliest `failed` node's `err_code` / `err_msg` / `err_stack` / `logs`.

## Debugging failures (`read_flow_run`)

`run_flow` only answers "did it work, where did it stop"; to ask "why", fetch the full record by `run_id`:

```json
{"run_id": "0190f...", "only_failed": true}
```

- by default only **failed nodes** are returned (`only_failed: false` returns the whole chain); `nodes: ["3","4"]` fetches specific steps.
- each node carries `input_vars` / `output_vars` / `local_vars` / `extra` (type-specific results such as HTTP status, token
  usage), plus `err_code` / `err_msg` / `err_stack` / `logs` / `retry_count` / `error_action` / `post_error` /
  `handler_error` (the last two are the `on_error` fallbacks' own failures: the node keeps its original code).
- when a variable is over the limit it degrades to `{"_truncated": true, "_bytes": 12345, "keys": [...]}` — meaning "the keys are
  all there, the values are too big", not data loss; storage itself truncates each top-level key at 64KB, and `truncated: true`
  on a node means it was truncated at storage time.
- `run_id` can also come from the front end's "Run records" list: **records produced by front-end debug runs and by calls from
  other agents are both inspectable**.

Troubleshooting order: `run_flow` gives the `run_id` and the earliest failing node → `read_flow_run` to inspect that node's
`input_vars` (did upstream pass the right values?) and `output_vars` (are the produced values right?) → change `config` against
the node reference file → `read_flow` to confirm the change landed → `run_flow` again. `inputs` keys must match the flow's
declared `arguments`; undeclared keys can still be passed, but they never appear in any `source_type=argument` variable.

## Endpoints: exposing and granting

> **Exposing an endpoint means making an interface callable from outside**; it is a side-effecting action. Unless the user
> explicitly asks, `create_flow` never passes `external_protocols`. "It can reach Baidu and output HTML" describes functionality,
> not permission to expose. To expose, ask first: which flow, mcp or rpc, the endpoint name, and who may call it. If something
> was exposed by mistake, **retract that endpoint** — deleting the flow is not the way to do it: it is a different
> action, it changes the flow id, and it now takes the version history and run records with it.

- **Exposing**: `external_protocols=["mcp"]` / `["rpc"]` / both prepares those slots; the switch itself waits for a release (`release_required`), because a flow created here has no released version; not exposing = no external entry point
- **The endpoint name = the flow name**: renaming the flow renames the endpoint, and the old `local:rpc:<old name>` grant **silently stops working**
- **Confirm exposure succeeded**: the protocol appears in `list_flows`'s `endpoints`; if it is absent, nothing is exposed yet (a prepared slot alone answers nobody)
- **Who can use it**:
  - an agent → `grant_agent_tool(agent, ref="local:rpc:<flow name>")` (the MCP-side tool name is `local__<flow name>`)
  - an external system → "service credentials" can only be granted **exposed** endpoints; flows that are not exposed are invisible there
- **Argument schema**: the parameter documentation an endpoint publishes comes from `flows.arguments` — `create_flow`'s
  `arguments` is exactly that, and `update_flow_arguments` maintains it afterwards. Without declarations the endpoint still
  accepts arbitrary arguments, but clients get no parameter documentation, so declare them before exposing for real; after
  changing, remember to **release** (the production entry point reads the released version).

## Maintenance

| What to change | How |
|---|---|
| Entry arguments | `update_flow_arguments` (`set` add/update / `remove` delete / `required` reset the required list) |
| Name / description / endpoints | there is no rename meta tool: `delete_flow` + `create_flow` to rebuild (the flow id changes; service-credential allowlists are granted against the target id, so old rows silently stop working) |
| Add nodes (container children / loops / sub-flows) | `create_node` (`parent_node` names the container); or `create_flow`'s `children` |
| Insert a node mid-chain | `insert_node(edge_id=…)`, atomic, no manual delete-and-reconnect |
| Change node config / move into a container | `update_node(node_id=…, config_patch=… / parent_node=…)` |
| Delete a node | `delete_node(node_id=…)`; a container takes its whole subtree |
| Connect / rewire / delete edges | `create_edge` / `update_edge` / `delete_edge` (edges only) |
| Status (normal / deprecated) | the console; once deprecated the definition is read-only and cannot be exposed |
| Delete | `delete_flow(flow=<id or name>)`; deletes the definition, its endpoints, its version history and its run records — unrelease a released version and retract an exposed endpoint first |
| Inspect the structure | `read_flow` for the diagram source; `list_flows` for an overview |

## Troubleshooting

| Symptom | Most likely cause |
|---|---|
| In `read_flow` a node has only `{"label"}` and no `config` | `config: {}` was passed — an empty object has no shape to refuse and nothing to warn about, so the call succeeds and the empty object is dropped as-is, leaving a dead node |
| Creation succeeds but `warnings` says some step is a no-op | `branch`/`intent` without `branches` (or a missing sub-branch) → add `branches`; `loop`/`process`/`function` with no children → add them with `children` or `create_node(parent_node=…)` |
| Run reports `body entry node not found` | the container is missing its `entry` child. `loop` / `process` / `function` all report this `config` error (it used to silently succeed and skip, so a sub-flow never ran and nobody noticed): `create_node` / `create_flow` add it automatically, a hand-written diagram does not |
| Error "label … must be unique" | a node with that name already exists (labels are how `source_ref` / `call.target` are written, so a duplicate points at the wrong node): pick another, or leave `label` empty so the tool appends a `-2` suffix |
| A branch matches but nothing happens | the `children` `id` and the `branches` `key` do not match (the tool blocks it), or `condition` forgot `return` |
| Run reports "branch / intent node has no branches configured" | `branches` **only builds outgoing edges**; the candidate branches / intents still have to be written in full under `config.children` |
| Run reports `output var xxx not defined` | the `mcp` / `rpc` node's `config.outputs` does not declare that output (these two have no fallback; `http` / `llm` fill it in) |
| A `code` node succeeds but every declared output is empty | the script never returned: `python` must assign `__return__` (a `return` statement is a `SyntaxError`), `lua` / `js` return a table / object from the top level, and `print` only reaches the node log |
| A config-related error on the very first step | the node's required sub-object is missing: `llm.messages` / `http.url` / `code.script` / `mcp.connection_id` + `tool_name` |
| `<no value>` in the result | a template field name is misspelled, or `config.inputs` does not declare that variable |
| Run reports `var path b not found in function params` | the function was entered by an edge instead of a `call`, or `call.args` did not pass that param — the body cannot default it (`input.b or 0` runs too late) |
| Error "node result not found" | `source_ref` points at a node that does not exist or has not run yet (ids start at `1`, so the first step is `2`) |
| `create_edge` reports "target_handle is required" | both handles must be supplied; the tool fills in no defaults. The error already suggests values based on the two nodes' positions — copy them |
| `create_edge` reports "only one incoming edge allowed" | that entry position already has an edge; use `update_edge` to change the source, or pick another entry position (the old edge is displaced) |
| `create_edge` reports "cross parent node" | the two nodes have different `parent_node` values — container children can only connect to nodes in the same container |
| Calling the endpoint says not found / not authorized | the endpoint is not exposed, or the ref uses the old flow name from before a rename |
| Changed the code but behaviour is unchanged | did you change an unsaved draft in the front end, or the stored copy? Trust what `read_flow` reads back |
| Error "the platform has not enabled this capability for the tenant" | code nodes need sandboxed script execution; ask the platform administrator to enable it — retrying does nothing |
