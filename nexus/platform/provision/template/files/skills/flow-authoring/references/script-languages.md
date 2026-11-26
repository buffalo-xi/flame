# Writing scripts: lua / python / js

Every script surface in a flow runs through the same executor — the `code` node, `branch` / `loop` conditions, the
`on_error` handler and post script, the streaming handlers of `llm` / `http` / `mcp` / `rpc`, output-binding transforms and
`result` / `progress` nodes. The **language conventions are therefore identical everywhere**; what differs is only the type
of value each surface expects back.

## Returning a value

| `lang` | How the script returns | Why |
|---|---|---|
| `lua` | a top-level `return` | the chunk is called as a function; its first return value is the result |
| `python` | assign to **`__return__`** | the script runs under `exec`, which has no return value, so the host reads the `__return__` variable |
| `js` | an ordinary `return` | the script is wrapped in an async function, so `return` (and `await`) work |

```lua
return 45
```

```python
__return__ = 45
```

```js
return 45;
```

A `return` statement in Python is a `SyntaxError: 'return' outside function`. This is the most common way to break a script
that was written for lua and then switched to python: it fails outright, it does not silently return nothing.

`input` is always present and is always an object (empty when no inputs are declared):

```lua
local n = input.n or 0
```

```python
n = input.get("n", 0)
```

```js
const n = input.n ?? 0;
```

## What each surface must return

Pick the language's return form above, then shape the value to the surface:

| Surface | Configured at | Must return | Notes |
|---|---|---|---|
| `code` node | `config.code` | an **object** keyed by declared variable names | an undeclared key fails the node: `the script returned an undeclared variable: X` |
| `branch` condition | `config.children[].condition` | a truthy value | `bool` preferred; `"true"` / a non-zero number also count as true, everything else is false |
| `loop` condition | `config.loop.condition` | **strictly a `bool`** | a string `"true"` is **not** accepted here (unlike `branch`) — return a real boolean |
| `on_error.handler` | `config.on_error.handler` | `"skip"` or `{action, error_code?, error_message?}` | a map rewrites the code / message and the node still fails; anything else also fails |
| `on_error.post.handler` | `config.on_error.post.handler` | an **object** keyed by output variable names | overwrites the node's outputs |
| `llm` / `http` / `mcp` / `rpc` handler | `<type>.handler` | an **object** keyed by custom output variable names | a string value appends, anything else overwrites; the node's fixed outputs are written by the node and must not be returned |
| output binding transform | `outputs.<name>.binding.transform` | a **string** | non-strings are stringified |
| `result` / `progress` node | `config.code` | a **string** | `template` is also allowed on these two nodes (pure Go rendering) and returns the rendered text directly |

## Two silent failures

- **`print` is not a return.** `print` / `log` / `console.log` go to the node log (`script output:` in the run detail) and
  write no variable. A `code` node whose script only prints **succeeds with every output empty**.
- **Assigning `__return__` inside a function needs `global __return__`.** Without it the name is a local variable and the
  host keeps reading `None`.

## Which languages are available

Only the languages listed under "workflow script languages" in the system prompt are pooled by the sandbox; a language
written from memory may only report `unsupported_lang` at runtime (mapped to a `config` error). When the list is
unavailable, use `lua`. `template` is pure Go rendering — it never touches the sandbox and is only valid on `result` /
`progress` nodes and binding transforms, not on a `code` node or any handler.

### Picking one: lua first, another language only for a reason

Write `lua` whenever it can do the job — it is the default (`lang` omitted = `lua`) and the simplest surface (a top-level
`return`, no `__return__` bookkeeping). The sandbox's Lua is a **bare interpreter with no third-party modules** — the image
ships no rocks, even JSON is encoded by the host wrapper — so reach for `python` / `js` only when the task needs something
Lua cannot do at that level:

| Situation | Choice |
|---|---|
| Arithmetic, string / table shaping, filtering, conditions, handlers, `on_error` scripts | `lua` |
| A real document or binary format — `xlsx`, `zip`, `pdf`, an image | `python` / `js`: `xlsx` alone is a zip of XML parts, and Lua has neither zlib nor a package that could add it |
| A library the task depends on (`requests`, `openpyxl`, …) | the language whose runtime actually carries it — read the sandbox probe result in the system prompt; never assume it is installed |
| The user names a language | that language, as long as it is in the pooled list |
| Another script node in the same flow already uses `python` / `js` | follow it — do not mix languages inside one flow without a reason |

Plain text output (CSV, `json`, a report string) does **not** justify switching: it is string building, and Lua does it
fine. Packages cannot be installed from a flow — that means rebuilding the sandbox image — so "Lua + a module" is not an
option.

## Errors

- runtime error → the traceback / stack lands in the node log, error code `script`;
- the platform has not enabled script execution for the tenant → `capability` (retrying does nothing, ask an administrator);
- the sandbox pool is exhausted → `busy` (the script never ran, so a retry is safe);
- a language the sandbox does not support → `config`.

Source: `service/flow/executors/script_exec.go`, `branch.go`, `loop.go`, `binding.go`, `result.go`, `progress.go`,
`service/flow/executors/runner.go`, `sandboxd/internal/codehost/templates.go`
