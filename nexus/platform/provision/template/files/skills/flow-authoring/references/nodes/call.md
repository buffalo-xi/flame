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
- `missing required arg "x" for function <id>` — the target function's required parameter (`required` defaults to `true`) has no matching argument.
- `unknown arg "x" for function <id>` — `args` contains a key not declared in the target function's `params`.
- `failed to resolve args` — an argument's `source_key` has no matching upstream output.
- Recursion limit exceeded — the function calls itself directly or indirectly with no terminating condition.

Source: `service/flow/executors/call.go`
