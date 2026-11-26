# The `rpc` node

Two uses: call a tool on an RPC connection, or **call an endpoint this tenant has exposed, in-process**.

## config shape

```json
{
  "rpc": {
    "target_type": "endpoint",
    "endpoint": "<endpoint name>",
    "arguments": {"x": "{{ .input.x }}"}
  },
  "inputs": {"x": {"type": "string", "source_type": "argument", "source_key": "x"}},
  "outputs": {
    "result": {"type": "string", "description": "call result"},
    "progress": {"type": "array[string]", "description": "progress notifications"},
    "error": {"type": "string", "description": "call error message"}
  }
}
```

The external-connection form:

```json
{"rpc": {"target_type": "connection", "connection_id": "<RPC connection id>",
         "tool_name": "foo", "arguments": {"x": 1}}}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `rpc.target_type` | — | `connection` | `connection` = an external RPC connection; `endpoint` = an endpoint this tenant has exposed (called directly in-process) |
| `rpc.connection_id` + `rpc.tool_name` | required in `connection` mode | — | the connection must be enabled |
| `rpc.endpoint` | required in `endpoint` mode | — | **the endpoint name** (= the flow name / agent name, unique per tenant, follows renames). Use the name, not the id: ids change after export and import. This is an in-tenant call, so the endpoint does not have to be exposed |
| `rpc.arguments` | — | `{}` | an object or a JSON string; values support templates |
| `rpc.progress` | — | `false` | when off, `progress` is always an empty array |
| `rpc.handler` | — | — | post-processing script; **`template` is not supported** |
| `config.outputs` | recommended to write in full | no fallback | a missing declaration reports `output var ... not defined` at write time |

A flow-type endpoint call shares the recursion depth with the intra-flow `call` node: one tenant-level number
(`flow_configs.max_recursion_depth`, 20 by default), counting **nesting**, so `A → B → C` is depth 3 and several rpc nodes
sitting side by side in one flow all stay at the same level. Going straight back to an entity already in the chain is rejected
earlier as `circular call detected`.

## Output variables

Fixed: `result`(string) / `progress`(array[string]) / `error`(string).

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Common errors

- `rpc config not found` / `endpoint` missing (or mistakenly written as `endpoint_id`).
- `RPC connection does not exist` / `connection is not enabled`.
- The endpoint call says "does not exist" — the endpoint name is wrong (the name = the flow name / agent name), or that flow
  has no rpc endpoint at all.
- The mode-specific fields (`endpoint` in `endpoint` mode, `connection_id` + `tool_name` in `connection` mode) are checked
  like any other required key: a returned warning at creation, an error from `validate_flow` / the canvas. `target_type`
  itself defaults to `connection` when absent, so an endpoint call without `target_type` silently becomes a connection call —
  and it is then the missing `connection_id` that gets reported.

Source: `service/flow/executors/rpc.go`
