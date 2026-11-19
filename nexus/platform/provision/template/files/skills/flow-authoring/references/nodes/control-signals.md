# Control signals: `break` / `continue` / `return` / `exit`

All four nodes have **no config** (leave `config` empty); they finish immediately with `success` and throw an internal signal:

| Node | Signal | Scope | Typical use |
|---|---|---|---|
| `break` | `loop_break` | the **innermost** `loop`: end the loop, which succeeds | break out once the target is found |
| `continue` | `loop_continue` | the innermost `loop`: skip the rest of this round | skip empty items |
| `return` | `function_return` | the innermost `function`: return normally; **inside a loop it exits the whole loop** (the loop succeeds); at the top level it ends this run | early return from a function |
| `exit` | `flow_exit` | propagates all the way up, **terminating the whole run** (every container closes successfully) | a termination condition hit, stop right away |

## Constraints

- `break` / `continue` **only work inside a `loop`**: writing them directly in a `function` body reports a `config` error
  (`break/continue can only be used inside a loop`).
- None of the four triggers `on_error`: they are not errors.
- The node itself is recorded as `success` but **does not follow its outgoing edge**.

## Common errors

- "continue has no effect" — it is inside a `process` that is not inside a `loop`: the signal travels all the way to the loop
  before taking effect, and no node in between runs.
- "return does not return" — it was written inside a loop: the semantics are to exit the loop, not the run.

Source: `service/flow/executors/control.go`
