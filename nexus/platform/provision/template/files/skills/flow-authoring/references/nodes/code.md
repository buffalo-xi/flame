# The `code` node

Runs a script in the sandbox and assigns the returned values to declared output variables.

## config shape

```json
{
  "code": {
    "lang": "lua",
    "script": "return { total = input.a + input.b }",
    "timeout": {"enabled": true, "seconds": 10}
  },
  "inputs": {
    "a": {"type": "number", "source_type": "node", "source_ref": "2", "source_key": "total"},
    "b": {"type": "number", "source_type": "argument", "source_key": "b"}
  },
  "outputs": {"total": {"type": "number", "description": "sum"}}
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `code.script` | ✅ | — | the script source; it receives an `input` object and **returns a map** |
| `code.lang` | — | `lua` | prefer `lua`; take another language only when it is listed under "workflow script languages" in the system prompt **and** the task needs something Lua cannot do (a third-party library, `xlsx` / `zip` / `pdf` output). A language written from memory only reports `unsupported_lang` at runtime. When that list is unavailable, use `lua`; `template` is only for Result / Progress nodes, not for a Code node — see [`../script-languages.md`](../script-languages.md) |
| `code.timeout` | — | not enabled → the run limit applies | `{enabled, seconds}` |
| `config.inputs` | — | — | resolved into the script's `input` |
| `config.outputs` | ✅ | — | every key the script returns must be declared here; an undeclared key fails the node with `the script returned an undeclared variable: X` |
| `config.targets` | — | — | used to write results into an **ancestor container**'s outputs/locals or into **flow variables** (`source_type: node` / `flow`) |
| `config.locals` | — | — | node-local variables |

## Return value

The node reads a **return value**, not stdout, and the form depends on `code.lang`:

| `lang` | How the script returns |
|---|---|
| `lua` | a top-level `return` of a table |
| `python` | assign to **`__return__`** |
| `js` | an ordinary `return` of an object |

In every language the value must be an **object whose keys are declared variable names** (outputs / targets / locals).
`print` / `log` is not a return: it only reaches the node log, so a script that only prints succeeds with every output
empty.

The reasoning, the three-language examples and the same convention for every other script surface (conditions, handlers,
on_error, binding transforms) are in [`../script-languages.md`](../script-languages.md).

A missing `code.script` or `config.outputs` is **only a creation warning** — the node is still created, then fails at run time
(scripts are refused as undeclared without `outputs`). Fill both in even when the tool reports success.

## Output variables

There is no fixed output; everything comes from `config.outputs`. Returning an **undeclared** key fails outright
(`the script returned an undeclared variable: X`), it is not silently dropped; a declared variable that is not returned stays empty.

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Common errors

- `script not found` — the `code` sub-object exists but `script` is not a string.
- `the script returned an undeclared variable: X` — a returned key was not declared in `outputs` / `targets` / `locals`.
- `variable declared in both outputs and targets` — a variable name may appear in only one of the three.
- `run-context variables (env/argument/builtin sources) are read-only and cannot be assignment targets` — only ancestor container
  variables and flow variables are writable; writing a flow variable does not trigger output bindings above it (bindings apply
  only to `source_type: node`).
- `the platform has not enabled this capability for the tenant` — script execution is disabled platform-wide; retrying is
  useless, ask an administrator.
- A sandbox worker is unreachable → `connection` (retryable); a language the platform does not support → `config`.

Source: `service/flow/executors/code.go`, `script_exec.go`
