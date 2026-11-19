# `on_error`: retry / skip / fallback script by error code

`config.on_error` has four optional blocks, and **matching is by error code** (`codes` is mandatory; without it nothing is enabled):

```json
{
  "on_error": {
    "retry":   {"codes": ["timeout", "connection"], "patterns": [], "max": 2, "delay_ms": 1000},
    "skip":    {"codes": ["not_found"], "patterns": []},
    "handler": {"codes": ["other"], "patterns": ["%rate limit%"], "lang": "lua",
                "script": "return 'skip'", "inputs": {}},
    "post":    {"set_outputs": {"text": "temporarily unavailable"},
                "handler": {"lang": "lua", "script": "return { text = 'fallback' }", "inputs": {}}}
  }
}
```

## Matching order

`retry` → `skip` → `handler` → if none matches, the node fails.

## retry

| Key | Required | Default | Meaning |
|---|---|---|---|
| `codes` | ✅ | — | list of error codes; **an empty array means no retry**. Available codes below |
| `patterns` | — | — | only effective when `codes` contains `other`: match the message, `%x%` contains, `x%` prefix, `%x` suffix, no `%` also means contains |
| `max` | — | `0` | number of retries (excluding the first attempt); `<=0` means no retry |
| `delay_ms` | — | `0` | **fixed** interval in milliseconds, no backoff |

## skip

| Key | Meaning |
|---|---|
| `codes` / `patterns` | on a match the node is recorded as `skipped` (continues as success), the error code stays inspectable; downstream receives **empty outputs** |

Use it for "even if the lookup fails, keep going"; add `post.set_outputs` when a default value is needed.

## handler

| Key | Meaning |
|---|---|
| `codes` / `patterns` | the script runs only on a match |
| `lang` | default `lua`; **`template` is not supported** |
| `script` | inputs: `error_code` / `error_message` / `error_node_id` / `error_node_type` / `retry_count` + `inputs` |
| `inputs` | extra variable declarations |

The script returns:

- the string `"skip"` (legacy form) or `{action: "skip"}` → treated as skipped;
- a map carrying `error_code` / `error_message` → the code and text are **rewritten** and the node still fails;
- anything else → the node still fails.

## post (fallback, only effective on certain nodes)

| Key | Meaning |
|---|---|
| `set_outputs` | static outputs, written straight into the node's output variables |
| `handler` | fallback script `{lang, script, inputs}`; the returned map overwrites the output variables |

- runs only on **final failure / skip** (not on the path where the `handler` script itself errors).
- **only `http` / `llm` / `code` / `mcp` / `rpc` / `call` support post**; on other nodes it is ignored even if written (the
  executor declares `HasPost=false` and the runner blanks `post`).
- the front-end `on_error` panel also offers it only for those node types plus `wait` (wait allows skip only).

## Three hard rules (code behaviour, not advice)

1. **`config` / `internal` / cancellation / control signals always bypass `on_error`**: retrying a configuration error is
   useless, fix the configuration; `break` / `continue` / `return` / `exit` are control flow, not errors.
2. **A node enters `on_error` only when its `nr.Status` is `failed`**: errors in a `branch` / `intent` sub-chain propagate
   unchanged per container semantics (that node's status is not failed), so they **do not trigger its own on_error**.
   `process` / `loop` / `function` are the opposite: a sub-chain error marks the container itself `failed`, so the container's
   on_error applies (even though the front end has no panel for it).
3. **An error code is only valid within the node's declared set**: an undeclared code is downgraded to `other`, so writing that
   code in `codes` no longer matches. On downgrade the message is suffixed with the original code (`… (original error code var)`);
   copy it into `codes`.
4. **Actions a node does not declare support for are simply ignored**: like `post`, `retry` / `skip` / `handler` must be within
   that node's capability declaration, or the configuration is dropped with a warning. For example `intent` declares only
   `retry`, so configuring `skip` on it does nothing; `branch` / `fork` / `assign` support no action, and `wait` supports `skip` only.

## What retries leave behind

Every attempt is a separate record (the first attempt plus each retry). `skip` / `handler` / `post` apply to **the last
attempt**: the node's final `status`, `error_code`, and the outputs written by `post.set_outputs`, all land in that last record.

- so with "all retries failed but skip is configured" the node is `skipped`, not `failed`;
- the `error_code` / `retry_count` seen by a handler script are also from the last attempt, not the first.

## Error codes

`timeout` / `busy` (sandbox concurrency quota exhausted, the script never ran) / `cancel` / `config` / `script` /
`capability` (not enabled on the platform) / `auth` / `not_found` / `connection` (network or sandbox worker unreachable,
**retryable**) / `var` / `internal` (platform-side failure, **bypasses on_error**) / `other`.

Source: `service/flow/executors/runner.go` (`parseOnErrorConfig` / `applyOnError` / `shouldBypassOnError`),
`service/flow/core/nodeResult.go` (error code constants).
