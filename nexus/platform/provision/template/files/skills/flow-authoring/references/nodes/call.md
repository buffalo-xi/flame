# The `call` node

Calls another `function` node **within the same flow** (recursion supported, limited by recursion depth).

## config shape

```json
{
  "target": "f_9x2k",
  "args": {
    "a": {"type": "number", "source_type": "node", "source_ref": "2", "source_key": "total"},
    "b": {"type": "number", "source_type": "argument", "source_key": "b"}
  },
  "outputs": {"sum": {"type": "number"}}
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `target` | ✅ | — | the target node's **id**, whose `type` must be `function`; otherwise a `config` error |
| `args` | — | `{}` | argument declarations; the keys must correspond one-to-one with the target function's `params` (a missing required one or an extra key → `config` error); resolved in the **parent flow** context and then injected |
| `outputs` | — | — | completely **overwritten by the target function's `outputs`** (the front end syncs this when editing the `call` panel) |

It **does not read** `inputs` / `locals`. Exceeding the recursion depth → `internal` error.

Depth is a **tenant-level** number (`flow_configs.max_recursion_depth`, 20 by default, the platform's `max_recursion_depth`
row is the hard ceiling), shared with the cross-flow `rpc` call, and it counts **nesting**, not calls: thirty sibling `call`
nodes all sit at depth 1. A recursive function is the reason the number is as high as it is — a tree walk is routinely 30–50
levels, and the tenant's `max_exec_count` / `max_timeout` are what stop a runaway one. When a run reports
`exceeded max recursion depth`, the function is recursing deeper than the tenant allows: add a terminating condition, or ask
an operator to raise the ceiling — rewriting the graph does not help.

`args.*.source_ref` may be written as a node **label** or as its id (a label is rewritten when it matches no id), the same
rule as `config.inputs`. Ids are still safer once the graph grows containers: read them back with `read_flow`.

## Output variables

Mirror the target function's `OutputVars` after it runs (names and types come from the function), and mirror its status as well.

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** after the call returns.

## Difference from `process`

| | `process` | `call` |
|---|---|---|
| Whose nodes it runs | its own children | another `function` node's children in this flow |
| Reuse | not reusable | callable from several places / recursive |
| Arguments | none | `args` → the function's `params` |

## Common errors

- `call target must not be empty` / `target node is not a function`.
- `missing required arg "x" for function <id>` — the target function's required parameter (`required` defaults to `true`) has no matching argument. `validate_flow` / the canvas badge reports it as `call_args` before the run.
- `unknown arg "x" for function <id>` — `args` contains a key not declared in the target function's `params`; also reported as `call_args`.
- `var path b not found in function params` — the parameter was never passed: either the function was entered by an edge
  instead of a `call`, or `args` omits it. Declaring it `required: false` does not help; the body resolves params before
  any script runs, so an in-body default is too late.
- `failed to resolve args` — an argument's `source_key` has no matching upstream output.
- Recursion limit exceeded — the function calls itself directly or indirectly with no terminating condition.

Source: `service/flow/executors/call.go`
