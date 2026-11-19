# Custom Tool Authoring Guide

## Purpose

Turn "a fixed piece of business logic" into a **tenant-level custom tool** (public tool name `custom__<name>`):
the model supplies arguments, the script computes the result, and a structured JSON value comes back.

This skill covers **whether it should be a tool at all**, **choosing the language**, **designing metadata and
parameters**, **how to write the code**, and **how to verify it**.

## Two ways to get it done

| Who | How | Shape |
|---|---|---|
| Super agent | `create_custom_tool` → `write_file` → `grant_agent_tool` | fully automated, three steps |
| A person | fill in the form and paste the code on the "Tools → Custom tools" page | form fields map one-to-one onto sections 1 and 2 below |

The super agent's three steps, **in this order and no other**:

1. **`create_custom_tool`** — create the entity: the contract file `tools/<name>/TOOL.json` plus a minimal runnable skeleton.
   Arguments: `name` / `description` / `display_name` / `lang` / `entry` / `parameters` / `output` / `config_fields` / `configs` / `timeout_ms` / `max_output_bytes` / `egress`.
2. **`write_file("/tools/<name>/<entry>", "<full code>")`** — write the real implementation, overwriting the skeleton.
   Without `entry`, the default depends on the language: `main.py` / `main.js` / `main.lua` / `main.sh`.
3. **`grant_agent_tool(agent="<target>", ref="custom:<name>")`** — grant it to the agent that will use it.

**The contract lives in `TOOL.json`, next to the code** (the file on disk wins over the row in the database):
description / parameter schema / language / entry / limits / configuration declarations all live there. To rename or
change the schema go through `update_custom_tool` (it writes the file too); do not edit the database by hand.

