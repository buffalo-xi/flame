# The `process` node

An inline sub-flow container: groups a set of nodes into one box, runs them in order, and returns to the main chain.

## config shape

```json
{
  "outputs": {"summary": {"type": "string", "description": "sub-flow output"}},
  "locals": {}
}
```

## Fields

| Key | Required | Default | Meaning |
|---|---|---|---|
| `outputs` | — | `{}` | variables the sub-flow exposes; written by **inner nodes** via `targets` (`source_ref: <process id>`) |
| `locals` | — | `{}` | container-local variables |
| `parallel` | — | absent (no author limit, `collect`, no deadline) | the **concurrency scope** settings of this body: `{"max_concurrency": 2, "join": "drain", "timeout": {"enabled": true, "seconds": 60}}`. `max_concurrency` <= 0 or absent = no author limit (the tenant ceiling still applies, see below); `join` = `collect` (default) / `drain` / `abort`; `timeout` uses the shared `{enabled, seconds}` shape and bounds the whole body |

It **does not read** `inputs`: **do not declare one.** A body node reaches the outer values directly — a `source_type: node`
reference resolves up the process chain (`FindNodeResultByProcessChain`) — and writes back through `targets`, so the container
is not a call boundary and has nothing to hand down. `create_flow` / `create_node` / `update_node` remove a stray
`config.inputs` on a `process` node and return a warning, and `validate_flow` reports `ignored_inputs` for a diagram that
already carries one.

There is **no** reference key for "call another flow" either — the children are simply the nodes with
`parentNode = process.id` in the same diagram. (For reuse with arguments, wrap the body in a `function` and call it.)

## Structural requirements

- the container must have an `entry` child (`type: "entry"`, `parentNode: <process id>`), generated automatically when the
  front end creates a container.
- no entry → a `config` error at runtime (`process node X: body entry node not found`).
  It used to be a silent skip, so the whole sub-flow never ran and nobody could tell.
- the body chain runs from the entry along the first outgoing edge to the end; each execution gets a fresh child process.
- the container itself may have only one outgoing edge; a second one reports a `config` error (not "only the first is followed").

## Concurrency scope

A `process` body is one concurrency scope. A **member** is an **executable** direct child that carries `data.concurrent: true`
and lies on the body's entry chain: the body dispatches it and keeps walking along the chain, and the body's end **joins**
every member. Control, output and instant nodes are never members: the `entry`, `break` / `continue` / `return` / `exit`, the
comment / layout nodes, `result` / `progress` and `assign` / `memory` have no such flag (`concurrent_member_type`) — control
flow stays on the chain, result / progress append to the run in call order, and assign / memory never wait, so a slot buys
nothing. An `exit` written inside a member's own body still ends the whole run - the nested host carries the signal out.

The two words are not interchangeable: `concurrent` is the flag on the node (`data.concurrent`, the `concurrent` argument, the
canvas tick), `parallel` is the host's block and record (`config.parallel`, `ParallelResult`); findings follow the same split
(`concurrent_member_*` for the flag, `parallel_*` for the scope's shape).

- set the flag with `concurrent: true` on `create_node` / `insert_node` (the parent must be a `process` or `loop`), clear it
  with `update_node {"concurrent": false}`. It lives in `data`, not `config`, so a config rewrite never loses it;
- the host carries the settings in `config.parallel`;
- a member failure fails the run without anything waiting for it. `collect` (default) lets the other members finish; `drain`
  stops starting new work, so queued members and later chain nodes never start (recorded `skipped`, reason `scope draining`);
  `abort` does that and also cancels the members still running (recorded `cancelled`, reason `aborted by scope`) — the chain
  node already executing finishes, and the walk stops at the next boundary;
- **a failed scope is an ordinary node failure**, so the host's own `on_error` is where it is handled: `skip` continues the
  main chain past the section (members keep their records, the snapshot stays on the skipped host), `handler` may rewrite the
  error code / message or return `action="skip"`, and `post` writes the fallback outputs. There is no `retry` on a host —
  re-running the body would repeat every member's side effects. A member's own `on_error` runs first and often removes the
  need for either. See `../on-error.md`;
- **a skipped scope still publishes what its members wrote**, which is why `skip` is only safe when nobody reads those outputs:
  after a `skip` the declared outputs hold either a member's value or the declared default, and with `join: "abort"` a
  cancelled member may have written half of it. Leave a marker the reader can test (`post.set_outputs`, e.g. `ok = false`)
  instead of relying on the gap being noticed;
- a scope `timeout` does the same when the deadline passes instead of a failure: the host fails with `timeout`, the message
  lists every member's state at that moment, and the snapshot is marked `abandoned`. A deadline that fires after the last
  member finished is ignored — a completed body has not timed out;
- `max_concurrency` limits members only. It never blocks the walk, so whatever follows the host is still reached. The
  number is a request: before the body starts the executor lowers it to the tenant ceiling (`flow_configs.max_concurrency`,
  code fallback 16), logs one line when it lowers an explicit value, and records both `max_concurrency` (what ran) and
  `system_ceiling` (what bounded it) in the snapshot — so a scope never runs unbounded;
- results leave the body only through the host's `outputs`, written by members' `targets`. A member never reads a sibling
  member and a chain node never reads a member — `validate_flow` reports `parallel_visibility`. To let one member's value feed
  another, use two hosts in series: `process { A } → process { B }`;
- the top level, a `function` body and a `branch` lane are not scopes: the flag there is `concurrent_member_parent` /
  `concurrent_member_not_on_chain`.

## Control signals

`break` / `continue` / `return` / `exit` all finish successfully and **propagate upwards** (they are not swallowed inside the
container). What that means per signal:

| signal | what a `process` body does with it |
|---|---|
| `break` / `continue` | passes it up; the **loop** that encloses it consumes it (leaking to the function boundary or the root is a config error) |
| `return` | **not allowed** in a `process` body. A return is only consumed by a function (`function.go` catches it), so here it would end the whole flow — successfully, with the rest of the chain never running — while reading as "leave this container". `validate_flow` reports `return_scope` as an error and `create_node` refuses to create one. If you need an early exit, put the body in a `function` and `call` it, or route around the rest with a `branch` |
| `exit` | ends the whole flow regardless of nesting |

`create_node` and `validate_flow` therefore both refuse a `return` outside a function: the function boundary is the signal's only consumer.

## Outgoing edges

After the container finishes, execution continues along the `process` node's own **first outgoing edge**.

Source: `service/flow/executors/process.go`
