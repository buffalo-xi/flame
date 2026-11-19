# The `memory` node

Reads the current session's memory messages, to be used as history in a later prompt.

## config shape

```json
{
  "outputs": {"messages": {"type": "array[object]"}},
  "limit": 10,
  "window": {"enabled": false, "unit": "hour", "value": 24}
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `outputs` | — | filled in automatically | fixed output `messages`(array[object]) |
| `limit` | — | unset → `ResolveMaxSessionRecords()` | take the most recent N; `<=0` counts as unset |
| `window` | — | `{enabled:false, unit:"hour", value:24}` | time window; effective only when `enabled=true` and `value>0`; `unit`: `second` / `minute` / `hour` / `day` |

It **does not read** `inputs` / `locals`. With no `session_id`, or an invalid `limit`, it outputs **an empty array** and the node
succeeds.

## Output variables

`messages`: `[{role, content, timestamp}]`, oldest → newest; one conversation record is split into a user and an assistant entry.

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** when done.

## Common errors

- Getting an empty array without an error — usually there is no session context (a stateless call), or `limit` / `window` is 0.

Source: `service/flow/executors/memory.go`
