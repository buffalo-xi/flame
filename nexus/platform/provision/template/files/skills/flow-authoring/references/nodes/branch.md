# The `branch` node

Routes the flow to different outgoing edges based on conditions. Every sub-branch needs **its own outgoing edge** (same for
`intent`) — when creating with tools this is expressed through `branches` (see "Building with `create_flow`" below); on the
canvas it is wired by hand.

## config shape

```json
{
  "branch": {"lang": "lua", "timeout": {"enabled": false, "seconds": 30}},
  "inputs": {
    "score": {"type": "number", "source_type": "node", "source_ref": "2", "source_key": "total"}
  },
  "children": [
    {"id": "a1b2c", "label": "high score", "condition": "return input.score >= 90", "exclusive": true},
    {"id": "d3e4f", "label": "medium score", "condition": "return input.score >= 60", "exclusive": false}
  ]
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `children` | ✅ | — | the branch array; an empty array → `config` error |
| `children[].id` | ✅ | — | corresponds to the outgoing edge's `source_handle` (`source<id>left` / `source<id>right`); a branch with an empty id is ignored |
| `children[].label` | — | `id` | used in logs only |
| `children[].condition` | — | empty = skip that branch | script text that must `return` a value; truthiness: a bool itself, a string only if `"true"/"True"/"TRUE"`, a number if non-zero |
| `children[].exclusive` | — | `true` | **exclusive by default**: on a match, the node ends immediately after that sub-chain, without trying later branches or the default branch. Only an explicit `false` makes it non-exclusive (keep comparing; every match runs) |
| `branch.lang` | — | `lua` | condition script language: `return` in lua / js, `__return__ = ...` in python (see [`../script-languages.md`](../script-languages.md)) |
| `branch.timeout` | — | off | `{enabled,seconds}`; when off there is no node-level timeout and the run limit applies |
| `inputs` | — | — | variables shared by all branches, available as `input.*` in condition scripts |

It **does not read** `outputs` / `locals`: a `branch` node has no output variables.

## How outgoing edges are wired

| Branch | `source_handle` |
|---|---|
| an ordinary branch `children[i]` | `source<childId>left` or `source<childId>right` (prefix match, first edge per key) |
| the default branch | `source<nodeId>left` / `right` / `bottom` (key = **the branch node's own id**) |

When nothing matches and there is no default outgoing edge, the node succeeds and **no sub-chain runs** (no error).
Each branch's sub-chain runs in its own child process with variables isolated from the main chain, running to the end from the
target node; an error in a sub-chain propagates upwards (it does not trigger the branch's own `on_error`).
After a branch matches, the branch node does **not** continue along its own first outgoing edge (its edges are all consumed by
branch semantics).

## Building with `create_flow` (`branches`)

The tools do not let you hand-write a diagram, but "every sub-branch must have an outgoing edge" still has to be expressed, so
the branch step needs `branches` (`key` = `children[].id`, `default` is the optional fallback):

```json
{
  "type": "branch", "label": "is it 45?",
  "config": {
    "inputs": {"result": {"type": "number", "source_type": "node", "source_ref": "compute 5*9", "source_key": "result"}},
    "children": [
      {"id": "c1", "label": "equals 45", "condition": "return input.result == 45", "exclusive": true},
      {"id": "c2", "label": "not 45",    "condition": "return input.result ~= 45", "exclusive": true}
    ]
  },
  "branches": [
    {"key": "c1", "steps": [{"type": "result", "label": "correct", "config": {"code": {"lang": "template", "script": "correct"}}}]},
    {"key": "c2", "steps": [{"type": "result", "label": "wrong",   "config": {"code": {"lang": "template", "script": "wrong"}}}]}
  ]
}
```

Result: the branch node draws a `sourcec1right` edge to the head of the c1 chain and a `sourcec2right` edge to the head of the
c2 chain; a `key: "default"` chain uses `source<the branch node's own id>right`.

- **It must be the last step in its chain**: after a branch runs, only the default edge is taken, so a main chain after the
  branch has no meaning.
- Sub-chains may nest `branch` again (ids are assigned consecutively in build order, no mental arithmetic needed).
- Sub-branches present in `children` but missing from `branches` only produce `warnings` (skipped when matched); creation is
  not blocked.

## The default branch is a "closing chain", not an else

Evaluated in `children` order:

| Situation | Result |
|---|---|
| a condition is true and `exclusive=true` (the default) | run that branch's sub-chain → **end the node**; later branches and the default branch do not run |
| a condition is true and `exclusive=false` | run that branch's sub-chain → keep comparing the next one |
| condition false / empty condition | skip, continue with the next |
| all compared, none matched | run the default branch (with no default outgoing edge the node just ends successfully) |
| all compared, some matched but all `exclusive=false` | **the default branch still runs** (it is a closing chain, not an else) |

So a "common tail that runs in every case" is reachable only through non-exclusive branches; an exclusive branch ends the node
once it matches, so if a tail is needed, chain it at the end of that branch's own sub-chain.

## Common errors

- `branch node has no branches configured` — `children` is empty.
- `branch X condition failed to execute` — the condition script errored (**a straight failure**, not a skip).
- A branch is never taken — `condition` was written as `input.score > 90` (no `return`), or `score` is not declared in `inputs`.

Source: `service/flow/executors/branch.go`