**Skipping step 3 = the model cannot see the tool.** Created but not granted, it never shows up in the target agent's
tool catalog, so the model never calls it (introspection is `list_custom_tools`, and that is the super agent's job).

Maintaining an existing tool:

| What to change | With what |
|---|---|
| Description / parameter schema / language / entry / timeout / egress / configuration | `update_custom_tool` (pass only the fields to change) |
| Code | `write_file` straight to `/tools/<name>/<entry>` (do not drop the leading `/`, see step 2) |
| Enable / lock / deprecate | **the console** (status changes do not go through meta tools) |
| Delete | `delete_custom_tool` (refused by default while it is granted / depended on / referenced by a group, listing the references; pass `force=true` to delete anyway) |

**Existence follows `tools/<name>/TOOL.json`** (the directory is the entity): copying a whole tool directory into
`tools/` makes it exist (normal status, no configuration by default), and removing the directory deletes it. Conversely,
changing only the database without writing the file has no effect — description and parameters follow the file.

Agents other than the super agent do not have these meta tools (category `custom-tool`, super agent only); they can only
hand the code to the user to create it under "Tools → Custom tools".

## When to use

When these intents appear:

- "write a tool that looks up / computes / converts X", "let the agent call this script itself"
- "I paste this script every time — can it be saved as a tool?"
- "the custom tool does not work": output is not JSON, timeouts, missing configuration, egress blocked

**Decide the shape first; do not default to a custom tool:**

| Need | Use | Rule of thumb |
|---|---|---|
| One-off task | just do it (`exec` / existing tools) | runs once, no reuse |
| Fixed logic, model supplies arguments, structured result | **custom tool** | pure computation / lookup / HTTP call with clear input and output |
| Needs to read and write session files, or run arbitrary commands | built-in `read_file` / `exec` | a custom tool is "one function", not another shell |
| Knowledge, process, conventions (no code) | a skill (read `skill-authoring` first) | nothing executable needed |
| Executable scripts that ship with a skill | the skill's `scripts/` + `run_skill_script` | lives with the skill, no separate grant |
| One codebase serving several tenants, with per-tenant keys / several implementations | a plugin | a custom tool **belongs to this tenant only** |

In one sentence: **it must be granted to an agent, appear in the tool catalog, and take a parameter schema — only then is
it a custom tool.**

## Step 0: choose the language

`python` / `js` / `lua` / `sh` are supported, but **whether it can actually run depends on the deployment**, not on the code:

| Language | Path | Notes |
|---|---|---|
| `python` | sandboxd's `langs.python` | **the default choice**: almost every environment has it |
| `js` | `langs.js` | the binary in the image is usually called `node`; the language name is still `js` |
| `lua` | `langs.lua` | most often missing; some deployments require an absolute path (e.g. `lua55`) |
| `sh` | bypasses `langs`, runs `sh <entry>` directly | works wherever the sandbox has `/bin/sh`; only for simple commands |

How to decide:

1. **Default to python** unless there is a clear reason not to;
2. before switching, confirm the environment has it: the top of the "Tools → Custom tools" page shows "sandbox xxx · runnable languages a / b", and unavailable languages are marked "(not available in this environment)" in the dropdown;
3. when the page shows "sandbox capability unknown", **do not guess**: start sandboxd first, or run `node -v` / `lua -v` once with `exec` to confirm;
4. under `mode=none` a wrong language configuration **fails at startup** (host `LookPath` validation); under `nsjail` / `docker` it is not validated, so the error only appears on the first call — never judge availability by "the service started fine".

## Hard constraints (read first, to avoid wasted work)

| Constraint | Details |
|---|---|
| Name | letters, digits and dashes only (`sum-sales`), cannot start with a dash; **must not contain `__`** (the public name is `custom__<name>`); ≤ 64 characters; **immutable after creation** (renaming silently invalidates every `custom:<name>` grant) |
| Where to write code | must be `/tools/<name>/<entry>`: **a leading `/` means from the tenant root**. Without it the path is treated as workspace-relative — the file lands in the session workspace while the tool keeps reading the skeleton from creation time, which looks like "I changed the code but the old logic runs" |
| Three-part rule | `status = 1` **and** the target agent is granted `custom:<name>` **and** the platform has `sandbox.exec` enabled. Miss one: the catalog entry is greyed out and execution is refused |
| Runtime | inside the sandbox the cwd is `/workspace` (this run's workspace, writable); the tool directory is mounted read-only at `/tool` |
| Egress | offline by default; requires `egress` **and** the platform's `net.egress` to be enabled |
| Timeout | the smaller of the tool's `timeout_ms` and this run's limit; maximum 600000 |
| Output | `stdout` must be **one valid JSON value**; exceeding `max_output_bytes` (default 65536, maximum 1048576) **fails outright** (no truncation) |
| Logs | always write to `stderr` (also length-limited) |
| Secrets | `secret` configuration items are masked on read-back; the script sees the plain value, but any **string** configuration value of **length ≥ 8** appearing in the output is replaced with `••••` and the result gets `_masked: true` (short values are not masked, to avoid mangling prose — do not rely on it for short keys, and never put them into the result yourself) |

## When handing it to a person, deliver three sections

1. **Metadata table** — field by field, ready to copy into the form;
2. **Full code** — one complete runnable file (not a fragment, no `...`);
3. **Verification checklist** — what to confirm, in order, after creating it.

When the super agent does it itself, there is no need to **output** these three sections, but the order is the same: decide
the metadata first, then write the code, then walk the checklist.

---

## Section 1: metadata

| Field | What to put |
|---|---|
| `name` | `sum-sales` (letters, digits, dashes; immutable after creation) |
| `display_name` | short panel title, may be empty (**does not affect the model**, which only sees name + description) |
| `description` | **for the model**: one sentence saying when to use it and when not to |
| `lang` / `entry` | `python` + `main.py` (or `js`/`main.js`, `lua`/`main.lua`, `sh`/`main.sh`) |
| `parameters` | input JSON Schema (**sent to the model**) |
| `output` | output JSON Schema (**not sent to the model**; shallow validation and display only; may be empty) |
| `config_fields` | configuration declarations (API keys, thresholds, etc.) |
| `timeout_ms` | default 60000; leave 2–3× headroom over the estimated runtime |
| `max_output_bytes` | default 65536; raise it for large tables, but stay within what the model can read |
| `egress` | enable only when external HTTP is genuinely needed, and name the target domains in the description |

### How to write the description

The model decides whether to call the tool based on it. Write "purpose + boundary":

- Good: `Summarizes sales by region and month, returning the total and the month-over-month change. Data comes from the tenant's built-in tables; no external network access.`
- Bad: `A powerful sales summary tool`

### How to design parameters

1. **Only include what the model can reliably supply**: it cannot see your database, so do not design parameters such as `table_id` or `file_path` that it cannot guess;
2. give every parameter a `description`; add `enum` where possible, and a `default` where possible;
3. put required ones in `required`; optional ones need a default or an explicit "if omitted, …";
4. use `string` / `integer` / `number` / `boolean` / `array` / `object`; avoid combinations such as `oneOf` (the model fills them in wrongly);
5. **do not design path parameters**: path semantics are unstable.

```json
{
  "type": "object",
  "properties": {
    "month": { "type": "string", "description": "Month in YYYY-MM form, e.g. 2026-09" },
    "region": { "type": "string", "enum": ["east", "north", "south"], "description": "Sales region; omit to aggregate all" },
    "top_n": { "type": "integer", "default": 5, "description": "Return the top N products, default 5" }
  },
  "required": ["month"]
}
```

### How to write `output` (optional, recommended)

- validation only happens when `output` is set, and it is **shallow**: the top-level `required` keys exist and the basic types declared in `properties` match (extra fields are fine);
- **once `output` is set, the script must emit a JSON object** (arrays are rejected);
- deep structures are not validated recursively; do not expect it to replace validation inside the script.

```json
{
  "type": "object",
  "properties": { "total": { "type": "number" }, "items": { "type": "array" } },
  "required": ["total"]
}
```

### `config_fields` and configuration values

Anything the tenant must fill in (API keys, endpoint URLs, thresholds) goes into configuration, never hardcoded:

| Field | Meaning |
|---|---|
| `key` | read in the script as `config["<key>"]` |
| `label` / `help` | what the person filling the form sees |
| `type` | `string` / `int` / `bool` |
| `secret` | masked on read-back; **any occurrence in the output is masked automatically** |
| `required` | missing it fails **before execution**, naming the missing key |
| `default` | may be empty |

Do not mark "optional nice-to-have" items as `required` — that makes the tool unusable until they are filled in.

### Boundary with global variables / environment variables

**A custom tool gets neither**, so do not design the script around them:

| | Where it works | Available inside a tool? |
|---|---|---|
| Global variables | workflow node inputs, `${global_vars.X}` in `AGENT.md` / `SKILL.md` | ❌ a tool has no node inputs, and documents are not expanded for it |
| Environment variables (`flows.env_vars`) | only the flow they belong to | ❌ not the tool's runtime context |

A tool is one process in the sandbox, and its only input is `args` + `config` on `stdin`. So the tenant's constants
(API base URLs, regions, accounts, thresholds) have **exactly one path**: declare them as `config_fields`, let a person
fill the values in under "Tools → Custom tools", and read them in the script from `config["<key>"]`. To serve the same
logic to several tenants, create one tool per tenant and fill in the configuration separately; do not expect it to follow
global variables.

Nor can you inject environment variables through `args.env`: `exec` and `run_skill_script` read `args.env`,
**custom tools do not** (the executor only adds its own runtime environment). The process environment a script can read
varies with the sandbox mode (`none` / `nsjail` / `docker`); using it as a configuration source welds the tool to one kind
of deployment.

---

## Section 2: implementation code

### Protocol (fixed)

```
stdin  : {"args": {...arguments from the model...}, "config": {...tool configuration...}}
stdout : one JSON value (the result itself)
stderr : logs / errors (never write these to stdout)
```

Read stdin to EOF before doing any work; do not wait for interactive input. **stdout must contain JSON and nothing else.**

### Skeletons

Python (default):

```python
import json
import sys


def main():
    payload = json.load(sys.stdin)          # read to EOF
    args = payload.get("args") or {}
    config = payload.get("config") or {}

    month = str(args.get("month") or "").strip()
    if not month:
        print("missing required argument month (YYYY-MM)", file=sys.stderr)
        sys.exit(2)                          # non-zero exit code → the tool fails; raw stderr is passed through

    top_n = int(args.get("top_n") or 5)
    # ... business logic ...
    result = {"total": 0, "items": []}
    json.dump(result, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
```

JavaScript (confirm the environment has `js` under `langs`):

```js
let raw = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', (c) => { raw += c })
process.stdin.on('end', () => {
  const { args = {}, config = {} } = JSON.parse(raw || '{}')
  if (!args.month) {
    console.error('missing required argument month (YYYY-MM)')
    process.exit(2)
  }
  process.stdout.write(JSON.stringify({ total: 0, items: [] }))
})
```

Lua (confirm the environment has `lua` under `langs`; there is no standard JSON library, so whether one ships depends on
the runtime):

```lua
local input = io.read("a")               -- read all of stdin
-- parse input with the JSON library the runtime provides to get args / config
local out = { total = 0, items = {} }
io.write(require("json").encode(out))    -- stdout carries the result only
```

Shell (simple commands only; use python for anything complex):

```sh
#!/bin/sh
payload=$(cat)
echo "$payload" >&2                      # debug output goes to stderr
printf '{"ok": true}\n'
```

### Error handling conventions

| Situation | What to do |
|---|---|
| Invalid arguments | write one plain sentence to `stderr` (which argument, what was expected) and `exit 2` |
| A business-level "not found / no permission" | **do not** throw; return `{"ok": false, "error": "no data for that region"}` normally |
| A genuine exception | print a summary of the stack to `stderr` and exit non-zero (the model sees the raw stderr) |
| Result too large | return a summary or the first N rows; exceeding the limit fails outright, nothing is truncated for you |
| The remote API is down | retry a few times (1–2), then return `{"ok": false, "error": "..."}` and say it may be retried |

The key: **express expected failures as JSON, express unexpected crashes as exit codes**.

### What you may and may not rely on

- ✅ `/tool` (this tool's directory, read-only): the main file may `import` sibling modules (for Lua, adjust `package.path` before `require`; see "Referencing sibling files");
- ✅ `/workspace` (cwd) is fine for temporary files **of this call**;
- ❌ do not expect `/workspace` to persist between calls: it follows this run's workspace and is not guaranteed to be there next time;
- ❌ do not assume `pip install` / `npm install` works: dependencies must be either built in or written by hand with the standard library;
- ❌ do not read `/agent`, `skills/` or other entity directories: only `/workspace` and `/tool` are mounted;
- ❌ do not log to stdout: a single `print("debug")` fails the whole call.

### Organizing multiple files

```
tools/sum-sales/
├── main.py          ← entry
└── rules.py         ← main.py can import rules
```

- the console form submits **metadata only** (written into `TOOL.json`); code and auxiliary files are edited by directory in the "Files" panel;
- after the super agent creates a tool, add auxiliary files directly with `write_file`: `write_file("/tools/sum-sales/rules.py", "<code>")`;
- you cannot write outside a **registered tool directory**, nor into a tool directory that does not exist (call `create_custom_tool` first);
- deleting a tool only goes through `delete_custom_tool` / the console; `delete_file` cannot remove a whole directory.

### Referencing sibling files

The entry point is launched as `[interpreter, /tool/<entry>]` and **the cwd is `/workspace`** — so paths relative to the
script file work, while paths relative to the cwd point at the workspace, not the tool directory:

| Language | How | Notes |
|---|---|---|
| Python | `from rules import calc` | the script's directory is on `sys.path[0]`, so the import just works |
| Node | `require("./rules.js")` | module resolution is relative to **the requiring file**, not the cwd |
| Lua | set `package.path = TOOL_DIR .. "/?.lua;" .. package.path` before `require("rules")` | **plain `require("rules")` fails**: the default `./?.lua` is relative to the cwd (the workspace) |
| Shell | `. "$TOOL_DIR/lib.sh"` | `source` follows the cwd for relative paths too |

Always derive `TOOL_DIR` from **the script's own location**; never hardcode `/tool`:

```python
from pathlib import Path
TOOL_DIR = Path(__file__).resolve().parent          # Python
```

```javascript
const TOOL_DIR = __dirname                          // Node (CJS; ESM uses import.meta.dirname)
```

```lua
local TOOL_DIR = (arg and arg[0] or ""):match("^(.*)[/\\]") or "."   -- Lua
```

```sh
TOOL_DIR=$(cd "$(dirname "$0")" && pwd)             # Shell
```

The reason: `/tool` only really exists under nsjail / docker. `mode: none` performs no isolation, and the mount table only
translates argv and cwd — a hardcoded `/tool/...` inside the code reports "file not found" when debugging locally yet works
in production, and that kind of divergence is the hardest to diagnose.

Two more constraints:

- **stdin can only be read once**: after the entry point reads it, it is at EOF, so a module it imports only gets an empty string — pass arguments in explicitly;
- **take configuration from `config` on stdin**; there is no need to parse `TOOL.json` for it (it is readable, but unnecessary).

---

## Section 3: verification checklist

1. **Registered**: `list_custom_tools` shows it (or the console's "Tools → Custom tools" does), status "enabled";
2. **Platform capability**: no "not enabled on this platform" warning (`sandbox.exec` is enabled — custom tools go through `/v1/exec`, the same tier as the `exec` tool; `sandbox.code` gates pooled workflow scripts and is unrelated);
3. **Language available**: the "runnable languages" line at the top includes the one you chose; if it says "capability unknown", confirm the interpreter exists with `exec` first;
4. **Granted**: `grant_agent_tool(agent="<target>", ref="custom:<name>")`. **Created without a grant = the model cannot see this tool**;
5. **Minimal call**: run it once with minimal arguments from the console's "Try it" (it runs the **saved** code; the button is greyed out while the form has unsaved changes) and confirm you get a JSON result;
6. **Parameter validation**: deliberately omit a required argument / pass the wrong type and confirm the error names the argument;
7. **Configuration**: run once with a `required` configuration item empty and confirm the error names the missing key; then fill it in and run again;
8. **Secret masking**: with a `secret` configuration item, confirm the result contains no plaintext secret (it is replaced and `_masked` is set);
9. **Egress**: when network access is needed, verify only after `egress=true` and the platform's `net.egress` is enabled; the offline error must be readable.

---

## Common pitfalls (by frequency)

1. **Logs mixed into stdout** → "stdout is not valid JSON". Send every debug line to `stderr`.
2. **Created but the model cannot see it** → the `grant_agent_tool ref="custom:<name>"` was forgotten, or the platform has not enabled `sandbox.exec`.
3. **Picked a language the environment does not have** → discovered only at call time. Check "runnable languages" at the top of the page first; default to python.
4. **Non-zero exit code although the script succeeded** → some libraries return non-zero at the end; make sure the normal path calls `exit 0` explicitly.
5. **Output over the limit** → return a summary or paginate; do not expect truncation.
6. **`output` set but an array is returned** → it must be a JSON object.
7. **Secrets in the result** → use a `secret` configuration item; never concatenate the key into the return value.
8. **`entry` changed without writing the file** → the call fails immediately, with an error that looks like "directory unreachable". Write the code in the same step as changing `entry`.
9. **Dependencies cannot be installed** → there is no package manager in the sandbox; reimplement with the standard library.
10. **Want to rename** → the name is immutable after creation (renaming breaks existing `custom:<name>` grants). To change it: create a new one, grant it again, then delete the old one.
