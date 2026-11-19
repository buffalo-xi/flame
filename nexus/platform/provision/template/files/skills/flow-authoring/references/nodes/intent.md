# The `intent` node

Lets the model **pick one** of several outgoing edges (single choice, no thresholds).

## config shape

```json
{
  "inputs": {
    "messages": {"type": "array[object]", "source_type": "argument", "source_key": "messages"},
    "input": {"type": "string", "source_type": "argument", "source_key": "input"}
  },
  "llm": {
    "gateway_model": "",
    "system_prompt": "You are an intent classifier. Decide the user's intent from the conversation history and the user input.\n\nHistory:\n{{json .input.messages}}\n\nUser input:\n{{.input.input}}",
    "params": {}
  },
  "children": [
    {"id": "a1b2c", "label": "check order", "description": "The user wants to check an order's status or shipment"},
    {"id": "d3e4f", "label": "refund", "description": "The user wants a refund or wants to complain"}
  ],
  "outputs": {
    "intent": {"type": "string"},
    "index": {"type": "number"}
  }
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `children` | ✅ | — | candidate intents; each `{id, label, description}` |
| `children[].description` | recommended | — | intent description, spliced into the classification prompt; when empty only the label is available |
| `llm.system_prompt` | recommended | — | the classification prompt; `{{ .input.* }}` reads `inputs` |
| `llm.gateway_model` | — | empty = tenant default model | |
| `llm.params` | — | `{}` | `max_tokens` / `temperature` / `top_p` / `extra_params` |
| `inputs` | recommended | — | used by the prompt; `input.messages` / `input.input` are the front end's two defaults |
| `outputs` | — | filled in automatically | always `intent` / `index` |

**`llm.messages` is not read**: `intent` assembles messages itself and only honours `system_prompt`.
**No threshold or confidence setting**: if the model cannot decide it returns -1, which takes the default branch.

## Output variables

Fixed: `intent`(string) = the matched branch's `label`, `""` when nothing matched; `index`(number) = the matched index, `-1`
when nothing matched.

## How outgoing edges are wired

Same as `branch`: a matched branch uses `source<childId>left|right`, the default branch
`source<nodeId>left|right|bottom`. No match and no default outgoing edge → the node succeeds and does nothing.

## Building with `create_flow` (`branches`)

Exactly the same form as `branch`: `key` = `children[].id` (`default` is the optional fallback), each sub-chain generates one
`source<childId>right` outgoing edge; `key: "default"` uses `source<the node's own id>right`.

**`branches` only builds edges and never fills in `children`** — the candidate intents still have to be written in
`config.children` (the executor reads `config["children"]` only; without it, it reports "the intent node has no branches
configured"). The tool validates `branches[].key` against `config.children[].id` and rejects creation on a mismatch.

```json
{
  "type": "intent", "label": "detect the user's intent",
  "config": {
    "llm": {"system_prompt": "..."},
    "inputs": {"input": {"type": "string", "source_type": "argument", "source_key": "input"}},
    "children": [
      {"id": "a1", "label": "check order", "description": "Check an order's status or shipment"},
      {"id": "b2", "label": "refund",      "description": "Request a refund or complain"}
    ]
  },
  "branches": [
    {"key": "a1", "steps": [{"type": "code", "label": "lookup", "config": {"code": {"lang": "lua", "script": "..."}}}]},
    {"key": "b2", "steps": [{"type": "code", "label": "refund",  "config": {"code": {"lang": "lua", "script": "..."}}}]},
    {"key": "default", "steps": [{"type": "result", "label": "not understood", "config": {"code": {"lang": "template", "script": "What would you like to do?"}}}]}
  ]
}
```

- **It must be the last step in its chain**: after a branch node runs, only the default edge is taken, so subsequent steps must
  be written into each sub-chain.
- Candidates present in `children` but missing from `branches` only produce `warnings` (skipped when matched); creation is not
  blocked.

## Prompts and global variables

`llm.system_prompt` follows the same rules as the `llm` node: global variables enter through `config.inputs` with
`{"source_type": "global", "source_key": "NAME"}` and are written as `{{ .input.xxx }}` in the prompt;
**there is no** `{{ .global_vars.X }}` (removed — the flow fails at load time).

Referencing a `secret` global variable is not blocked but is logged as a warning — the value enters the model context and run
records.

## Common errors

- `the intent node has no branches configured` — `config.children` is empty (`branches` does not fill it in).
- Always taking the default branch — `children[].description` is too vague, or `system_prompt` gives no numbering or selection rule.
- Failing to parse the classification — the model did not answer in JSON; the executor falls back to finding `{"index":n}`,
  a bare number, or the first `{...}` in the text.

Source: `service/flow/executors/intent.go`
