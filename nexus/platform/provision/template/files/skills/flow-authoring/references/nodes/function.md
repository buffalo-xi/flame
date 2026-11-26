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

It **does not read** `inputs`: parameters only arrive from a `call` node.

`required: false` relaxes the **call-site** check only. The body cannot make up for a parameter that was not passed:
`local b = input.b or 0` is too late, because `config.inputs` is resolved from the params **before** the script runs and a
missing key fails there (`var path b not found in function params`). An optional parameter means "the caller may pass it",
not "the body may skip it" — pass it every time.

## Never wire it into the main chain

A function node is entered by `call`, never by an edge: it has **no incoming edge and no outgoing edge**. Wire the main
chain into it and the executor runs the body right there with **empty parameters** — the first parameter read fails with
`var path a not found in function params` — and nothing after it in the chain runs. Route the main chain around the
function and let a `call` node enter it.

## Only a `call` may name it from outside

Everything outside the body reads the function's result through a `call` node: the call's `outputs` mirror the function's, so
a downstream step points at the **call**, not at the function. A `source_ref` (in `inputs` / `targets` / `args`) that names
a function node from outside its body is a `config` error (`function_ref`) — the value would be empty at run time. Inside the
body the function node is the way to read `params` and write `outputs`, so those references are expected.

## Writing the function's outputs

A step inside the body writes into the function's `outputs` through **`targets`**, not through its own `outputs`:

```json
{"targets": {"sum": {"type": "number", "source_type": "node", "source_ref": "<function node id>", "source_key": "sum"}}}
```

`source_key` names the function variable the value lands in; **omitted, the outer key is used** — so the example above
writes the function's `sum`. Write it explicitly to map a script key onto a differently named output. The name must exist in
the function's `outputs`, otherwise the run fails with `output var X not defined`.

- the same name must not also appear in that step's `outputs` (`the variable is declared in both outputs and targets`) —
  `outputs` / `targets` / `locals` are pairwise disjoint;
- `source_ref` may be written as the function node's label, but ids are safer: the step index tells you nothing once the
  graph has containers, so read the real ids back with `read_flow` (see `../nodes-edit.md`);
- a `code` step written this way needs no `config.outputs` — declaring the keys in `targets` is what makes it complete.

## Structural requirements

- it must have an `entry` child (`parentNode = function.id`, `label: start`).
- the body chain runs from that entry along the first outgoing edge to the end; each invocation gets a fresh child process.
- `return` is **not required**: the body simply ending returns normally with whatever was written to `outputs`. Use `return`
  only for an early exit.

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
