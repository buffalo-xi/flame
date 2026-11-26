# Code node: how to return a value

A code node does not "print" its result into the flow — it hands over **variable names and values**. Every language therefore has to answer one question: where does the return value go?

## The three languages differ

| Language | How to return | Why |
|---|---|---|
| Lua | `return` a table from the top level | the script is a chunk, and its first return value is the result |
| Python | assign to `__return__` | the script runs under `exec`, which has no return value, so the host reads the `__return__` variable |
| JavaScript | `return` an object directly | the script is wrapped in an async function, so `return` is the result |

Writing `return` in Python fails at once with `'return' outside function` — the first pitfall most people hit.

The code node only offers the languages the sandbox reports (lua / python / js); `template` is pure Go rendering and appears only on the Result and Progress nodes.

## Three hard rules

1. **Return an object of "variable name → value".** A key must be one the node declares: an output variable, a write target or a local variable. Any other name fails the node with `undeclared variable`.
2. **The value must be JSON-serialisable.** Strings, numbers, booleans, null, arrays and objects work; custom classes, functions and sets do not.
3. **`print` / `log` is not a return.** Both are redirected to stderr and only show up in the node log (`script output:` in the run detail); they write no variable.

## Common mistakes

- Copying Lua's `return {...}`: an immediate Python syntax error.
- Writing `def main(): return {...}` without calling it or assigning `__return__`: the node succeeds with every output empty.
- Assigning `__return__` inside a function without `global __return__`: it becomes a local name the host never sees.
- Returning a string or a number instead of an object: `unexpected return type`.
- Expecting `print(...)` to become the return value: it only reaches the log.

## Returning nothing still succeeds

With `__return__` left at its default (`None`) the node does not fail — it simply writes nothing. "The node succeeded but the downstream variable is empty" almost always means the return statement never ran, especially when it sits inside an `if` branch.

For troubleshooting, read `script stderr:` in the run detail: Python's traceback is reproduced there, and its line numbers are the line numbers of your own script in the editor.
