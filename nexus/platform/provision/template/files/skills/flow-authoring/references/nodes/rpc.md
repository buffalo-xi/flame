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

## Output variables

Fixed: `result`(string) / `progress`(array[string]) / `error`(string).

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Common errors

- `rpc config not found` / `endpoint` missing (or mistakenly written as `endpoint_id`).
- `RPC connection does not exist` / `connection is not enabled`.
- The endpoint call says "does not exist" — the endpoint name is wrong (the name = the flow name / agent name), or that flow
  has no rpc endpoint at all.

Source: `service/flow/executors/rpc.go`
