# The `progress` node

Emits one "progress" update outwards (reasoning / intermediate state); it does not enter the final result.

## config shape

```json
{
  "category": "text",
  "code": {
    "lang": "template",
    "script": "Processing item {{ .input.index }}…",
    "timeout": {"enabled": false, "seconds": 10}
  },
  "inputs": {"index": {"type": "number", "source_type": "node", "source_ref": "2", "source_key": "index"}}
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `code` | ✅ | — | `{lang, script, timeout}`; `lang` is usually `template` |
| `code.script` | ✅ | — | the progress text; empty / missing → `script` error |
| `category` | — | `text` | node-level setting; `text` (progress) or `thinking` (reasoning), deciding which area the front end shows it in |
| `inputs` | — | — | template source |

**There is no `outputs` panel**: `output` is written automatically by the executor.

Creation does **not** check `config.code` here (there is no `progress` case in the creation checks): a node without it is
created happily and only fails when the run reaches it. Write the block even when `create_flow` returns no warning.

## Output variables

`output`(string): this progress text.

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Difference from `result`

| | `progress` | `result` |
|---|---|---|
| Destination | the progress channel (many allowed) | the result channel (the final answer) |
| Section | `category: text` / `thinking` | none |
| Streaming | sent one by one | controlled by `stream` |

Source: `service/flow/executors/progress.go`
