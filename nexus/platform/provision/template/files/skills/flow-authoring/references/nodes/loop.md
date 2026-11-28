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
| `count` | `total_times` (number) | a fixed number of times; `0` (or negative) runs **no round at all** and the node still succeeds; only a missing key falls back to the system limit `ResolveMaxLoopTimes()` |
| `each` | `source_key` (string), `break_on_empty` (bool) | `source_key` must be **a key of the same name in `config.inputs`**; its array is iterated item by item; an empty array with `break_on_empty=true` ends immediately; a `null` element aborts when the switch is true |
| `while` | `lang` (default lua), `condition` (script), `max_times` (number, `<=0` = system limit) | **check first, then run** |
| `do_while` | same as above | **run at least one round first**, then check |

`total_times` / `max_times` **must be numbers**. A wrong type (for example a string `"5"` after an asset-pack export) reports a
`config` error instead of silently falling back to the system limit — that silent path would blow "run 5 times" up to
"run to the limit".

`max_times` of `while` / `do_while` is the **ceiling**, not a round count: `<=0` there keeps the system limit (an explicit 0
ceiling is read as "no ceiling declared"). `do_while` always runs at least one round, so its ceiling cannot stop the first one.

`while` / `do_while` condition scripts support `loop.timeout = {enabled, seconds}`; when off (or unset) there is no node-level
timeout and the run limit applies.

**The condition must return a real boolean**, and the return form depends on the language: `return input.index < 5` in lua /
js, `__return__ = input["index"] < 5` in python. Unlike a `branch` condition — which also accepts `"true"` and non-zero
numbers — a loop treats anything but `true` as false, so a string `"true"` ends the loop on the first check. See
[`../script-languages.md`](../script-languages.md).

## Loop variables per mode

The four modes publish different variables, and both the node panel and the tools fill `config.loop_vars` with exactly the set
for the mode declared:

| Mode | Variables | Written | Meaning |
|---|---|---|---|
| `count` | `index`, `total` | before each round runs | `index` = 0-based round; `total` = `total_times` |
| `each` | `index`, `item`, `total` | before each round runs | `item` = current element; `total` = array length (after the system-limit truncation) |
| `while` | `index` | **before the condition is evaluated** | the round about to be tested, so `return input.index < 4` runs four rounds |
| `do_while` | `index` | before each body run; the condition then sees that same number | the round that just ran — `input.index < 2` yields three rounds, because this mode always runs at least once |

Three consequences worth knowing:

- in `while` / `do_while` the condition script is the first run-time reader of these variables, which is why `index` is
  written before the check (not after the round);
- a step **inside the body** reads the current round's variables the same way it reads any other node output: declare it in
  `config.inputs` with `source_type: "node"`, `source_ref` = the loop node id and `source_key` = `index` / `item` / `total`
  (the variable picker lists them under the loop node). Outside the body they are not readable: after the loop they only hold
  the last round, so the static check reports such a reference;
- **`config.inputs` is re-resolved before every condition check** in `while` / `do_while` (`count` / `each` keep the startup
  snapshot, because an iterated array must not change under its own iteration). A condition can therefore read a value the
  body wrote back into the loop's `outputs` / `locals`, and a declared input is re-read each round too. Precedence follows
  `NodeResult.GetVariable`: outputs and locals shadow a loop variable of the same name. The **first** check sees the declared
  defaults (`number` → `0`, `string` → `""`), so write the condition so that the default starts the loop — `total < 100`
  with `total = 0`, not `total > 0`;
- the static check knows this shape: a `while` / `do_while` loop reading its own `outputs` / `locals` / `loop_vars` through
  `config.inputs` is **not** `self_ref`, so do not restructure a correct accumulator to silence a finding (`count` / `each`
  are still reported — their snapshot never moves).

```json
{"inputs": {"i": {"type": "number", "source_type": "node", "source_ref": "<loop id>", "source_key": "index"}}}
```

`loop_vars` and `outputs` are two separate channels; do not mix them: loop variables (the index, the current element) go into
`loop_vars`, while ordinary values produced by the loop body go into `outputs` (child nodes write back via `targets`, with
`source_ref` pointing at the loop node). Writing to the wrong channel means the consumer gets nothing.

Never name an output `index` / `item` / `total`: a read resolves `outputs` before the loop variables, so such an output
silently shadows the round value for every reader, inside and outside the body.

## The loop body and "after the loop"

- the loop body = the child chain, entered at the `loop`'s `entry` child (`label: entry`), running along the first outgoing edge
  to the end; each round gets a fresh child process. When no entry is found it reports a `config` error at runtime and does
  **not** silently no-op for N rounds.
- **after the loop** = the `loop` node's own **single outgoing edge** (there is no separate body / next handle); connecting a
  second one reports a `config` error.
- child nodes write values back into the `loop`'s `outputs` via `targets` (`source_ref: <loop id>`); `iterations` records the
  actual round count.
- `iterations` is written by the runner and is **not** a `config.outputs` entry: the node panel lists it in the output
  variables as a locked row. Declaring an output with that name makes it shadow the real count, because a read resolves
  `outputs` before the loop variables — the same reason `index` / `item` / `total` must never be output names.
- each round's body is a **concurrency scope** as well (`config.parallel`, members as direct children carrying
  `data.concurrent: true`), with the same rules as `process` — see [`process.md`](process.md#concurrency-scope);
- a failed round fails the loop, carrying the round number. The loop's own `on_error` can `skip` it, rewrite the error with
  `handler`, or write fallback outputs with `post`; there is no `retry`, because re-running the body would repeat every
  member's side effects — see
  [`../on-error.md`](../on-error.md#scope-hosts-process--loop).

## Control signals inside a loop

| Node | Effect |
|---|---|
| `break` | ends this level of the loop; the loop node succeeds |
| `continue` | skips the rest of this round and starts the next |
| `return` / `exit` | the loop records success and the signal **propagates upwards** |
| any other error | the loop fails, carrying the round number |

Two consequences that only show up in a run:

- both `break` and `continue` **have no outlet**, so a node after the signal is never reached. Work that must still run in
  that round belongs on a `branch` child chain that does not pass through the signal node — you cannot "continue and then
  carry on down the body".
- `continue` ends the round, not the exit test: a `do_while` evaluates its `condition` after a continue round exactly as
  after a normal one, the way `do { ... continue; ... } while (c)` does. Only a false condition or `max_times` ends the loop.
  A `while` needs nothing special — its condition is checked at the top of the next round anyway.

## Common errors

- `loop config not found` / `unknown loop type`.
- `the each source is not in inputs: <key>` — `source_key` was not declared in `config.inputs`.
- `the each source is not an array: <key>` — upstream supplied something other than an array.
- `loop config <key> must be a number, got <type>` — a non-numeric `total_times` / `max_times`.
- `condition_no_return` / `condition_input_ref` — the `while` / `do_while` condition is checked the way a branch condition is:
  no return token at all (`return` in lua / js, `__return__` in python) and an `input.<name>` the node does not hand in are
  reported by `validate_flow` before the run. A name that only exists in `outputs` / `locals` / `loop_vars` is legitimate —
  the runner merges those into `input` before every check.
- A `break` in a sub-chain errors — it is not inside any loop (most likely a child node was moved out of its container).

Source: `service/flow/executors/loop.go`
