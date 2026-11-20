# The `llm` node

Calls the model once for a text result (streaming supported). Three fixed output variables; scripts may only write custom
variables.

## config shape

```json
{
  "llm": {
    "gateway_model": "",
    "messages": [
      {"role": "system", "content": "You are a data analyst"},
      {"role": "user", "content": "Write 3 key points from this data: {{ .input.data }}"}
    ],
    "params": {"temperature": 0.3, "max_tokens": 800},
    "response_mode": "complete"
  },
  "inputs": {"data": {"type": "string", "source_type": "node", "source_ref": "2", "source_key": "body"}},
  "outputs": {
    "content": {"type": "string", "description": "reply text"},
    "reasoning_content": {"type": "string", "description": "reasoning trace"},
    "tool_calls": {"type": "array[object]", "description": "tool call list"}
  }
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `llm.messages` | ✅ | — | `[{role, content}]`; `role` defaults to `user`; `content` supports templates; an empty array → `config` error |
| `llm.gateway_model` | — | empty = tenant default model | set but not found → `not_found`; a tenant with no default model also gives `not_found` |
| `llm.response_mode` | — | `complete` | accepts `complete` / `sse_stream` only; anything else is a `config` error |
| `llm.params` | — | `{}` | `max_tokens` / `temperature` / `top_p` / `extra_params` |
| `llm.handler` | — | — | post-processing script `{script, lang, timeout{enabled,seconds}}`; **`template` is not supported** |
| `config.inputs` | — | — | source of the `{{ .input.x }}` templates |
| `config.outputs` | recommended to write in full | filled in by the executor | the three fixed variables; it runs without them (`ensureFixedOutputs` adds them), and the front end writes them all |
| `config.locals` | — | — | local variables, used in the handler as `{{ .local.x }}` |

## Output variables

Fixed: `content`(string) / `reasoning_content`(string, only reasoning models produce it) / `tool_calls`(array[object]).
The `handler` may only write custom outputs; writing these three is blocked.

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Prompts and global variables

Global variables enter the prompt through input variables: declare `{"source_type": "global", "source_key": "NAME"}` in
`config.inputs` and write `{{ .input.xxx }}` in `messages[].content`. **There is no** `{{ .global_vars.X }}` form (it was
removed; leaving it in the config makes the flow fail at load time instead of rendering `<no value>`).

The run's output language arrives the same way: declare `{"source_type": "builtin", "source_key": "language"}` and use
`{{ .input.xxx }}`. It is a BCP-47 tag such as `zh-CN`, and an **empty string** when no caller specified one. The node does
**not** append an "answer in X" rule by itself — whether the prompt says so is the author's decision, and a prompt that
should follow the language has to spell it out.

Referencing a `secret` global variable is **not blocked**, but a warning is logged: the value enters the model context, and
also `session_records` and SSE. Prefer using secrets in connection / tool configuration (`http.headers`, `mcp` / `rpc`
connection parameters) rather than in prompts.

## Common errors

- `messages must not be empty` — `messages` is an empty array or is not an array.
- `unsupported response_mode` — something like `auto` was written.
- `no model specified and no default model` — `gateway_model` is empty and the tenant has no default model configured.

Source: `service/flow/executors/llm.go`
