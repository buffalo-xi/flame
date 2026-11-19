# The `process` node

An inline sub-flow container: groups a set of nodes into one box, runs them in order, and returns to the main chain.

## config shape

```json
{
  "outputs": {"summary": {"type": "string", "description": "sub-flow output"}},
  "locals": {}
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `outputs` | — | `{}` | variables the sub-flow exposes; written by **inner nodes** via `targets` (`source_ref: <process id>`) |
| `locals` | — | `{}` | container-local variables |
| `concurrency` | — | absent (serial) | **a container never carries this key: the tools ignore any `concurrency` here and return a warning**; concurrency (off by default, enabled only when the user explicitly asks) is configured on the **member nodes** of the group (`{"mode":"mono"}` or `{"mode":"fork","fork_node_id":"<id>"}`, see `README.md`) |

It **does not read** `inputs`, and there is **no** reference key for "call another flow" — the children are simply the nodes
with `parentNode = process.id` in the same diagram.

## Structural requirements

- the container must have an `entry` child (`type: "entry"`, `parentNode: <process id>`), generated automatically when the
  front end creates a container.
- no entry → a `config` error at runtime (`process node X: body entry node not found`).
  It used to be a silent skip, so the whole sub-flow never ran and nobody could tell.
- the body chain runs from the entry along the first outgoing edge to the end; each execution gets a fresh child process.
- the container itself may have only one outgoing edge; a second one reports a `config` error (not "only the first is followed").

## Control signals

`break` / `continue` / `return` / `exit` all finish successfully and **propagate upwards** (they are not swallowed inside the
container).

## Outgoing edges

After the container finishes, execution continues along the `process` node's own **first outgoing edge**.

Source: `service/flow/executors/process.go`
