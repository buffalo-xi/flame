# The `mcp` node

Calls a tool on an MCP connection.

## config shape

```json
{
  "mcp": {
    "connection_id": "<MCP connection id>",
    "tool_name": "calculator",
    "arguments": "{\"a\": 6, \"b\": 7, \"op\": \"{{ .input.op }}\"}",
    "progress": false
  },
  "inputs": {"op": {"type": "string", "source_type": "argument", "source_key": "op"}},
  "outputs": {
    "result": {"type": "string", "description": "tool result"},
    "progress": {"type": "array[string]", "description": "progress notifications"},
    "error": {"type": "string", "description": "call error message"}
  }
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `mcp.connection_id` | ✅ | — | the connection must exist and be enabled, otherwise `not_found` / `config` |
| `mcp.tool_name` | ✅ | — | a tool name exposed by that connection |
| `mcp.arguments` | — | `{}` | **stored by the front end as a JSON string**; an object is accepted too. String values support templates |
| `mcp.progress` | — | `false` | when off, `progress` is always an empty array and the server does not push reasoning / text in full |
| `mcp.handler` | — | — | post-processing script `{script, lang, timeout{enabled,seconds}}`; **`template` is not supported** |
| `config.outputs` | recommended to write in full | no fallback | the three fixed variables; **a missing declaration reports `output var ... not defined` at write time** |

## Output variables

Fixed: `result`(string) / `progress`(array[string]) / `error`(string).
When the tool returns `isError=true`, `error` is written but the node still **succeeds**; only a call exception marks it failed.

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Common errors

- `mcp config not found` — there is no `mcp` sub-object in `config`.
- `mcp connection_id must not be empty` / `tool_name must not be empty`.
- `MCP connection does not exist` / `connection is not enabled`.
- `failed to decode arguments JSON` — the string form of `arguments` is not valid JSON (note it must still be valid after templates render).
- Creation does **not** check either `connection_id` or `tool_name`: an empty value is accepted silently, the node is created,
  and it only fails when the run reaches it. Both are required — write them even when `create_flow` returns no warning.

Source: `service/flow/executors/mcp.go`
