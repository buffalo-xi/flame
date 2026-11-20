# Agent Authoring Guide

## Purpose

Guide you through creating / updating / deleting an agent and writing a high-quality `AGENT.md`.

## When to use

Read this skill when these intents appear:

- "create an agent (that specializes in X)"
- "build me a poem / joke / chat bot"
- "set me up an assistant for Y"
- "rename / re-describe some agent", "delete an agent", "which agents are there now?"

**Do not create an agent for something done once, or when the user did not ask for a reusable agent.**

## How to do it

### 1. Confirm four things first

| Item | Requirement |
|---|---|
| `name` | Unique; only **letters, digits, dashes** (`^[a-zA-Z0-9][a-zA-Z0-9-]*$`), no underscores, cannot start with a dash, ≤64 characters. Lowercase + dashes, e.g. `poet`, `english-mate`. It is also the **definition domain** directory name (`tenants/{schema}/agents/{name}/`); "workspace" means something else — the runtime domain (session / runs directories), and the two are not the same thing. |
| `description` | One sentence saying what this agent does; it appears in lists and others use it to judge the purpose. |
| `agent_md` | The full AGENT.md (see below). |
| `default_language` | Optional: the agent's default reply language (BCP-47, e.g. `zh-CN`), for an agent that inherently serves one audience. Leave it out for the normal case — the agent then follows the caller / user. It can also be set later with `update_agent(default_language=…)`. |

### 2. Write AGENT.md

Structure it with second-level headings covering: **role, scope of responsibility, behavioural rules, output requirements**. Key points:

- open by stating "who you are and what you are responsible for";
- spell out the **reply language / style / limits**. The default reply language is "follow the user's input"; **do not hardcode Chinese**;
- spell out the **output format** (constraints such as "output only the content" or "only one at a time" must be written in);
- write executable instructions, not platitudes;
- short and precise; avoid piling up general knowledge unrelated to tool capabilities.

**Why you must not hardcode "Chinese"**: tool descriptions, skill bodies, and the capability / sandbox notes injected by the system may be Chinese or English (they are translated in batches when the primary language changes) — that is the **language of the prompt**, not of the reply. The end of the system prompt already carries a general rule: "reply in the language the user writes in"; hardcoding a language in AGENT.md **overrides** it — do so only when this agent inherently serves a single language (e.g. a writing tutor for one language), and state the reason in the body.

**Two ways to fix a language**: an agent that inherently serves one audience can either state it in AGENT.md (see above) or
set the structured default at creation with `create_agent(default_language="zh-CN")`, or later with
`update_agent(default_language="zh-CN")` — this writes `agents.default_language` (BCP-47; an empty string clears it, and
`read_agent` echoes the current value). The structured default is the weaker of the two: a caller passing `_meta.language`
still overrides it for that single call. Its advantage is reach — it also travels into the workflows this agent runs, where
it is readable as the builtin variable `language`.

**System entities are the exception**: `main`'s `AGENT.md` is part of the system prompt and is always written in
**English**, whatever language the user speaks. Tenant-built agents follow the user's language instead.

Example skeleton:

```markdown
# Role

You are a <role>, responsible for <responsibility>.

## Scope

- ...

## Behaviour

- Reply language: follow the user's input
- Style: ...

## Output requirements

- ...
```

**AGENT.md can reference global variables**: write `${global_vars.<variable name>}` (note `${}`, not the `{{ }}` used in flows); the backend expands them from this tenant's values before the document enters the system prompt. The point is to keep environment-specific constants (addresses, regions, account names) in the tenant configuration, so the same AGENT.md installed into another tenant only needs the variables changed there.

Two things to watch:

- Referencing a variable that does not exist, or **referencing a `secret` variable**, fails outright and names the variable — it is not a warning. Secrets are already rejected when the document is saved; documents imported from an asset pack bypass the save-time check and are rejected at load time as well. Do not put secrets into AGENT.md and do not pass them through it: the prompt enters the model context and run records.
- Listing / exporting an asset carries only the reference names, never the values; after installing into the target tenant, that tenant's variables of the same name fill them in. If the names do not match, it installs but does not run; the pre-install check lists what is missing.

When unsure how a name is spelled, call `list_global_vars` first (it returns names / types / descriptions only, **never values**) — do not guess.

### 3. Tools and skills: a new agent can read and write its own area by default

