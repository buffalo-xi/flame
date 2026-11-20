# Skill Authoring Guide

## Purpose

Guide you through creating / updating / deleting a skill, and writing a high-quality `SKILL.md`.
Skills with **executable scripts** also need those scripts in the right place, with the body explaining how to run them.

## When to use

When these intents appear:

- "save this process / convention as a skill", "do it this way from now on"
- "write a skill that does X", "save this script / this code as a skill"
- "change / delete some skill", "which skills are there now?"

**Do not create a skill for something done once** — just do it.

## What a skill is

A skill consists of **two parts** (the second is optional):

| Part | Location | Required | Role |
|---|---|---|---|
| `SKILL.md` | skill directory root | yes | the knowledge body: how the job is done |
| `scripts/` | inside the skill directory | no | accompanying **executable scripts**, entry fixed at `scripts/main.py` |

Hence two shapes:

- **document-only skill** — body only (conventions, processes, templates);
- **executable skill** — the body explains "how to use it", the code lives in `scripts/`, and the agent runs it **directly** with `run_skill_script` — there is **no need** to copy the code into the workspace.

Directory layout (relative to `agent.root_dir`):

```
tenants/{schema}/skills/{name}/                    shared (tenant-level)
├── SKILL.md
└── scripts/main.py                                ← optional; the default entry when script is omitted

tenants/{schema}/agents/{agent}/skills/{name}/     private (agent-level)
├── SKILL.md
└── scripts/…
```

Every skill has an **ownership**, decided by `agent_id`:

| Ownership | Content location (relative to agent.root_dir) | How it takes effect |
|---|---|---|
| **Shared** (tenant-level) | `tenants/{schema}/skills/{name}/SKILL.md` | enters an agent's prompt only after being **bound** to it; visibility is separately controlled by "visible to everyone / allowlist" |
| **Private** (agent-level) | `tenants/{schema}/agents/{agent}/skills/{name}/SKILL.md` | owned by the owning agent automatically (resident in its prompt); only it can see, edit or delete |

On a name collision, **private shadows shared**.

**Shared ≠ automatically in effect**: a freshly created shared skill is **narrowed** (`restricted = true`, with an allowlist
containing only the creator). It must be bound under the agent's "Settings → Call grants → Available skills" before it is
written into that agent's prompt; agents it is not bound to **cannot see it and cannot change it**. **The super agent can do
both directly**: `bind_skill` / `unbind_skill` add and remove allowlist entries, and
`update_shared_skill(..., restricted=false)` opens it up to everyone (`restricted=true` goes back to allowlist mode, keeping
the allowlist data so nothing has to be rebuilt); `read_skill` echoes the current `restricted`, `bound_agents`, and the tool
dependencies it declares (`tool_deps`, shown but not activated).

## Shared or private?

The criterion: **is this knowledge used by every agent, or only by specific agents?**
Private and shared are **two separate tool sets**, and picking the tool picks the ownership:

| Situation | Tool |
|---|---|
| Meta capability (creating skills, creating agents, changing the system), specific to one agent's role and **maintainable by that agent itself** | `create_own_skill` (its own private; **requires an administrator grant first**) |
| Business domain knowledge, shared conventions (report format, PR process…) | `create_shared_skill` (tenant-level shared, **super agent only**) |
| Specific to one agent's role but **created on its behalf by the super agent** (it is not granted, or creating it itself is impractical) | `create_agent_skill` (private, owned by the named agent; **super agent only**) |

Two hard constraints:

