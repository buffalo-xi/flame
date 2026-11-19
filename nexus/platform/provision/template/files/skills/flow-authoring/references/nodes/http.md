# The `http` node

Sends one HTTP request, supporting JSON / form bodies and SSE streaming responses.

## config shape

```json
{
  "http": {
    "url": "https://api.example.com/search",
    "method": "POST",
    "headers": [{"key": "Authorization", "value": "Bearer {{ .input.token }}"}],
    "params": [{"key": "q", "value": "{{ .input.q }}"}],
    "body_type": "json",
    "body": "{\"q\": \"{{ .input.q }}\"}",
    "timeout": {"enabled": true, "seconds": 30},
    "response_mode": ""
  },
  "inputs": {"q": {"type": "string", "source_type": "argument", "source_key": "q"}},
  "outputs": {
    "body": {"type": "string", "description": "response body"},
    "mode": {"type": "string", "description": "response mode"},
    "status": {"type": "number", "description": "HTTP status code"},
    "headers": {"type": "string", "description": "response headers"},
    "sse_events": {"type": "array[string]", "description": "SSE event list"},
    "stream_finished": {"type": "boolean", "description": "whether streaming finished"}
  }
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `http.url` | ✅ | — | templates supported |
| `http.method` | — | `GET` | |
| `http.headers` | — | `[]` | a **`[{key,value}]` array**, not an object; values support templates |
| `http.params` | — | `[]` | appended to the query string; same array shape |
| `http.body` | — | empty | templates supported |
| `http.body_type` | — | `json` | `json` / `form`; only decides the default `Content-Type` |
| `http.response_mode` | — | empty = automatic | when empty, decided by the response `Content-Type: text/event-stream`; may also force `complete` / `sse_stream` |
| `http.timeout` | — | off | `{enabled, seconds}`; when off (or unset) there is no node-level timeout and the run limit applies |
| `http.handler` | — | — | post-processing script `{script, lang, timeout{enabled,seconds}}`; **`template` is not supported** |
| `config.inputs` | — | — | source of template values |
| `config.outputs` | recommended to write in full | filled in by the executor | the six fixed variables; it still runs if they are missing |

## Output variables

Fixed: `mode` / `status` / `headers` / `body` / `sse_events` / `stream_finished`.
The `handler` runs once in complete mode, and in SSE mode once per event plus once at the end; it may only write custom variables.

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Credentials: prefer global variables

The pattern is to add an input sourced from a global variable in `config.inputs`, then reference it in the header:

```json
"inputs":  { "token": {"type": "string", "source_type": "global", "source_key": "CRM_TOKEN"} },
"headers": [{"key": "Authorization", "value": "Bearer {{ .input.token }}"}]
```

The value never lands in the flow definition, an asset export carries only the reference name, and after installing into
another tenant the target tenant's value fills it in.
**Do not** write `{{ .global_vars.X }}`: that form was removed and the flow fails at load time.

Do not put tokens or passwords directly into `headers` / `params` / `body` — they are cleared by key name when listing (the
installer has to fill them in again), and carrying them across tenants is a leak.

## Common errors

- `url is required` — `url` is missing (or renders empty).
- Connection failure / timeout → `connection` / `timeout`; to handle them, list those two codes in `on_error.retry.codes`.
- Downstream cannot get `body` in an SSE scenario — when streaming, the body is in `sse_events` and `body` may be empty.

Source: `service/flow/executors/http.go`
