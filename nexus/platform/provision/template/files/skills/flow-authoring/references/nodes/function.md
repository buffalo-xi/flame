# The `function` node

An inline function container callable by `call` (**not** a sub-flow invocation; its children are still graph nodes of this flow).

## config shape

```json
{
  "params": {"a": {"type": "number", "description": "first addend"}},
  "outputs": {"sum": {"type": "number", "description": "sum"}},
  "locals": {}
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `params` | — | `{}` | parameter declarations `{type, required, description}`; `required` defaults to `true`, and the `call`'s `args` key set must match exactly |
| `outputs` | — | `{}` | return value declarations; `call` syncs the target function's `outputs` into its own outputs |
| `locals` | — | `{}` | local variables |

It **does not read** `inputs`. Entering a `function` node directly (not through a `call`) leaves the parameters empty.

## Structural requirements

- it must have an `entry` child (`parentNode = function.id`, `label: start`).
- the body chain runs from that entry along the first outgoing edge to the end; each invocation gets a fresh child process.

## Control signals (unlike `process`)

| Node | Effect |
|---|---|
| `return` | the function **returns normally** (the signal is swallowed) and `call` receives the outputs |
| `exit` | propagates all the way up, terminating the outer level |
| `break` / `continue` | a `config` error inside the function body (usable only inside a loop) |

## Outgoing edges

As an ordinary node it has a first outgoing edge too; but it is normally entered only through `call`, so the outside does not
need to connect it.

Note: the backend reads `on_error` for `function` as well, but **the front end has no on_error panel for it**, so it has to be
written elsewhere (e.g. through the API).

Source: `service/flow/executors/function.go`
