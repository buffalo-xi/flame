# The `assign` node

Pure assignment: writes template-rendered values into this node's output variables, or into an **ancestor container**'s
variables / **flow variables**.

## config shape

```json
{
  "inputs": {"raw": {"type": "string", "source_type": "node", "source_ref": "2", "source_key": "body"}},
  "outputs": {"total": {"type": "number", "description": "parsed number"}},
  "targets": {"container_var": {"type": "string", "source_type": "node", "source_ref": "<container id>"}},
  "assignments": [
    {"kind": "output", "target": "total", "path": "", "value": "{{ .input.raw }}"}
  ]
}
```

## Fields

| Key | Required | Meaning |
|---|---|---|
| `assignments` | ✅ | an array, each item `{kind, target, path, value}` |
| `assignments[].kind` | ✅ | either `output` (write this node's output) or `target` (write an ancestor container variable or a flow variable) |
| `assignments[].target` | ✅ | the variable name; for `kind: target` it must be declared in `targets` |
| `assignments[].path` | — | sub-path; when `path` starts with `[` or the target is an array, it becomes `target+path`, otherwise `target+"."+path` |
| `assignments[].value` | — | a Go template string; converted to number / boolean / string per the target variable's `type` |
| `inputs` | — | template context (`{{ .input.x }}`) |
| `outputs` | required when writing `kind: output` | target variable declarations; the type decides the conversion |
| `targets` | required when writing `kind: target` | writable target declarations: ancestor container outputs/locals (`source_type: node`, `source_ref: <container id>`) or flow variables (`source_type: flow`) |

Those are the only two writable target kinds; `env` / `argument` / `builtin` are this run's read-only inputs, and writing them
is an error.
Writing `source_type: flow` does not trigger output bindings on flow variables (bindings apply only to `source_type: node`).

It **does not read** `locals`.

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Common errors

- `assignments` missing / empty → the node does nothing (no error), easily mistaken for a successful assignment. Creation
  and `validate_flow` / the canvas both flag it as a warning (`no_op`), but the node still runs and reports success, so the
  empty list only shows up as "the value never arrived" further down.
- `unknown assign kind` — `kind` written as something like `variable`. The list is all-or-nothing: one malformed entry
  fails before any assignment lands, and the pre-save check reports it before the run.
- Writing into a `target` that is not declared in `targets` → `config` error.
- Assigning `"abc"` to a number — the conversion fails and reports `var` (listing `var` in `on_error.codes` lets you skip or retry).

Source: `service/flow/executors/assign.go`