`create_agent` **does not accept** tool arguments. A newly created agent **already comes with a set of tools** (an administrator can adjust them under "Settings → Tool configuration"):

- **Included by default**: `read_file` / `list_files` / `file_exists` / `write_file` / `edit_file` / `delete_file` (its own session workspace only) + `list_agents` / `read_agent` / `list_skills` / `read_skill` / `use_skill` + `call_agent` (who it can call is decided by "call grants"; with no relation configured, calls fail).
  `use_skill` must be among the defaults: the tools a skill declares are injected only after it activates them, and without `use_skill` an ordinary agent has no way to light up `allow` grants (`read_skill` is view-only and carries no tools);
- **Still needs a separate grant**: `exec` and `run_skill_script` (command execution / running a skill's bundled scripts; both belong to the `builtin:exec` group and come with a single grant; high risk), and the three **own-skill** tools (`create_own_skill` / `update_own_skill` / `delete_own_skill`, which only touch **its own private** skills);
- **Unavailable to ordinary agents (super agent only; grants do not help)**: `*_shared_skill` (creating tenant-level shared skills), `*_agent_skill` (creating private skills for another agent), `bind_skill` / `unbind_skill`, agent management, tool and call grants. They are not exposed in "tool configuration" at all — ticking them is impossible (the catalog hides them by identity) and forcing a call is rejected by the execution layer;
- **Pure chat agents** (only answer in the session): **nothing to do**;
- **When tools beyond the defaults are needed**: an administrator grants them under that agent's "Settings → Tool configuration" (`required` always injected / `allow` available once granted / `deny` explicitly excluded), or turns on "unrestricted tools".
  **As the super agent you can also do this directly**: `grant_agent_tool(agent, ref, kind?)` to grant, `revoke_agent_tool(agent, ref)` to revoke, `update_agent(agent, unrestricted=…)` to toggle the switch. `kind` is optional:
  `required` (always injected and resident; never downgraded even if a skill declares it),
  `allow` (available on demand; **a skill must already declare the dependency**, otherwise the write is rejected — such a row would never take effect),
  `deny` (refuse: not injected even if a skill declares it; overrides the existing row for that ref).
  Omit `kind` and the system decides (declared → `allow`, otherwise `required`). One row per ref; changing `kind` overwrites the old row, and the return value says what was overwritten. **Where refs come from: `search_tools(query, agent)` first** — do not write them from memory. It matches keywords against connection names / tool names / descriptions / ref fragments and returns refs you can paste straight into `grant_agent_tool` (`builtin:file` for a whole group, `mcp:fs` for a whole connection, `mcp:fs:read_file` for a single tool), annotating items the target already holds (`granted`). If it finds nothing, do not force a ref: a misspelled connection name only earns `not found`, and `search_tools` also answers "which connections does this tenant actually have".
  **To add a declaration to a skill**, use `grant_skill_tool(skill, ref)` for one and `revoke_skill_tool(skill, ref)` to remove one (or `update_*_skill(tool_deps=[...])` for a full replacement); check what it declares now with `read_skill(skill)`.
  The target must be in normal status (deprecated entities are always rejected, **yourself included**); **adding tools to yourself is allowed** (you are the administrator), and your tool set is recomputed on the next turn of this run — no need to wait for a new session. Before changing anything, call `read_agent(agent)` to see the current state: it returns `tool_grants` (each with `effective` / `pending` / `via_skill` / `note`, explaining whether the row is active now and which skill lights it up), plus `unrestricted` / `discovery`, so you neither duplicate a grant nor revoke something still in use;
- **When skills are needed**: add with `bind_skill(agent, skill)`, remove with `unbind_skill(agent, skill)` (**super agent only**) (also configurable in the console under "Settings → Call grants → Available skills"); only **tenant-level shared** skill names are accepted — private skills need no binding, since they are inherently resident in the owning agent's prompt. `read_agent` echoes the current `skills` (bound) and `private_skills` (owned private skills), and whether the `discovery` / `unrestricted` switches are on;
  `update_agent(agent, discovery=…)` toggles "may it discover unbound shared skills";
  `update_agent(agent, is_supervisor=…)` grants / revokes **super privileges** (on par with the super agent: management tools + cross-domain writes); only a supervisor can do this, and the last supervisor in a tenant cannot be demoted.
  The prompt carries only names + descriptions (just an index); bodies are read on demand with `read_skill`, and **tools are injected only by `use_skill`**.
  Once an agent is deprecated (3), its private skills and its own definition such as `AGENT.md` are frozen (only normal status is writable).
  When you call `list_skills`, you see **all tenant-level skills** (the allowlist is ignored), so there is never a "can edit but cannot list" gap.
- **When it must call other agents**: add a row with `allow_agent_call(agent, target)`, revoke with `revoke_agent_call(agent, target)` (**super agent only**; also configurable in the console under "Settings → Call grants"). A call grant is one row in `agent_relations` — without that row `call_agent` is always refused, and **no agent can bypass it**: even you must write the row before calling someone (and writing it is something only you can do).
  The callee can be any non-deprecated agent (**including a super agent** — when invoked it still has the full management surface and always reads drafts); `read_agent` shows the target's current `can_call` / `called_by`.

So: **never promise in a reply that a new agent has tool capabilities beyond its defaults**; if extras are needed, say honestly that they must also be granted in tool configuration.

**Creating a skill does not bind it, and neither may you bind it on a hunch.** This is the one place where a wrong guess stays silent: an unrequested binding changes which agent carries the skill in its prompt, and the user notices much later.

- The three create tools (`create_own_skill` / `create_shared_skill` / `create_agent_skill`) never bind the skill to a third party. Their return value carries `bound_agents`, the authoritative answer to "who can see it right now": a new **shared** skill lists its creator (that single row is the automatic grant that makes it usable at all), a **private** skill always returns an empty list, because private skills have no binding dimension.
- **Bind only when the user named a target agent** ("give it to the poet", "let the reviewer use it"). Then call `bind_skill(agent=<that agent>, skill=<name>)`. Never bind to yourself, to a guessed agent, or "because this agent will probably need it" - binding to finish the story is exactly the failure this rule forbids.
- **Report the binding state in the reply, always in one of two shapes**: "Bound to <agent>" or "Not yet bound". A bare "created the skill" is not an answer, because the user cannot tell whether the agent can use it. When it is not bound, also say who can see it now (a shared skill: whoever `bound_agents` lists; a private skill: its owning agent only) and that `bind_skill` is how to widen it later.
- The creator row of a shared skill is **not** a binding you made: do not report it as "bound it to you", and do not re-bind it.

### 4. Which directories are visible at runtime

Tools are not limited to a single "workspace" view. The mounts of the three execution tool families are fixed:

| Tool | `/workspace` | Entity directory (**read-only**) |
|---|---|---|
| `exec` | the workspace of this run, read-write, also the cwd | `/agent` = this agent's definition domain (`AGENT.md`, private skills) |
| `run_skill_script` | same as above | `/skill` = that skill's directory |
| `custom__*` (custom tools) | same as above | `/tool` = that tool's directory |

- `/agent` is read-only: `cat /agent/AGENT.md` and reading a private skill's attachments work, **writing does not**;
- **Do not put scripts in the definition domain**: they can run there, but under `mode: none` paths such as `/agent/...` are not translated (only argv and cwd are), which shows up as "file not found when debugging locally, fine in production". Put scripts that must run repeatedly in the skill's `scripts/` and run them with `run_skill_script`; put one-off scripts in the workspace and run them with `exec`;
- None of the three tool families can see other entity directories (other agents, other skills, other custom tools).

### 5. Calling `create_agent`

Arguments: `name` and `agent_md` are required; `description` optional (recommended); `default_language` optional (BCP-47, omit for "follow the caller"; a malformed tag fails the creation instead of being stored); `is_supervisor` optional (default false; the agent is created with super privileges right away — supervisor only, do not hand it out casually: it is the same level as the super agent).

### 6. Editing the draft is not going live

Everything you can change about an agent — `AGENT.md`, the description, the two switches, the default reply language — lives in
its **draft**. Production entry points (MCP / RPC / endpoints / a flow outside a debug run) read the **released current
version**, so a change takes effect only after the agent is released again from its version panel in the console (the super
agent has no release tool of its own):

- **release** freezes the whole definition into a new version (`AGENT.md`, description, `tools_unrestricted`,
  `skill_discovery`, `default_language`) and points production at it;
- **apply** (load) writes an older version back into the draft and leaves production untouched — that is how you continue from
  an old version;
- **rollback** points production at an older version and leaves the draft untouched.

So never say "it is already live" right after an edit: say what changed and that it takes effect at the next release. An agent
that was never released cannot be called through a production entry point at all.

### 7. Self-check after creating

- the name is valid and does not collide with an existing agent (run `list_agents` first);
- the description lets others judge the purpose;
- the output constraints in AGENT.md match what the user asked for;
- when tools / skills are needed, tell the user explicitly where to configure them ("tool configuration" grants, "available skills" bindings); and verify the three-part rule for them to take effect: **the shared skill is bound** + the agent has an `allow` grant for the matching tool + `use_skill` activates it at runtime — miss any one and the tool never appears;
- every skill you created is reported as either "Bound to <agent>" or "Not yet bound" — never bound on a hunch, and never bound to yourself to make the setup look wired up.

## Cautions

- **Do not "delete and recreate" to change configuration**: `create_agent` fails on a duplicate name, while renaming / re-describing / `discovery` / `unrestricted` / `default_language` are all handled by `update_agent`; to change the **AGENT.md body**, write it with the file tools directly — `write_file("/agents/<name>/AGENT.md", full text)` (or a partial change with `edit_file`). Deleting and recreating changes the **ID** and loses bound skills, so avoid it.
- **`update_agent` forbids exactly one thing on "yourself while running": renaming** (`agent` = yourself + `new_name` is rejected — renaming would break the remaining file operations of this turn, a purely technical reason). **Description / `discovery` / `unrestricted` / `default_language` / `is_supervisor` all apply to yourself too**, no need to go through the console; to change your own AGENT.md body use `write_file("/agents/<yourself>/AGENT.md", full text)`.
- **`main` (the built-in super agent) and three built-in skills are protected**: `delete_agent("main")` and renaming `main` are rejected (the error contains `protected agent`); `skill-authoring` / `agent-authoring` / `custom-tool-authoring` likewise cannot be deleted or renamed (body and description remain editable). This is a separate gate **orthogonal** to the "normal status" gate — when you see `protected`, do not retry with other arguments or try to change the status first.
- **As the super agent you can write files across agents / skills**: a file path starting with `/` is resolved from the **tenant root** — `/agents/<name>/AGENT.md`, `/skills/<name>/SKILL.md`, `/agents/<name>/skills/<name>/SKILL.md`. Read the original with `read_file` first. The write gate has two criteria, do not confuse them: **the agent's own definition** (`AGENT.md`, description, etc.) requires that agent to be in **normal status** (deprecated is rejected); **files inside an agent's private skill directory** likewise require the **owning agent** to be in normal status (deprecated is frozen), plus the skill itself being normal. **Being the super agent does not relax this.** Note that the `/` prefix is **not yours alone**: an ordinary agent can also use it to reach **its own** private skill directory (`/agents/<itself>/skills/<skill>/...`, including attachments and subdirectories, through the same two gates); all other cross-domain paths remain yours only. An ordinary agent cannot even read or write **its own `AGENT.md`** (it is outside its reach); only you or the console can change it.
- **Entities still go through meta tools**: create / delete / rename an agent with `create_agent` / `delete_agent` / `update_agent`; for skills there are three sets — `*_own_skill` for **your own private** skills, `*_shared_skill` for **tenant-level shared** skills, `*_agent_skill` for **a given agent's private** skills (the tool name is the ownership; the wrong one is rejected, and `skill_id` pinpoints the target). Do not use file tools to create or delete entity root directories.
- **Retry only for one kind of error**: retry once or twice, unchanged, only when the error text is exactly a platform write failure such as `pq: invalid input syntax for type uuid` (it fails the same way with another name or other arguments). **Do not retry other errors** — duplicate names, invalid names (underscore / leading dash), deprecated targets and insufficient grants are deterministic failures; retrying only wastes time. Report the real error honestly.
- **A new agent only has the tool set above**: `exec` and **own-skill creation** (`*_own_skill`) need a separate grant; while creating **shared** (tenant-level) skills, creating private skills for another agent, skill binding, agent management and tool / call grants are all **super-agent only** — granting them to an ordinary agent does not help. So do not promise capabilities beyond the defaults, and do not treat "go grant it in tool configuration" as a universal answer.
- **Be honest**: after calling, tell the user the real tool return; if you actually wrote something yourself instead of calling a sub-agent, do not claim "I had some agent generate it".
