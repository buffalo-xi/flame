# The `loop` node

A container node: repeats its own child nodes (`parentNode = loop.id`), starting from the auto-generated `entry` child.

## config shape

```json
{
  "loop": {"type": "count", "total_times": 10},
  "loop_vars": {"index": {"type": "number", "description": "loop index"}},
  "inputs": {},
  "outputs": {},
  "locals": {}
}
```

## The four `loop` modes

| type | Keys | Meaning |
|---|---|---|
| `count` | `total_times` (number) | a fixed number of times; `<=0` falls back to the system limit `ResolveMaxLoopTimes()` |
| `each` | `source_key` (string), `break_on_empty` (bool) | `source_key` must be **a key of the same name in `config.inputs`**; its array is iterated item by item; an empty array with `break_on_empty=true` ends immediately; a `null` element aborts when the switch is true |
| `while` | `lang` (default lua), `condition` (script), `max_times` (number, `<=0` = system limit) | **check first, then run** |
| `do_while` | same as above | **run at least one round first**, then check |

`total_times` / `max_times` **must be numbers**. A wrong type (for example a string `"5"` after an asset-pack export) reports a
`config` error instead of silently falling back to the system limit for `<=0` — that silent path would blow "run 5 times" up to
"run to the limit".

`while` / `do_while` condition scripts support `loop.timeout = {enabled, seconds}`; when off (or unset) there is no node-level
timeout and the run limit applies.

`loop_vars` declares loop variables (`index` / `item` or custom ones), referenceable from child nodes.

`loop_vars` and `outputs` are two separate channels; do not mix them: loop variables (the index, the current element) go into
`loop_vars`, while ordinary values produced by the loop body go into `outputs` (child nodes write back via `targets`, with
`source_ref` pointing at the loop node). Writing to the wrong channel means the consumer gets nothing.

## The loop body and "after the loop"

- the loop body = the child chain, entered at the `loop`'s `entry` child (`label: entry`), running along the first outgoing edge
  to the end; each round gets a fresh child process. When no entry is found it reports a `config` error at runtime and does
  **not** silently no-op for N rounds.
- **after the loop** = the `loop` node's own **single outgoing edge** (there is no separate body / next handle); connecting a
  second one reports a `config` error.
- child nodes write values back into the `loop`'s `outputs` via `targets` (`source_ref: <loop id>`); `iterations` records the
  actual round count.

## Control signals inside a loop

| Node | Effect |
|---|---|
| `break` | ends this level of the loop; the loop node succeeds |
| `continue` | skips the rest of this round and starts the next |
| `return` / `exit` | the loop records success and the signal **propagates upwards** |
| any other error | the loop fails, carrying the round number |

## Common errors

- `loop config not found` / `unknown loop type`.
- `each data source does not exist in inputs` — `source_key` was not declared in `config.inputs`.
- `data source is not an array` — upstream supplied something other than an array.
- A `break` in a sub-chain errors — it is not inside any loop (most likely a child node was moved out of its container).

Source: `service/flow/executors/loop.go`
