# raise

Fails the node on purpose with an error code you pick. Nothing is sent anywhere and no sandbox is started: the node ends
the chain as failed, carrying a code an enclosing host or a `call` node can classify.

**Terminal**: it takes an incoming edge and no outgoing one. A raise never succeeds, so nothing after it can ever run -
its successor would be dead by construction, and `validate_flow` reports `no_outgoing` on a diagram that wires one anyway.

## config

| Key | Required | Meaning |
|---|---|---|
| `raise.code` | yes | One of the raisable codes below |
| `raise.message` | yes | The failure text; a template over the inputs declared above (this node has no local and no output variables) |
| `inputs` | no | Variables the message reads; declare them here before referencing them |

```json
{
  "type": "raise",
  "data": {
    "label": "Order missing",
    "config": {
      "inputs": {
        "order_id": { "type": "string", "source_type": "node", "source_ref": "5", "source_key": "order_id" }
      },
      "raise": { "code": "not_found", "message": "order {{ .input.order_id }} not found" }
    }
  }
}
```

## Which codes you may use

`timeout`, `busy`, `script`, `capability`, `var`, `connection`, `auth`, `not_found`, `other`.

Three are refused, each for its own reason:

- `config` / `internal`: the runner returns them straight away without consulting `on_error`, so a rule written against
  them would never fire;
- `cancel`: it means the user stopped the run, and the stored status comes from the run context, so a raised `cancel`
  would record a failure whose code claims a cancellation that never happened.

## No error handling on this node

`raise` declares no `on_error` action and no `post`, the same as `branch` / `assign`. Its failure is authored, so "may
this be tolerated here" was answered before the node was drawn; allowing `skip` would turn the node into an `assign` that
writes nothing, and a `handler` would only rewrite a message that is already written next to it.

The catch point is the caller:

- inside a `process` / `loop` body: the host fails, and its `skip` / `handler` / `post` decide. That is also the way to
  record a soft failure - `process.on_error.skip` plus `post.set_outputs` writing a marker such as `ok = false`;
- inside a `function` invoked by `call`: the `call` node's own `on_error` decides, which is the ordinary "the callee
  throws, the caller decides" shape;
- on the top-level chain: nothing catches it, and the run fails with your code. That is what a top-level raise means.

A conditional failure - "fail when this is true" - is a `branch` in front of the raise, not an `on_error`.

## When to use it

- Replace an `if` that would otherwise fall through silently: raise `not_found` and let the caller decide.
- Give a `code` node a classified failure without spending a sandbox round-trip.
- Do **not** use it to signal "the platform is broken": that is `config` / `internal`, and both are refused here.