1. **A skill's availability should match the availability of its tools** — confirm the target agent can actually get the tools a skill needs before binding it there;
2. **Binding is what injects it** — what enters the system prompt is the "binding" (tens to a few hundred characters each), regardless of shared/private.
   When in doubt, prefer private (private only ever enters the owning agent's prompt), or shared + bind only to the agents that really need it.

**Default to private; when unsure, also private** — private can become shared at any time, whereas a shared skill that has
spread requires unbinding it agent by agent to be clean again.
Agents other than the super agent only have the private set (`*_own_skill`, and **an administrator must grant it first**); when a
shared skill is needed, ask the super agent to create it and then `bind_skill` it.
The super agent can also use `*_agent_skill` to create / update / delete an agent's private skills directly — **without**
granting that agent `*_own_skill`.

## Code in the body, or in `scripts/`?

**Criterion: more than about 20 lines, or needs `import`, or needs argument parsing — always `scripts/`.**

| Shape | Fits | Cost |
|---|---|---|
| Embedded in the body | three-to-five-line snippets / one-off illustrations | the agent must copy it into the workspace and run it every time → the body and the copy **drift apart**; arguments cross a shell, with quoting traps |
| `scripts/main.py` | a complete runnable program (`import` / `argparse` / error handling / several functions) | the host agent must be granted `builtin:exec`; who may write it is restricted (see below) |

Three reasons to prefer `scripts/` — `run_skill_script` exists for exactly this:

- **one copy of the code**: the skill directory is mounted read-only at `/skill`, so there is no "one copy in the body, one in the workspace" divergence;
- **arguments never cross a shell**: `args` is passed to the script **verbatim as an argv array**, with no splitting / quoting / escaping issues
  (`exec` goes through a shell; on Windows `cmd` swallows quotes, and `python -c "…"` often shows up as exit 1 with no output);
- **saves turns**: one fewer `write_file`, and no re-copying every time.

### Hard conventions for scripts

| Convention | Detail |
|---|---|
| Entry | `scripts/main.py` — the default when `run_skill_script` has no `script` |
| Extension | only `.py` / `.js` / `.lua` / `.sh`; **the extension decides the interpreter** (command names `python` / `node` / `lua` / `sh`; **no** absolute paths or versions) |
| cwd | the workspace root (`/workspace` inside the sandbox). Reference files in the **workspace** with relative paths; for files in the **skill directory** see "Referencing files inside the skill directory from a script" below |
| Mounts | `/skill` **read-only** (skill directory), `/workspace` **read-write** (also the cwd); `/agent` is **not mounted** |
| Arguments | passed verbatim from the `args` array (argv), not as a single command-line string |
| Path | `script` must live inside the skill directory — arbitrary paths are rejected (running arbitrary paths is `exec`'s job) |
| Auxiliary modules | put them in `scripts/lib/` and use **absolute imports** (`from lib.x import y`); **relative imports are forbidden**. This is the **Python** convention; see the next section for other languages |

How to run:

```
run_skill_script(skill="xlsx-report", args=["--input", "in.xlsx", "--min-amount", "1000"])
```

### Referencing files inside the skill directory from a script

The script is launched as `[interpreter, /skill/<script>]` and **the cwd is `/workspace`** — so "files in the skill
directory" and "files in the workspace" are two different path systems; do not mix them:

| Language | Referencing files inside the skill directory | Notes |
|---|---|---|
| Python | `Path(__file__).parent / "lib/x.py"`; modules via `from lib.x import y` | `/skill/scripts` is on `sys.path[0]`, so sibling imports work |
| Node | `require("./lib/x.js")` | resolved relative to **the requiring file**, not the cwd |
| Lua | `dofile(SKILL_DIR .. "/lib/x.lua")`, or set `package.path = SKILL_DIR .. "/?.lua;" .. package.path` before `require("x")` | **plain `require("x")` fails**: the default `./?.lua` is relative to the cwd (the workspace) |
| Shell | `. "$SKILL_DIR/lib.sh"` | `source` follows the cwd for relative paths too |

Always derive `SKILL_DIR` from **the script's own location**; never hardcode `/skill`:

```python
from pathlib import Path
SKILL_DIR = Path(__file__).resolve().parent.parent      # one level above scripts/
```

```javascript
const SKILL_DIR = require("path").resolve(__dirname, "..")   // Node (CJS; ESM uses import.meta.dirname)
```

```lua
local SKILL_DIR = (arg and arg[0] or ""):match("^(.*)[/\\]scripts[/\\]") or "."   -- Lua
```

```sh
SKILL_DIR=$(cd "$(dirname "$0")/.." && pwd)             # Shell
```

The reason: `/skill` only really exists under nsjail / docker. `mode: none` performs no isolation, and the mount table only
translates argv and cwd — a hardcoded `/skill/...` inside the code reports "file not found" when debugging locally yet works
in production, and that kind of divergence is the hardest to diagnose.

Three more constraints:

- **`scripts/` is code, `templates/` / `references/` are data**: both are under `/skill` and both are read-only, but only the extensions in
  `scripts/` (`.py` / `.js` / `.lua` / `.sh`) are treated as executable by `run_skill_script`;
- **`/skill` is read-only**: write every artifact to `/workspace` (the cwd) or a subdirectory — do not try to land them in the skill directory;
- **`.` is lethal in shell**: in `sh`, `. a-file-that-does-not-exist` **terminates the whole script** (it does not report and continue),
  and the symptom is "no output at all, not even an exit code" — a wrong relative path costs more than you expect, so always use the absolute `SKILL_DIR`.

### Permissions for writing `scripts/`

`create_*_skill` **writes only the `SKILL.md` body**; scripts must be written **separately** with the file tools:

| Skill ownership | Who may write scripts | Path form |
|---|---|---|
| Shared (tenant-level) | **super agent only** | `write_file("/skills/<name>/scripts/main.py", …)` |
| Private (agent-level) | the owning agent itself, or the super agent | `write_file("/agents/<yourself>/skills/<name>/scripts/main.py", …)` |

Write gate (the same criterion for both tiers): **the target skill must be in normal status**; **the owning agent** must also be in
**normal status** (deprecated is frozen). The super agent is judged by the same criterion for the **owning agent** — identity
does not relax it.

## How to write SKILL.md

Four fixed sections (level-2 headings `##`), containing only what is necessary:

- `## Purpose` — one sentence on what problem this skill solves
- `## When to use` — when it should be read; the triggers must be concrete and recognizable
- `## How to do it` — steps / rules / templates; this is the main body
- `## Cautions` — easy pitfalls and boundary conditions

Writing requirements:

- **lead with "When to use"** — the agent relies on that section to decide whether to read the rest;
- **write executable instructions** — "run `go test ./...`", not "testing should be done";
- **provide templates and examples** — cheaper in tokens and more usable than long prose;
- **do not write common sense** — only what the model does not know or easily gets wrong;
- **be short**. A skill is knowledge read on demand; the longer it is, the more it costs.

**System skills are always written in English**: `skill-authoring` / `agent-authoring` / `custom-tool-authoring` /
`flow-authoring`, together with their `references/` and `templates/`, are part of the system prompt. Any change to those files
must be written in **English**, whatever language the user speaks. Tenant-built skills follow the user's language.

**The body can reference global variables**: write `${global_vars.<variable name>}` (note `${}`, not the `{{ }}` used in flows);
the body returned by `read_skill` / `use_skill` is expanded by the backend from this tenant's values. The point is to keep
environment-specific constants (endpoint URLs, regions, account names, table names) in the tenant configuration, so the same
skill installed into another tenant only needs the variables changed there.

Four things to watch:

- **only `SKILL.md` is expanded**: scripts in `scripts/`, and files in `references/` / `templates/`, land verbatim, and nobody
  replaces `${global_vars.X}` there — if a script needs an environment value, let it read an environment variable, or have the
  caller pass it in through `args`;
- referencing a variable that does not exist, or **referencing a `secret` variable**, fails outright when the body is read and
  names the variable — it is not a warning; never put secrets into the body: the body enters the model context and run records;
- listing / exporting an asset carries only the reference names, never the values; after installing into the target tenant, that
  tenant's variables of the same name fill them in, and if the names do not match the body cannot be read after installing;
- **`create_*_skill` / `update_*_skill` do not validate references when writing the body** (only the console's file endpoint
  rejects secrets), so a mistake only blows up on the first `read_skill` — reading it once right after creating it is the
  cheapest safeguard.

When unsure how a name is spelled: the super agent can call `list_global_vars` directly (names / types / descriptions only,
**never values**); an ordinary agent does not have this tool (super agent only), so do not guess — ask the administrator for the
exact name.

**When a skill has scripts, the body must cover three things** (this is the only difference between an executable skill and a
document-only one):

1. **what the script does** — one sentence; do not restate the code;
2. **how to call it** — a complete `run_skill_script` example including typical `args`, with the arguments explained in a table;
3. **input / output contract** — which file it reads, where the artifacts go, how failure looks.

**Never paste script code into the body** — the code lives in `scripts/`; the body explains only how to use it.
Pasting code into the body creates a second copy that drifts: change the script, forget the body, and an agent that reads the
body acts on stale code.

## Naming rules

Must match `^[a-zA-Z0-9][a-zA-Z0-9-]*$` and be at most 64 characters:

- letters, digits, dashes; **no** underscores, **no** leading dash;
- lowercase + dashes: `git-pr-flow`, `report-format`;
- recognizable at a glance: `code-review` beats `cr` or `helper`.

## Making a skill a user command (`/name`)

A skill can also be invoked **by the user** from the chat window: typing `/name args` runs it without the model deciding to
pick it. Three frontmatter keys control this; they belong to the skill definition, so they version with it:

| Key | Meaning |
|---|---|
| `user_invocable` | whether `/<name>` appears in the chat window's command menu. Default **off**: a skill stays model-only until someone turns this on |
| `command_dispatch` | `""` (default) = inject `SKILL.md` and let the model work; `tool` = dispatch the arguments straight to `command_tool`, **bypassing the model** |
| `command_tool` | the target tool for `command_dispatch: tool` (for example `local:rpc:daily-report`). A `tool` dispatch without a target is an unfinished draft: the command still runs, but only injects the body |

What follows from those three keys:

- a command is offered only to agents that can already **use** the skill (private + bound, plus whatever their discovery
  setting admits). `/` never reaches a shared skill that `restricted` keeps away from that agent;
- an injected command replaces the message the model sees with the body plus `Arguments: <rest>`, and pre-activates the
  skill's tool dependencies, so the declared tools are available on the very next turn - the model does not need `use_skill`
  again;
- a `tool`-dispatched command receives `{command, commandName, skillName}`: the raw argument string, `"/name"` and the skill
  name. Design the target tool around that shape;
- dispatch bypasses the model, **never the grants**: the tool still has to be injected for that agent, otherwise the command
  fails with a permission error.

Do **not** make a skill a command when it needs understanding, clarification or multi-step judgement. A command is for "one
sentence in, one fixed action out" - a thin wrapper around a tool, not a conversation.

## Which tools to use

**Private** (your own, visible only to this agent) — usable only after **an administrator grants the agent `*_own_skill`**:

| Purpose | Tool | Key arguments |
|---|---|---|
| Create | `create_own_skill` | `name`, `skill_md` **required**; `description` optional |
| Change content / description / rename | `update_own_skill` | `name` **required** (for locating); `skill_md`, `description`, `new_name` as needed |
| Delete | `delete_own_skill` | `name` (or `skill_id`) |

**Shared** (tenant-level) — **super agent only**:

| Purpose | Tool | Key arguments |
|---|---|---|
| Create | `create_shared_skill` | same as `create_own_skill`; after creation only you are granted, so `bind_skill` it to others |
| Change content / description / rename | `update_shared_skill` | same as `update_own_skill`; changes affect **every** agent bound to it |
| Delete | `delete_shared_skill` | same as `delete_own_skill` |

**Explicit ownership** (create an agent's private skill on its behalf) — **super agent only**:

| Purpose | Tool | Key arguments |
|---|---|---|
| Create | `create_agent_skill` | `agent`, `name`, `skill_md` **required** |
| Change content / description / rename | `update_agent_skill` | `agent`, `name` **required**; `skill_md`, `description`, `new_name` as needed |
| Delete | `delete_agent_skill` | `agent`, `name` (or `skill_id`) |

**Shared / on-behalf skills may declare tool dependencies (`tool_deps`, super agent only)**: `create_shared_skill` /
`update_shared_skill` / `create_agent_skill` / `update_agent_skill` take one extra optional argument, `tool_deps` — the list of
tool refs the skill needs (such as `builtin:exec`, `builtin:file`). It is a full replacement: pass `[]` to clear, omit it to leave
unchanged. It **does not create a grant**; it only "lights up" an `allow` grant the host agent **already has**, and it decides
whether `grant_agent_tool` writes `allow` (on-demand injection, saving context) or `required` (always injected). The
`*_own_skill` family **does not accept** this argument (passing it is an explicit error).

**Change a single dependency with the incremental tools (super agent only)**: `grant_skill_tool(name|skill_id, ref)` adds one,
`revoke_skill_tool(name|skill_id, ref)` removes one. Both are idempotent and touch **only that one row** — unlike `tool_deps`,
which is a full replacement (omit = leave unchanged, pass `[]` to clear). The return value carries the complete `tool_deps` after
the change, so there is no need to `read_skill` again.

`ref` follows the same form as `grant_agent_tool`: `builtin:<tool name>` / `builtin:<category>` /
`mcp:<connection>[:<tool>]` / `rpc:…` / `local:rpc[:<endpoint>]`; **custom groups are not accepted** (`custom:*` is rejected).
The target skill must be in normal status (deprecated is always rejected), and its owning agent must be in normal status too
(when deprecated, its private skills are frozen); renaming is not their job — use `new_name` on `update_*_skill`.
Removal does **not** check existence — stale refs left behind by deleted tools or connections can still be cleaned up, and that
is intentional.

> **When a skill ships scripts, `tool_deps` must declare `builtin:exec`** — `run_skill_script` belongs to that category group, and
> without a grant on the host agent the skill cannot run even after it is created (the call is refused explicitly). Declaring the
> dependency does not create a grant: an administrator or the super agent must still grant it with
> `grant_agent_tool(agent, "builtin:exec")`; and **the skill must be bound to the host agent** (`run_skill_script` only accepts
> skills "injected into me": private ∪ bound).

**General**:

| Purpose | Tool | Key arguments |
|---|---|---|
| See what is available | `list_skills` | none |
| Read one in full (**without activating**) | `read_skill` | `name` (or `skill_id`; targeting by id is super agent only); echoes `tool_deps` and visibility |
| Use one (**activates its tool dependencies**) | `use_skill` | `name` (or `skill_id`); returns the same body + `tool_deps`, and its dependencies are injected from the next inference on |
| Bind / unbind (allowlist) | `bind_skill` / `unbind_skill` | `agent`, `skill` (**super agent only**, idempotent) |

`list_skills` lists only the current **visible scope** (private + bound; plus "visible to everyone" shared skills when the agent has
`discovery` enabled), at most 100 per call (when `truncated=true`, narrow the scope as the note suggests); **the super agent's view
is all tenant-level skills** (the allowlist is ignored).

The tool name is the ownership: `*_own_skill` touches only your own private skills, `*_shared_skill` only tenant-level shared
skills, `*_agent_skill` only **the named agent's** private skills; the wrong tool is rejected explicitly (it never silently
changes another scope).
The reverse is equally strict: the `scope` argument **has been removed**, and passing it is an explicit error pointing at the
counterpart tool; `restricted` is accepted only by `update_shared_skill`, and passing it to `*_own_skill` / `*_agent_skill` is
likewise an explicit error.

## Creation procedure (four steps, none optional)

| Step | Action | What happens if skipped |
|---|---|---|
| 1 | `create_shared_skill` / `create_own_skill` / `create_agent_skill` — **writes `SKILL.md` only** | —— |
| 2 | when scripts are involved: use `write_file` to put them under `<skill dir>/scripts/` (**the path must start with `/`**; see the "Permissions for writing `scripts/`" table above) | the body tells people to run a script that does not exist; **creation still succeeds**, and it only blows up at runtime |
| 3 | **smoke test**: run it once with `run_skill_script` right away, and **read the artifact back** to verify | the defect waits until the user's first use |
| 4 | to let others use it: `bind_skill` + `grant_agent_tool(agent, "builtin:exec")` | the skill is in the database, but that agent cannot see it / cannot run it |

**Step 1 does not create the `scripts/` directory** (measured: after creation `list_files` shows only `SKILL.md`), and there is
**no validation at all** — a body saying "run `scripts/main.py`" with no script written still returns success. So steps 2 and 3
are up to you.

**The smoke test must cover at least three things**:

1. the happy path with normal arguments;
2. **reading the artifact back** to confirm the content is right — `run_skill_script` returns `stdout` / `stderr` / `exit_code` /
   `truncated`; exit code 0 **does not** mean the artifact is correct; on error read `stderr` first (when output is too long it is
   cut and `truncated=true`);
3. one boundary case (invalid arguments, a non-existent output directory, empty input, …).

When `stderr` still does not explain it, use `exec` to drop the script into the workspace as a `.py` file, run it standalone,
redirect to a log and read that with `read_file`.

## Cautions

- **Do not write `SKILL.md` into a directory by hand** — there is no row in the database then, `list_skills` cannot see it, and it
  becomes an orphan file. Always go through `create_own_skill` (or `create_shared_skill` for shared, `create_agent_skill` on
  behalf of another agent) / `update_*_skill` / `delete_*_skill`.
- **The body goes into `skill_md`; auxiliary files are written separately**: the `*_own_skill` family writes only the `SKILL.md`
  body. Auxiliary files such as scripts / templates / samples / checklists must be written separately into the skill directory with
  the file tools using the `/` prefix (paths and permissions in "Permissions for writing `scripts/`"). Note that for an ordinary
  agent the `/` prefix **reaches only its own private skill directory**: other agents / skills, tenant-level shared skills and its
  own `AGENT.md` are unreachable (shared skills' auxiliary files can be written only by the super agent).
- **Auxiliary files come in two kinds; do not conflate them**:
  - **passive data** (templates / samples / checklists / corpora) — not read automatically at runtime, i.e. "dead files": either put the key
    content into the body, or state their path and purpose in the body so whoever reads it can open them when needed;
  - **executable scripts** (`scripts/*.py|*.js|*.lua|*.sh`) — **not dead files**: `run_skill_script` mounts the skill directory and
    runs them directly, with the extension deciding the interpreter. Their existence and usage **must be written into the body's
    "How to do it"**.
- **`*_own_skill` is not available to every agent** (the default tool set **does not include** own-skill creation): if you cannot get
  the tool, do not keep trying — either ask an administrator to grant it under "tool configuration", or ask the super agent to create
  a shared skill with `create_shared_skill` and `bind_skill` it to the agent.
- **Renaming moves the whole skill directory**: when `update_own_skill` / `update_shared_skill` receive `new_name`, the directory
  (auxiliary files and subdirectories included) is moved as a whole; you need do nothing.
- **Omitting `skill_md` changes metadata only**; the content stays as it is.
- **`tool_deps` exists only on shared / on-behalf tools** (super agent only): passing it to the private `*_own_skill` family is an
  explicit error. Use `grant_skill_tool` / `revoke_skill_tool` for single changes and `update_*_skill(tool_deps=[...])` for a full
  replacement. Keep three "not equal to"s apart:
  - **declaring a dependency ≠ granting it**: if the host agent has no matching `allow` grant, declaring it injects nothing;
  - **binding ≠ injection**: binding only puts the skill into the prompt index; it produces no tool candidates;
  - **seeing the dependencies in `read_skill` ≠ activated**: dependencies take effect only after `use_skill` (from the next inference
    on) and expire when this run ends.
- **`run_skill_script` needs file-system storage** (`local` / `nfs`): the skill directory has to be mounted into the sandbox, and
  under S3 storage the host path is unreachable, which reports "the skill directory is unreachable on the host" (the script itself is
  fine; do not go changing it).
- **Change and delete shared skills carefully (super agent only)** — other agents may be bound to it and using it; renaming or
  changing content affects every agent bound to it.
- **The `SKILL.md` entry file cannot be deleted on its own**: under root addressing `delete_file` refuses to delete `SKILL.md`
  (deleting it would leave an empty skill "registered in the database without a body", and at runtime a missing body degrades
  silently to empty content). To delete a whole skill use `delete_own_skill` / `delete_shared_skill` / `delete_agent_skill`.
- **`skill-authoring` / `agent-authoring` are system cornerstones: they cannot be deleted or renamed** (body and description remain
  editable). The same applies to the built-in super agent `main` (cannot be deleted / renamed; it can be called by other agents, and
  when invoked it always reads drafts) — this sits outside the status gate; when the error says `protected`, stop retrying.
- **The system prompt carries only name + description**; the body is read with `read_skill`. So `description` must state clearly
  "when I should be read".
- **Self-check after creating**:
  1. is the name valid? Is the right tool chosen (private or shared)?
  2. does `description` let another agent decide whether to read it? Is the body's "When to use" concrete enough?
  3. **are there scripts?** If so: are they written into `scripts/` (not pasted into the body)? Does `tool_deps` declare
     `builtin:exec`? Does the body explain "what the script does / how to call it / input-output contract"? Is the host agent granted
     `builtin:exec`, and is the skill bound to it?
