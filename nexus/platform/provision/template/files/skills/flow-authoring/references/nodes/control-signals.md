# Control signals: `break` / `continue` / `return` / `exit`

All four nodes have **no config** (leave `config` empty); they finish immediately with `success` and throw an internal signal:

| Node | Signal | Scope | Typical use |
|---|---|---|---|
| `break` | `loop_break` | the **innermost** `loop`: end the loop, which succeeds | break out once the target is found |
| `continue` | `loop_continue` | the innermost `loop`: skip the rest of this round | skip empty items |
| `return` | `function_return` | the innermost `function`: return normally; **inside a loop it exits the whole loop** (the loop succeeds); it **must have a function above it** — at the top level it ends the whole run, so the tools refuse it and `validate_flow` reports `return_scope` (use `exit` there) | early return from a function |
| `exit` | `flow_exit` | propagates all the way up, **terminating the whole run** (every container closes successfully); a **concurrent member** may fire it too — the member succeeds, the scope stops, its siblings are cancelled | a termination condition hit, stop right away |

## Constraints

- `break` / `continue` **only work inside a `loop`**: writing them directly in a `function` body reports a `config` error
  (`break/continue can only be used inside a loop`). `validate_flow` / the canvas badge reports the same placement problem
  before the run (`control_scope`): a `break` / `continue` that no loop can reach, including one inside a `function` body
  whose call site sits in a loop. A `process` forwards the signal, so its body stays in scope.
- `return` **must sit inside a `function`** — a direct child or any depth below it (a `process` / `loop` under the function is
  still inside it). Anywhere else it has no consumer, so it would end the whole flow while the run reports success:
  `create_flow` / `create_node` refuse to build one and `validate_flow` / the canvas badge reports `return_scope`. End the
  whole run with `exit`; to leave only a container, route around the rest of the body with a `branch`.
- `break` / `continue` end the round, **not the loop's exit test**: a `do_while` evaluates its `condition` after a continue
  round exactly as after a normal one (`do { ... continue; ... } while (c)` semantics), so only a false condition or
  `max_times` ends the loop. Skipping that check was a defect until 2026-11 and made such a loop run to `max_times` while
  still reporting `success` — a diagram written against it needs its `condition` / `max_times` rechecked, not a `break`
  added to compensate.
- An `exit` run still **succeeds**: the record tells the two endings apart, not the status. A successful `exit` node in the
  node list is the proof, and `run_flow` / `read_flow_run` summarise it as `stop_reason: "exit"` + `stop_node_id` (absent =
  the flow walked its normal end). The run-records page shows the same node; there is no separate status value to rely on.
- An `exit` from a **concurrent member** means the same thing as one from the body's chain: the member records `success`,
  the scope stops starting work (later chain nodes are `skipped` with "terminated by exit at node X"), the members still
  running are **cancelled** with that same reason — never `failed`, a casualty is not a cause — and the host carries
  `exited_by` in its `parallel` snapshot. It outranks a failure that happened in the same instant: the author asked for the
  run to stop, and the failed node keeps its own failed record either way. `break` / `continue` / `return` are **not**
  global — from a member they are refused with `config` ("cannot leave its body"), because only a loop or a function of
  that body could consume them.
- None of the four triggers `on_error`: they are not errors.
- The node itself is recorded as `success` but **does not follow its outgoing edge**.

## Common errors

- "continue has no effect" — it is inside a `process` that is not inside a `loop`: the signal travels all the way to the loop
  before taking effect, and no node in between runs.
- "return does not return" — it was written inside a loop: the semantics are to exit the loop, not the run. Outside a function
  altogether (top level, or a `process` / `loop` body with no function above it) it is refused outright as `return_scope`.

Source: `service/flow/executors/control.go`
