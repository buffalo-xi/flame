# The `fork` node

A concurrency group marker: it does nothing itself, it just gives the "nodes to run concurrently" a group name (its node id).

## config shape

```json
{"max_concurrency": -1}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `max_concurrency` | — | `-1` (unlimited) | how many run at once in this group; only `>=1` throttles. **Must be a number** — a wrong type (e.g. a string) is a `config` error instead of silently meaning "unlimited" |

No output variables, never fails.

## How a node joins a concurrency group

**Concurrency is off by default**: the tools strip `config.concurrency` from nodes and return a warning, unless the user
explicitly asked for concurrency and `concurrency=group` was declared in the call (`create_flow` / `create_node` /
`insert_node`).

Once enabled, write `config.concurrency` on **the node that should run concurrently**:

- absent = serial, following its outgoing edge normally;
- `{"mode": "mono"}` = mono concurrency (one node processes several records concurrently; not part of any fork group);
- `{"mode": "fork", "fork_node_id": "<fork node id>"}` = join that `fork` group (group concurrency).

A node marked for concurrent execution **runs only itself and does not follow its outgoing edge** — the subsequent flow is
joined by a `wait` node.

## Outgoing edges

The `fork` node itself behaves like an ordinary node, following **its single outgoing edge** (usually straight to `wait`, or
nowhere).

Concurrent branches are **not** pulled out of the `fork` — they are declared by the members' own `config.concurrency` (see
above). So a second outgoing edge on `fork` means nothing and reports a `config` error at runtime.

Source: `service/flow/executors/fork.go`, `runner.go`
