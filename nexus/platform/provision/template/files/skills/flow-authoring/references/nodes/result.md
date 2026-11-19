# The `result` node

Emits one "result" outwards (conversation text / the final answer). Whatever the script renders is what is sent.

## config shape

```json
{
  "code": {
    "lang": "template",
    "script": "{{ .input.summary }}",
    "timeout": {"enabled": false, "seconds": 10}
  },
  "inputs": {"summary": {"type": "string", "source_type": "node", "source_ref": "3", "source_key": "content"}},
  "stream": false
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `code` | ✅ | — | reuses the script node structure `{lang, script, timeout}`; **`lang` is usually `template`** (pure rendering, outside the sandbox) |
| `code.script` | ✅ | — | the result text; empty / missing → `script` error |
| `inputs` | — | — | template source |
| `stream` | — | `false` | **a bool, not an object**; `true` = send each one as a streaming result, `false` = accumulate only and deliver once when the run ends |

**Do not write `config.outputs`**: the front end strips it on save (the node's `output` is written automatically by the executor).

## Output variables

`output`(string): this result's text (not declared outwards; use `assign` to capture it if needed).

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done (usually to the next `result` or straight to the end).

## Common errors

- `result code not found` — the `code` sub-object is missing.
- `<no value>` in the result — `inputs` does not declare it, or the template name is misspelled.

Source: `service/flow/executors/result.go`
