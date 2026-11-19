# The `wait` node

A concurrency join point: waits for all concurrent branches in a `fork` group to finish.

**Only used in the concurrency-group scenario (the user explicitly asked for concurrency and `concurrency=group` was
declared)**; concurrency is off by default, and a lone `wait` means nothing.

## config shape

```json
{
  "fork_node_id": "<id of the fork node on the same process chain>",
  "timeout": {"enabled": false, "seconds": 30}
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `fork_node_id` | ✅ | — | the `fork` node's id, which must be on the **same process chain**; missing, wrong, or on another chain are all `config` errors (no longer `internal`) |
| `timeout` | — | not enabled → the instance / system limit (**not 30 seconds**) | `{enabled, seconds}` |

No output variables.

## Behaviour

Scans the status of the concurrency group members: any `failed` / `cancelled` → this node fails (`other`);
still incomplete at the timeout → `timeout`. A given fork can be waited on by only one `wait` (a duplicate wait → `config`, a graph
structure error).

## Outgoing edges

An ordinary node: continues along **its first outgoing edge** once the join completes.

Source: `service/flow/executors/wait.go`
