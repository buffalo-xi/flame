# Directory layout and supervisor agent permissions (nexus)

[中文](workspace-layout-and-supervisor-power.zh.md) | English

> Split the "definition domain" from the "runtime domain" and introduce user / session directories; on that basis give the
> supervisor agent cross-agent / cross-skill super permissions, gated on "the target is in an editable state (`status == 1`)".
>
> **Current state: stages 1, 2 and 3 are all shipped and in effect** — the directories are migrated (`workspace/` → `agents/`,
> `shared-skills/` → `skills/`), the migration script ran to completion and has been removed from the code; `gate.go` (`/`
> tenant-root addressing + status gate), `IsMainAgent`, and `skill_id` targeted lookup in the meta tools are all implemented.
> Admin-class tools were further consolidated into `CategorySuper` (natural group `builtin:super`), produced only for the
> supervisor agent (see D18); afterwards, per D19, skill "create / update / delete" was split into two tool sets: private
> `*_own_skill` (usable by everyone, but **not in the default set**, see D20) and shared `*_shared_skill` (supervisor only);
> then per D20 the "build your own skill" trio was moved out of the default tool set and is now granted per agent by admins.
> A few planned extensions of stage 3 were not done — the list is in §7; open items are in §6.
>
> **D30 (later change)**: the identity predicate moved from `IsMainAgent(name)` to the entity attribute `agents.is_supervisor`
> — `main` is merely the supervisor seeded when a tenant is created, and a tenant can configure further agents with the same
> permissions. The migration script is `sql/agent_supervisor.sql`; wherever the text below says "supervisor only", the predicate
> is always read from the `agents.is_supervisor` attribute.

---

## 1. Background

Two threads, and **they must be done in order** (done the other way round, `Context.Workspace` gets changed twice and one round
is wasted):

1. **Directory layering** (the foundation): split "definition (`AGENT.md` / private skills)" from "runtime (artifacts produced
   while working)", and introduce user / session directories
2. **Supervisor agent permissions**: let the supervisor change any agent / skill, with the gate = the target is in an editable
   state

### 1.1 Pain points before the change

One directory carried two responsibilities:

```
tenants/{schema}/workspace/{agent}/
├── AGENT.md                ← definition: belongs to the agent, shared across users and sessions
├── skills/{skill}/SKILL.md ← definition: same as above
└── assorted artifacts / temp files  ← runtime: belongs to "one session", same lifetime as one execution
```

Consequences (all have actually happened or are reproducible):

- users A and B using the same agent → **they see each other's files** (bleed-through)
- the previous session's artifacts are still lying there → **cross-session pollution**
- there is no way to "clear one session's stuff"
- the supervisor's permission (changing someone else's **definition**) and "working in its own playground" share one path space,
  so the gate criteria get mixed up
- the supervisor could only change another agent by "delete and recreate", and after recreating **the ID changes**

### 1.2 Path facts before the change

| Item | Location / value |
|---|---|
| Storage root | `storage.root_dir` (local / nfs use it as the root; s3 uses `storage.s3.bucket` + `prefix`) |
| Definition directories | `tenants/{schema}/workspace/{agent}/`, `tenants/{schema}/shared-skills/{skill}/` |
| File tool root | `Context.StoragePath(rel)` = `tenants/{schema}/workspace/{me}/{rel}` |
| exec sandbox | `Mounts: [{Host: Context.Workspace, Guest: "/data"}]`, cwd = `/data` |
| `Context.Workspace` | the **host absolute path** from `ResolveWorkspace(rootDir, schema, name)` |
| session | **only a DB row, no directory**; and `session_id` **was not passed into the runtime** |

---

## 2. Settled decisions

| # | Decision | Notes |
|---|---|---|
| D1 | The supervisor may modify the files and attributes of **any** other agent / skill | the original requirement |
| D2 | Gate: the target must be `status == 1` (normal); deprecated (3) is rejected | the predicate always goes through `models.IsModifiable`, never a literal `status == 1` |
| D3 | The supervisor **cannot change status** | otherwise it could unlock itself and D2 would be void; status switching is admin-only |
| D4 | Reads are unrestricted; only **writes** are gated | deprecated entities can be read too |
| D5 | ~~The supervisor cannot change its own tool grants~~ **Revised**: the supervisor **can change its own tool grants and switches** (it is the administrator). The boundary now rests on `status` — a deprecated (3) agent (including the supervisor itself) is refused all writes, and **changing status remains admin-only** (D3), so self-unlocking is impossible. Only one self-modification ban remains: **it cannot rename itself** (subsequent file operations in this round would break — a technical constraint, not a permission). After a self-change, `Context.MarkToolsDirty()` marks it stale and the tool set is recomputed on the next turn of this run, no need to wait for the next session | The old "prevent self-escalation" rule did not stop the real threat (the goal of prompt injection is precisely to make it modify itself), yet it turned "give the supervisor a custom tool and verify it" into a support ticket to an admin. The marginal risk of opening it is small: the supervisor already carries `builtin:exec` (arbitrary commands inside the sandbox), so the tool-surface increment is far smaller than the data-domain / sandbox boundary |
| D6 | File tools judge by **entity directory** (revised in stage 3): any file / subdirectory **inside** an entity directory is writable by the supervisor (subject to the status gate); an entity's **creation / deletion / rename / metadata** go through meta tools | replaces the original "the whole `skills/` tree is read-only"; the orphan-prevention boundary moves from "file type" to "entity existence" — writing a non-existent entity → rejected |
| D7 | Rename: `workspace/` → `agents/`, `shared-skills/` → `skills/` | |
| D8 | Add `users/{uid}/files/` | user uploads / artifacts kept across sessions |
| D9 | Add `users/{uid}/sessions/{sid}/`, **without an agent dimension** | `call_agent` sub-agents share the same playground; flow sessions can be reused |
| D10 | An agent **cannot write its own definition domain** | definitions go through meta tools only. **The skill subdirectory was narrowed by D23**; `AGENT.md` still belongs to meta tools (**reads** were opened up by D32) |
| D11 | exec mounts: `/workspace` ← runtime working directory (rw, cwd); `/agent` ← its own definition domain (ro) | logical paths inside the sandbox, mapped from already-computed host paths |
| D12 | The supervisor's exec **does not mount `/tenant`** | it would bypass the D2 gate, and a black-box shell could read the whole tenant without authorization |
| D13 | Deleting a session deletes the directory (`DeletePrefix`); `Create` creates the directory (S3 writes `.keep`) | quotas were added later (see the session-quota section); retention is still open |
| D14 | Old data is **not migrated** | the only existing artifact was `workspace/poet/sfds` (empty file), which can simply be deleted → no need to decide a `uid` |
| D15 | Paths: only the supervisor supports a `/` prefix = resolved from the **tenant root** (three-domain namespace) | normal path semantics unchanged, backward compatible |
| D16 | `main`, `skill-authoring`, `agent-authoring` are on the **delete/rename protection list**: cannot be deleted or renamed (other fields may change) | **orthogonally stacked** on the D2 status gate; implemented as `models.IsProtectedAgent` / `models.IsProtectedSkill` |
| D17 | The identity predicate `IsMainAgent` and the delete/rename protection list `IsProtected*` are **decoupled** | the former is unique and drives super permissions and default tools; the latter may be multiple and only governs delete/rename. They currently hold the same value `{main}`, but the code splits them into `mainAgents` / `protectedAgents` |
| D18 | Admin-class tools (`create/update/delete_agent` + the skill binding tools `bind_skill` / `unbind_skill`) are consolidated to **supervisor only**: new category `CategorySuper` (natural group `builtin:super`), moved out of `builtin:agent` | two layers: the grant catalog does not produce them by identity (a non-supervisor cannot even pick them, and the API rejects them) + a hard gate in `ExecuteTool`. Delete the redundant `preset:agent-manage` / `preset:skill-manage` (their members coincide exactly with `builtin:super`) |
| D19 | Skill "create / update / delete" is **split into two tool sets by ownership**: private `create/update/delete_own_skill` (category `skill`, usable by everyone, only touches its own private skills) + shared `create/update/delete_shared_skill` (category `skill-admin`, supervisor only); the tool name is the ownership, and the `scope` parameter and `owner` targeting are **retired** | the category answers "who may use which tool"; space ownership is enforced separately by `skillManager.checkSkillScope`, and the two are orthogonal and both required. `*_own_skill` is an ordinary tool (category `skill`, usable by everyone) but **does not enter** the default set (see D20). Old refs are renamed by `sql/skill_own_tool_rename.sql` |
| D20 | The "build your own skill" trio (`create/update/delete_own_skill`) is **moved out of the default tool set**: a new agent does not get them, admins grant them per agent explicitly (materialised as `required` on grant) | Building a private skill turns the agent's own prompt into a writable surface — a `name` / `description` written there is carried in the system prompt from the next call on, so input-induced writes become a **persistent injection**. `required` is always injected and is not narrowed by skill dependencies; the cost is that revoking means deleting the whole row (`revoke_agent_tool`, or a D29 `deny` override), not narrowing part of it |
| D21 | The supervisor may grant tools to **any** agent (including itself): `grant_agent_tool` / `revoke_agent_tool` (category `tool-grant`) + `update_agent(…, unrestricted)`; **the write kind (`required` / `allow`) is assigned automatically by the system, not chosen by the model**; `deny` and explicit `kind` were added by D29; no full replacement form | The goal is for the supervisor to take over "tool configuration", provided the target is `status == 1` (including itself — changing status is admin-only, so no self-unlocking, see D3 / D5 revision). Automatic rather than model-chosen: the model cannot see each skill's `tool_deps` (they live in `skill_tool_grants`, not in the `SKILL.md` body), and a wrong guess means "wrote `allow` but it never takes effect" or "should be injected on demand but sits in the context permanently". The criterion is the same as `Resolver` (both expand to **leaf tool refs** and intersect): a skill declares it → `allow` (injected on demand, saving context); nobody declares it → `required` (`allow` is only a candidate filter, and with no candidates it is a no-op). Cost: an `allow` grant hangs on the skill binding, so unbinding makes it **silently ineffective** — hence `read_agent` echoes each grant's effective state and the skill it comes from, making the uncertainty visible |
| D22 | The supervisor can also manage **call grants** and **tenant-level skill visibility**: `allow_agent_call` / `revoke_agent_call` (category `tool-grant`) + `update_shared_skill(…, restricted)`; call grants **may let the supervisor add outbound edges to itself**; no full replacement form | A call grant is one row in `agent_relations` — `caller.go` treats every agent alike (**no row means denied, the supervisor gets no exemption**), so "the supervisor can call a sub-agent" requires really writing the row, otherwise a freshly created agent cannot be called. Allowing the supervisor to add outbound edges to itself: it extends "whom the supervisor may call", which is a different thing from the tool surface in D5; the reverse (target = supervisor) is refused at the tool layer (`caller.go` hard-refuses calls to the supervisor). Skill visibility only gets a **switch**: the allowlist is `agent_skill_bindings`, maintained by `bind_skill` / `unbind_skill`, so **cannot** be done with `SetVisibility` (it clears and rebuilds the allowlist) — hence a new `SetRestricted` that changes one column. A shared skill is narrowed by default when created (`restricted=true` + allowlist containing the creator), so `create_shared_skill` needs no such parameter |

| D23 | **Root addressing for non-supervisors (own skill directory)**: in an editable state a `/` prefix grants access to **its own private skill directory** `agents/{self}/skills/{skill}/**` — read any file, write / modify / delete any file and subdirectory; everything outside the allowlist is rejected. `AGENT.md` and other entities remain unreachable (D10 unchanged). Reads and writes **share one allowlist**, but reads do not query the DB and have no status gate (D4), while writes pass a double gate of "its own `status == 1`" + "the skill is registered and `status == 1`" | Motivation: the old rule treated a private skill as a "single-file knowledge document", so a non-supervisor changing its own skill could only edit the `SKILL.md` body (`update_own_skill`), while the supervisor and the admin console (`admin/skills/files.go`) had long been able to manage a whole skill directory. Opening this cell aligns the three. Implementation: `checkReadOwn` / `checkWriteOwn` in `gate.go`, with parsing and authorization split by `parseRootAddr` (the write side does not reuse the read side, so error copy does not leak the wrong criterion). **Deliberately not opt-in**: `write_file` / `edit_file` / `delete_file` are already in the default tool set, and the only switch is the agent's `status`. The **deletion** protection for `SKILL.md` was also extended to file tools (same criterion as the admin side's `isSkillEntry`) — deleting the entry file leaves a skill that is registered in the DB but has no body, and the runtime silently degrades to empty content when it cannot read the body. **Owning-agent status gate**: when writing an agent's private skill, besides the skill's own status, the **owning agent** must also be in a normal state — private skills under a deprecated agent are frozen entirely (reads unaffected, D4). Four places share the criterion: `checkWriteOwn` (judges itself), `checkWrite` (the supervisor path judges the owner), `skillManager.checkOwnerAgent` (the meta tool trio), and the admin side (`admin/skills` create / update / delete / visibility / status switch + the three write endpoints in `files.go`) |

| ~~D24~~ | **Revoked**: the original decision added `agents.locked_skill_edit` (whether a locked agent still permits editing the private skills it owns). Agents / skills no longer have a "locked (2)" state (three states → two: 1 normal / 3 deprecated), so the switch lost its meaning and was removed wholesale: the column, `models.SkillWritableByAgentStatus` / `CanToggleLockedSkillEdit`, and `PUT /admin/agents/:id/locked-skill-edit` are all deleted. Now "may the owning agent write the private skills it owns" has exactly one criterion — `models.IsModifiable(status)` (normal state only) — and the four judgement sites (`checkWriteOwn` / `checkWrite` / `checkOwnerAgent` / `ensureAgentModifiable`) must still share it | The reasoning matches workflows: production freezing is already expressed by the version model (editing a draft does not affect production, only releasing does), so the "break production by editing" the lock was meant to prevent cannot happen; what remains is only "forbid changing the definition", so after a problem you must unlock before fixing, and the moment you unlock the definition is editable again — the risk comes right back. Migration: `nexus/sql/agents_skills_drop_locked_status.sql` |
| D25 | **The supervisor's tool set comes from exactly the same source as any other agent**: resident = the `required` rows in `agent_tool_grants`. The core set is defined by `models.SupervisorCoreGrantRefs` (6 **groups**, expanding to 19 tools), materialised by `CreateAgent` (supervisor branch) and backfilled for existing tenants by `sql/main_core_tool_grants.sql`. The old "everything is on in code, the grant table only answers what extra was given" is deleted | Motivation: what the supervisor holds was known only to the code — not queryable from the DB, not adjustable per tenant. **Groups** rather than single tools: a group is atomic, so deleting a whole class is visible; deleting one single-tool row (e.g. `read_file`) reports no error and just silently loses a tool — a pit that was actually stepped in. **Fuse**: when the supervisor has **zero grant rows**, or has **not yet materialised the core set** (`MainCoreMigrationPending`: no row for the `builtin:agent-admin` group), it falls back to everything-on and logs an Error (**2026-11: after the migration completed this predicate was deleted, leaving only the zero-row one**). Runtime and the admin preview share the same predicate |
| D26 | `browser` is **not resident**; a tenant that needs a browser grants it explicitly as `required` (not `allow` — it follows no skill, so `allow` would be a no-op) | The four reasons are in §2.4. It is not in the baseline; whoever needs it adds it (the supervisor can add it to itself too, D5 revision), but once added it is resident as `required` and the large 14-action schema occupies budget constantly, so the advice is to add it per tenant temporarily and reclaim it with `revoke_agent_tool` afterwards rather than leaving it in core by default |
| D27 | The admin surface is split from the single `CategorySuper` into four groups: `agent-admin` / `skill-admin` / `tool-grant` / `custom-tool`; `supervisorOnlyCategories` is the single criterion for "supervisor only"; `CategorySuper` is kept as the compatibility expansion of `builtin:super` (the union of the four groups) until existing data is cleared | A single group cannot do "grant on demand" — either all 19 or none. After the split `agent-admin` is resident (bootstrap: without the ability to create agents nothing else can be brought up), and the other three groups activate on demand with skills |
| D28 | **"Use it and it is injected"**: after a successful `use_skill`, the tool dependencies that skill declares take effect in later turns of this run (`Context.LoadedSkills` + `refreshToolDefs`); they disappear with the Context when the run ends. **A binding only decides catalog visibility and produces no tool candidates**; `read_skill` degrades to pure viewing. The three `*-authoring` skills are **not** resident-bound to the supervisor | Skill dependencies used to honour only "already bound" (persisting across runs), while `read_skill` fetched the body without tools — so either 17 tools hung around permanently (no context saving) or the model had to do `bind_skill`/`unbind_skill` in two steps (and the chain broke within a run, because the tool set is computed once at run start). Replacing "already bound" with "already read" would turn "just want to read the doc" into activation (large schema eating budget, hitting the 50 cap, widening the mis-selection surface). Splitting into read / use decouples viewing from activation: body and tools both exist only in this turn and reset across runs. Not an escalation: candidates are always narrowed by `allow` |

| D29 | `grant_agent_tool` gains an optional `kind` (`required` / `allow` / `deny`); when omitted the system still decides automatically. `revoke_agent_tool` echoes the `kind` of the deleted row; `deny` rows in `read_agent` are no longer mislabelled pending | D21's "let the model not choose" fixes the default path but leaves two holes: ① there is no `deny` entry — "the skill declares it but I do not want it" could only be done from the admin console, and under on-demand semantics `allow` only lights up after `use_skill`, so `deny` is the only brake that can block that road too; ② automatic assignment downgrades a basic tool that should be resident (e.g. `read_file`) to `allow` when some skill declares it, so the agent cannot even read a file without `use_skill` first. Hence explicit `required` to pin residency and explicit `deny` as a brake, while `allow` still cannot be written by hand (rejected outright when no skill declares it, avoiding a dead grant that never takes effect). Each ref keeps one row (`UpsertAgentGrant` overwrites by ref), changing `kind` overwrites the old row, and the return value echoes "what the old row was" — otherwise the caller would think `deny` stacks. Not an escalation: the `tool-grant` group is still supervisor-only, and `deny` removes power |
| D30 | The identity predicate moves from **name** to **attribute**: `agents.is_supervisor` (`sql/agent_supervisor.sql`). `models.IsMainAgent(name)` is **deleted** in favour of entity-field judgement (`tools.Context.IsSupervisor`, the `isSupervisor` captured in each manager's constructor); `main` is merely the supervisor seeded at tenant creation, and a tenant may configure several. External fields are renamed accordingly: `ToolInfo.MainOnly` → `SupervisorOnly` (JSON `main_only` → `supervisor_only`), `toolgrant.NewCatalog(..., isSupervisor)` | The name predicate pinned "who is the over-privileged agent" to the single `main`, which could neither be renamed nor duplicated. After the change: ① granting goes through `update_agent(is_supervisor=…)` (category `agent-admin`, supervisor only) and the same field in the admin `PUT /admin/agents/:id`; ② **demoting the last supervisor of a tenant is refused by the repository** — `SetSupervisor` locks all supervisor rows with `SELECT … FOR UPDATE` in the same transaction before counting, so two concurrent demotions cannot both pass and leave the tenant permanently without an admin surface; moreover `main` as a **seeded asset can never be demoted** (`models.CanDemoteSupervisor`) — it can be neither renamed nor deleted, and demoting it would leave a dead corner occupying the fixed entry point with no permissions and no way back; ③ delete protection still only looks at the name list (`IsProtectedAgent`, which contains only `main`) — it protects "a seeded asset that has no recovery path once deleted", orthogonal to super permissions, so a newly created supervisor can be deleted and renamed |
| D31 | The supervisor attribute is **not** folded into the full-field overwrite of `UpdateAgent`; it is a separate `SetSupervisor` (repository) + an explicit branch in the admin `PUT /admin/agents/:id` + the meta tool `update_agent(is_supervisor=…)` | Once a full-field UPDATE carries this column, any caller that "only changes the description" and forgets to fill it back silently resets the super permission to zero. Attribute changes must be explicit single-column operations (the same approach as `SetUnrestricted`). The target must be in a normal state (deprecated does not accept attribute changes), the same criterion as D2; when the target is itself, mark stale with `Context.MarkToolsDirty()` and the tool set is recomputed on the next turn of this run |
| D32 | The **read** gate for non-supervisors is relaxed from "only its private skill directory" to "its own definition directory `agents/{self}/**`" (including `AGENT.md` and attached files); the **write** gate is unchanged (still only `agents/{self}/skills/{skill}/**`) | Sharing one allowlist for reads and writes was self-contradictory: you can edit `skills/x/SKILL.md` (which is also a prompt) but cannot read your own `AGENT.md`. The correct reason for the read/write asymmetry is not "prevent prompt self-editing" but **attribution** — writing `AGENT.md` is an identity change and must be made by a third party (a human or the supervisor), otherwise "who produced this draft" cannot be answered and the version model's premise of "no self-release" is gone; reads have no such problem, and they let an agent say which rule set it is using when it explains or diagnoses itself. The implementation adds one clause to `checkReadOwn` (`kind == "agent" && name == c.AgentName`); `checkWriteOwn` and `resolveWrite` are untouched (`TestResolveWriteOwnAgentMDStillDenied` guards it) |

### 2.1 Why D12 (no `/tenant` mount for the supervisor's exec)

1. **Duplicate capability**: through file tools (the Storage layer) the supervisor can already read and write all three
   domains, **with gates**.
2. **A writable `/tenant` would void D2**: one `echo ... > /tenant/agents/poet/AGENT.md` bypasses `IsModifiable()`.
3. **A read-only `/tenant` still risks unauthorized reads**: the supervisor is driven by an LLM, so user A's prompt can induce
   it to `cat` user B's session files; exec is a black-box shell and a single `find` scans the whole tenant.
4. **The real need is "run commands in my own playground"**: `/data` already covers it.

If the need ever materialises, open it minimally per call: `exec(command="npm run build", agent="poet")` → mount only
`agents/poet` extra (supervisor only + a `status == 1` check).

### 2.2 Why D18 (admin-class tools only for the supervisor)

"Create / update / delete an agent" and "bind a skill to whoever should use it" are system cornerstones and should belong only
to the supervisor (the skill bodies themselves are covered in §2.3).

Implemented as **three layers**, all of them required:

1. **Ownership consolidation (category)**: `create/update/delete_agent`, the shared-skill `create/update/delete_shared_skill`
   and the skill binding tools (`bind_skill` / `unbind_skill`) are tagged `Category = CategorySuper`, naturally forming the
   `builtin:super` group, and are removed from `builtin:agent` / `builtin:skill` — they appear only in their own dedicated
   group.
2. **The grant catalog is generated per identity**: `toolgrant.Catalog` carries an `isSupervisor` view; for a non-supervisor the
   whole `builtin:super` group is not produced (custom / preset groups whose members are filtered out disappear as well). They
   **cannot be selected at all** on the grant page. `SetAgentToolGrants` validates existence against a catalog with the same
   criterion → a non-supervisor submitting those refs is **rejected** with `ref not found or has no tools`.
3. **A hard gate at the execution layer (defence in depth)**: `ExecuteTool` refuses `IsSupervisorOnlyTool(name) &&
   !IsMainAgent(c.AgentName)` outright — even if a grant was misconfigured or historical data lingers, a non-supervisor cannot
   execute it.

**The skill side is the exception**: `agent-authoring` / `skill-authoring` need to **declare** these tools as dependencies, and a
skill dependency is only "declared as needed", not a grant. So the dependency picker in `Skills.vue` stays fully visible, and
these tools merely get a "supervisor only" badge saying "binding it to a non-supervisor has no effect".

### 2.3 Why D19 (skill create/update/delete split by ownership)

There are two ways to "let an agent use a skill": create a **private** skill for it, or create a **shared** skill and
`bind_skill` it. Only the latter is right — the binding is the single source of truth for "who is using it", can be unbound at
any time, and its current state is visible in `read_agent`; creating a private skill on someone else's behalf decouples
ownership from usage rights and makes the whole binding system pointless. Hence:

1. **Shared skills belong only to the supervisor** (`*_shared_skill` tagged `CategorySuper`) → it reuses D18's three layers
   directly, with no new gate;
2. **Private skills are open for every agent to build its own** (`*_own_skill` tagged `CategorySkill`). The category only
   answers "who may use which tool"; "which space may be touched" is enforced separately by `skillManager.checkSkillScope`:
   own may only touch **its own private** skills, shared only **tenant-level** ones. They are orthogonal — the own series is
   available to everyone, and an agent with discovery enabled can "see" shared skills, so without this check a non-supervisor
   could edit a shared skill through the visibility path;
3. **Two tools instead of a `scope` parameter**: the tool name is the criterion, and picking the wrong tool is **explicitly
   rejected** with a hint about which one to use; `scope` is an implicit default, so a model that forgets to pass it silently
   creates into the other space (which is exactly what `create_skill` looked like before the change). The risk of
   mis-selecting between similarly named tools is suppressed on four levels: the tool name (`own` / `shared` symmetry), the
   tool description, the decision table in `skill-authoring`, and explicit errors on the old parameter fields;
4. **`*_own_skill` does not enter `DefaultAgentTools`** (D20): building a private skill makes the agent's own prompt a writable
   surface, so it is not granted by default; an agent that really needs it is granted explicitly by an admin under "tool
   configuration", materialised as `required` — `required` is chosen to avoid the pitfall of `allow` being narrowed by skill
   dependencies (otherwise the self-build ability would mysteriously vanish as soon as the agent binds a skill that declares
   tool dependencies). Cost: `deny` has no effect on `required`, so reclaiming it means a **full replacement** in the admin
   console.

The rename cost: the tool name is part of the grant ref, so the existing `builtin:{create,update,delete}_skill` rows in
`agent_tool_grants` / `skill_tool_grants` / `tool_group_items` are rewritten together by `sql/skill_own_tool_rename.sql`,
otherwise they become dangling references and the tools silently disappear.

### 2.4 Why D25 / D26 (the supervisor's tools move from "code privilege" into "the grant table")

**Before**: with no grants, `buildToolDefs` fell back to `toolgrant.BuiltinTools()` (all 37) for the supervisor, and
`resolveGrantedTools` contained a block saying "the supervisor **always** owns every built-in tool, the grant table only answers
what extra was given". Three consequences:

1. **Not auditable**: `select * from agent_tool_grants where agent_id = <the supervisor's id>` showed only the 11 old default
   seed rows, with no relation to the 37 tools it actually had;
2. **Not adjustable**: removing `shell` from one tenant's supervisor meant changing code and shipping a release;
3. **Split semantics**: the same concept (which tools are available) used two different mechanisms for the supervisor and for
   other agents.

**After**: the supervisor's resident set = the `required` **group** rows materialised from `models.SupervisorCoreGrantRefs`
(6 groups / 20 tools):

| Group ref | Members | Resident | Notes |
|---|---|---|---|
| `builtin:file` | 6 | ✅ | read / write / list / delete / exists |
| `builtin:exec` | `exec`, `run_skill_script` | ✅ | one-shot process |
| `builtin:shell` | `shell` | ✅ | resident session, schema with only 3 parameters |
| `builtin:agent` | `list_agents`, `read_agent`, `call_agent` | ✅ | read-only + collaboration |
| `builtin:skill` | `list_skills`, `read_skill`, `use_skill` | ✅ | see / use skills: `read_skill` gives the body + `tool_deps` (no activation), only `use_skill` activates dependencies |
| `builtin:agent-admin` | create / update / delete agent + `bind_skill` / `unbind_skill` (5) | ✅ | **the supervisor's bootstrap ability** |
| `builtin:skill-own` | `*_own_skill` (3) | ❌ | follows `skill-authoring` |
| `builtin:skill-admin` | `*_shared_skill` + `*_agent_skill` (6) | ❌ | follows `skill-authoring` / `agent-authoring` |
| `builtin:tool-grant` | `grant/revoke_agent_tool`, `allow/revoke_agent_call`, `search_tools` (5) | ❌ | follows `agent-authoring` |
| `builtin:custom-tool` | `*_custom_tool` (4) | ❌ | follows `custom-tool-authoring` |
| `builtin:flow-admin` | `create/list/read/delete_flow` (4) | ❌ | follows `flow-authoring` |
| `builtin:browser` | `browser` (1) | ❌ | follows no skill (see below) |

**Why groups rather than individual tools**: a group is atomic — deleting one by mistake loses a whole class, visible at a
glance on the panel; whereas deleting `read_file` from 33 single-tool rows reports **no error at all** and just silently loses
one tool (a pit that was actually stepped in). The cost is that a tool added to a group automatically enters core, which is
exactly the semantics "core = the system capability baseline" should have.

**The two things not in core**:

- `browser` (D26): ① it says itself "if exec can get the content, do not open it", and presence in the tool list is the
  **strongest** usage hint; ② its schema is the largest by far (14 actions / 18 parameters); ③ it is the only combination of
  "goes out to the network + reads arbitrary pages + carries a login state (cookies persist across sessions)"; ④ it is
  inconsistent with the system's own criterion — `DefaultAgentTools` already deems a browser non-standard.
  **Note it cannot be `allow` on demand**: `allow` is only a filter over candidates, and candidates come from "dependencies
  declared by skills activated in this turn"; a browser follows no skill ⇒ candidates are always empty ⇒ `allow` is a no-op
  (`Degraded` in `resolve.go`). If you want it, add a `required` row (the supervisor can add it to itself, D5 revision) and
  delete it afterwards.
- `*_own_skill`: the supervisor uses `*_shared_skill` / `*_agent_skill`; private skills are meaningless for a global
  administrator (same lineage as D20).

**How on-demand groups take effect ("use it and it is injected")**: candidates = **skills activated with `use_skill` in this
turn** (valid for this run). A binding only decides that the skill appears in the system prompt's catalog (that one line with
name + description) and produces no candidates at all.

```
use_skill("custom-tool-authoring")     → body enters the context + recorded in Context.LoadedSkills
                                       → the tool set is recomputed on the next LLM call
                                       → custom-tool's 4 tools appear in the schema
  ...working...
(run ends, tools and body disappear together)

read_skill("custom-tool-authoring")    → body + tool_deps (display only), not registered, no tools appear
```

Key implementation:

| Location | Role |
|---|---|
| `tools.Context.MarkSkillLoaded` | registers after a successful `use_skill` + marks stale, **carrying a snapshot of the dependency declarations** (`read_skill` is pure viewing and does not register) |
| `agentImpl.refreshToolDefs` | recomputes `toolDefs` **and** `AllowedTools` (the two must share one source) |
| `toolgrant.Resolver.Resolve` | candidates prefer `VisibleSkill.Deps` (the declaration snapshot registered at activation); only when nil does it fall back to `skill_tool_grants` |
| the top of the `Run` loop | recompute only when `ConsumeSkillsDirty()` is true — zero extra cost if no skill was activated |

Key boundaries:

1. **Valid within the run only**: reset across runs (`core.RecordsToMessages` only backfills user/assistant text, tool
   round-trips do not enter history), so it never becomes "every later turn carries them". This is far cheaper than plan A
   (`bind_skill` writing to the DB, permanently in effect).
2. **The cap is unchanged**: candidates are always narrowed by `allow`; activating a skill can only light up groups an admin has
   already granted, never anything outside the grants.
3. **Viewing ≠ activation**: `read_skill` returns the body + `tool_deps` (the dependency list, **display only**) and no tool
   appears because of it; to get tools you must `use_skill` (which returns the same body and `tool_deps` + registers the
   activation). Showing dependencies is not an escalation: activation results are always narrowed by `allow`, so seeing
   `builtin:exec` does not light up anything ungranted.
4. **Binding ≠ candidate**: a binding only decides that the skill appears in the system prompt's catalog (the name +
   description "comment"), producing no tool candidates — otherwise binding a few authoring skills would be equivalent to
   making their 17 tools permanently resident in the context.

If one link in the chain is missing it silently does not take effect: `required` has no such group → `allow` has the group →
some skill **activated in this turn** declares it → candidates ∩ allow. `read_agent`'s `effective` / `pending` / `via_skill`
show whether each grant is actually lit and which skill must be `use_skill`ed for it to light up.

**In production runs (`DefinitionMode=current`) the "which dependencies does the skill declare" part of this chain reads the
frozen snapshot of the current version** (`skillMetaReader`, the same implementation as the resident catalog / `read_skill`):
candidates honour only the snapshot, and both `grant_agent_tool`'s automatic kind and `read_agent`'s `via_skill` judge by it —
adding or removing a dependency in the draft never lights up or hides a tool in production. Display (`tool_deps`) and effect
share one source, which is the whole point of the "freeze metadata, freeze deps too" change.

> **2026-11 update**: the existing-tenant migration is complete; `MainCoreMigrationMarker` / `MainCoreMigrationPending` have
> been removed from the runtime (`agent_impl.resolveGrantedTools`) and from the admin preview, and `onlyLegacyDefaultGrants`
> plus the preview warning "please run sql/main_core_tool_grants.sql" were removed with them. All that remains is the
> "**zero grant rows** ⇒ fall back to everything on" fuse (bootstrap safety, kept long term). The argument about the
> single-group marker below is kept as a historical record — it documents why that choice was made at the time.

**The fuse (I1)**: the supervisor with zero grant rows ⇒ fall back to everything on. This is not a leftover compromise but
**bootstrap safety** — the tenant template `tenant_template.sql` contains no agent rows, and the supervisor is created by the
admin console afterwards; if the backfill script was forgotten, leaving the supervisor disabled would mean the whole tenant
loses the ability to create / grant anything, which is harder to recover from than a brief over-privilege (it is a
full-privilege identity to begin with). The predicate is a single one: `models.MainCoreMigrationPending(refs)` is true, i.e.
the supervisor **has no row for the `builtin:agent-admin` group**.

Why a single-group marker rather than "any core group" or "at least 6 rows":

- counting rows: an admin may legitimately delete one group (e.g. no `shell`), and the moment they do the predicate fails;
- any core group: under the old criterion `builtin:file` / `builtin:skill` may have been granted individually, which would be
  misread as "already migrated", so the supervisor would be resolved from the table and lose all admin abilities;
- `agent-admin`: under the old criterion **no path** granted this group alone to the supervisor (admin tools were then one
  whole `CategorySuper`, the admin console hid built-in classes from the supervisor, and neither `preset:default` nor
  `DefaultAgentTools` contained admin tools).

The actual existing t0 supervisor was exactly the shape "only one `plugin:web_search` row" — precisely the shape both of the
first two predicates would miss.

**Trade-off: `deny` no longer applies to the supervisor's core.** Core rows are `required`, and the general semantics of
`required` is "always injected, `deny` does not move it" (see `toolgrant/resolve.go`). To reclaim a whole class, **delete that
group's row** — the revocation then also lives in the data instead of hiding in a counter-directional grant row.
`sql/main_core_tool_grants.sql` therefore uses the single-group marker to decide "already migrated" and **will not** add back a
group you deleted.

**Rollout order (mandatory)**:
1. `sql/main_core_tool_grants.sql` — clear the old per-tool core rows, write the 6 group rows
2. `sql/supervisor_on_demand_groups.sql` — the supervisor's 5 `allow` rows, and `skill_tool_grants` for the four skills
3. ship the code (category split: `CategorySuper` → the five `*-admin` groups + `supervisorOnlyCategories`)
4. restart

**Acceptance**:

| Item | Criterion |
|---|---|
| DB | the supervisor's `required` = the 6 core groups; `allow` = the 5 on-demand groups |
| Skills | each of the four `*-authoring` skills has `skill_tool_grants`; the supervisor is bound to them by the starter kit |
| Runtime | no "falling back to default everything-on" in the logs; the supervisor's schema contains neither `browser` nor the tools of the five on-demand groups |
| On demand | the corresponding tools appear after `bind_skill` and disappear after `unbind_skill` |
| Fuse | clear the supervisor's grant rows → still everything on + one Error |
| Reversible | delete the backfilled rows + revert the code ⇒ back to the pre-change state |

**Where `CategorySuper` goes**: after the split into `agent-admin` / `skill-admin` / `tool-grant` / `custom-tool` /
`flow-admin`, no tool belongs to `super`; it is kept only as the **compatibility expansion entry** for `builtin:super`
(equivalent to the union of the five groups) so existing grant rows keep resolving. Once existing data is clean, delete it
together with `tools.CategorySuper`.

---

## 3. Target directory structure

```
{root_dir}/tenants/{schema}/
├── agents/{agent}/                  definition domain (was workspace/)
│   ├── AGENT.md
│   └── skills/{skill}/SKILL.md        private skills
├── skills/{skill}/SKILL.md          definition domain (was shared-skills/)
├── users/{user_id}/                 runtime domain
│   ├── files/                         user uploads / cross-session artifacts
│   └── sessions/{session_id}/         ← default root of file tools = exec cwd
└── runs/{run_id}/                   temporary playground without a session (see §6 T1; with a user, prefer users/{uid}/runs/{run_id}/)
```

### 3.1 Migration mapping

| Now | After |
|---|---|
| `tenants/t0/workspace/main/` | `tenants/t0/agents/main/` |
| `tenants/t0/workspace/joker/` | `tenants/t0/agents/joker/` |
| `tenants/t0/workspace/poet/` | `tenants/t0/agents/poet/` |
| `tenants/t0/workspace/poet/sfds` | **deleted** (empty file) |
| `tenants/t0/shared-skills/agent-authoring/` | `tenants/t0/skills/agent-authoring/` |
| `tenants/t0/shared-skills/english/` | `tenants/t0/skills/english/` |
| `tenants/t0/shared-skills/skill-authoring/` | `tenants/t0/skills/skill-authoring/` |

The migration **does not move files by hand**; it is written as an **idempotent** function run at startup (`agents/` already
has content → skip; otherwise `Rename`), and it works with all three Storage implementations (local / nfs / s3).

> **Executed on 2026-10-11**: under `tenants/t0/`, `workspace/` → `agents/` and `shared-skills/` → `skills/`, with all three
> agents' `AGENT.md` and private skills intact. The migration implementation (cross-tenant walk + idempotent rename) was
> removed from the code after it ran, so a restart no longer performs any migration; see git history if you need to reuse it.

---

## 4. Tool semantics and permission matrix

### 4.1 The three domains

| Domain | Path | Read | Write | Gate |
|---|---|---|---|---|
| **Runtime domain** | `users/{uid}/sessions/{sid}/` (default root, no prefix needed) | this session | write freely | none |
| **User domain** | `/users/{uid}/files/` | the user / the supervisor | the user / the supervisor | none |
| **Definition domain** | `/agents/{name}/`, `/skills/{name}/` | all agents | **supervisor only** (exception in the next row) | `status == 1` |
| **Definition domain · own skills** (D23) | `/agents/{self}/skills/{skill}/**` | the supervisor + the agent itself | the supervisor + the agent itself | write: own `status == 1` + skill `status == 1`; read: no gate |

### 4.2 Tool mapping

| Tool | Before | After |
|---|---|---|
| `read_file` / `write_file` / `edit_file` / `list_files` / `delete_file` / `file_exists` | root = `workspace/{me}` | root = runtime working directory (session / runs); `/` prefix = tenant root (supervisor only) |
| `exec` | mounts `workspace/{me}` → `/data` | mounts runtime domain → `/workspace`(rw, cwd); definition domain → `/agent`(ro) |
| `read_agent` / `read_skill` / `list_agents` / `list_skills` | `WorkspaceKey` | point at `agents/` / `skills/`; `list_skills` gains `all` (supervisor only) |
| `create_agent` / `update_agent` / `delete_agent` | write `workspace/{name}/` | write `agents/{name}/` |

> The word "workspace" now refers exclusively to the **runtime working directory** (mapped into the sandbox as `/workspace`),
> no longer to an agent's definition domain. The tree on the left of `Agents.vue` is renamed "definition"; the "workspace"
> panel now reads the session directory.

`call_agent` has two further constraints (separate from the write gate): ① a deprecated agent cannot be called
(`IsRunnable`); ② no row in the `agentrelations` grant table means denied. The supervisor is not excluded by either — it can be
called by other agents / flows, and when called it still reads the draft per `resolveDefinitionMode` (admin-surface abilities
are not discounted because of who the caller is).

### 4.3 Cross-domain examples for the supervisor

```
write_file("/agents/poet/AGENT.md", ...)            → requires poet.status == 1
edit_file("/skills/report-format/SKILL.md", ...)    → requires this skill.status == 1
read_file("/users/B/files/a.txt")                    → reads are unrestricted
exec("npm run build")                                → runs in /workspace of its own session
```

### 4.4 Root addressing for non-supervisors (D23)

```
write_file("/agents/joker/skills/report/docs/spec.md", ...)  → ✅ attached file of its own private skill
                                                                (the skill must be status == 1; the agent itself must be status == 1 too)
read_file("/agents/joker/skills/report/SKILL.md")             → ✅ reads have no status gate
list_files("/agents/joker/skills/report")                     → ✅ lists its own skill directory
write_file("/agents/poet/AGENT.md", ...)                      → ❌ someone else's definition domain
write_file("/agents/joker/AGENT.md", ...)                     → ❌ AGENT.md is out of scope here (belongs to meta tools)
write_file("/agents/joker/skills/unknown/a.md", ...)          → ❌ unregistered skill; the hint is to use create_own_skill
write_file("/skills/report-format/SKILL.md", ...)             → ❌ tenant-level shared skill (supervisor only)
delete_file("/agents/joker/skills/report/SKILL.md")           → ❌ the entry file may not be deleted on its own (D23)
```

> Writing `agents/joker/skills/...` without the `/` prefix is stopped by `entityLikePath`, which returns the form above as
> guidance to the model — otherwise it would believe it had "created" something and leave a fake entity directory behind in the
> workspace.

---

## 5. exec / sandbox mounts

```go
Mounts: []sandbox.Mount{
    {Host: workDir,   Guest: "/workspace", ReadOnly: false}, // the playground (session or runs), cwd
    {Host: agentRoot, Guest: "/agent",     ReadOnly: true },  // its own definition domain, read-only
}
```

`workDir` is `users/{uid}/sessions/{sid}/` with a session and `users/{uid}/runs/{run_id}/` without one (see T1). `exec.go` needs
no distinction — it only honours `Context.Workspace`, an already-computed host absolute path.

Caveats:

- the `Mount.ReadOnly` field exists (`tools/sandbox/sandbox.go`), but **`none` isolation translates sandbox paths back to host
  paths**, so read-only relies on the tool layer policing itself; only `nsjail` / `docker` are truly read-only;
- `Mount.Host` is a **host path**, and sandboxd may run on another machine / container → **temporary directories must not use
  `os.MkdirTemp`**; they must live under `{root_dir}`;
- the "must be under this tenant's directory" panic assertion in `Context.StoragePath` is **kept** → cross-tenant is still
  unreachable;
- `nsjail`'s rootfs needs the **`/workspace` directory to pre-exist** (a bind mount target must already exist; `/tmp` works
  everywhere simply because it is always there);
- exec's `cwd` only goes through `safePath` and currently **does not support the `/` tenant-root prefix** → the supervisor gets
  no over-privileged shell channel either.

---

## 6. Open items

| # | Status | Conclusion |
|---|---|---|
| **T1** | ✅ decided | without a session use `users/{uid}/runs/{run_id}/` (one per call, **no `sessions` / `session_records` written**); deleted when the call ends, with a backstop sweep at startup. Only when there is not even a user does it fall back to the tenant-level `runs/{run_id}/` |
| **T2** | ✅ decided | do it. The flow's `ExecContext` gains `UserID`, and agent nodes share the flow's `session_id` and user |
| **T3** | open | should privileged tools write an audit log (who changed what of whose, and when) — recommended |
| **T4** | open | minimise the supervisor's exec mounts per call (e.g. `exec(agent="poet")` mounts only that definition domain) — when the need arises |

### 6.1 Availability of `session_id` / `user_id` per entry point

**Conclusion: every entry point can obtain a user** (the `AuthInfo` of RPC / MCP / flow is resolved from the apikey / oauth
binding, and `ID` is the bound user).

| Entry point | `session_id` | `user_id` |
|---|---|---|
| `admin/agents/chat.go` (chat) | yes (created / validated) | yes (`ai.ID`) |
| `service/rpc/server` (RPC endpoint call) | no (the caller passes `params._meta[session_id]`, **the only place honoured**) | yes (`authInfo.ID`) |
| `service/mcp/server` (MCP endpoint call) | no (same as above) | yes (`authInfo.ID`) |
| `service/flow/executors/rpc_target.go` (agent node in a workflow) | yes (the flow's `ExecContext.SessionID`) | yes (`authInfo.ID`) |
| `service/agent/caller.go` (`call_agent` sub-agent) | passes through the parent's | passes through the parent's (**may be empty**) |
| `service/websocket/handler.go` (background debugging) | yes (`ExecContext.SessionID`) | yes (`s.authInfo.ID`) |

`session_id` is a **switch**: passed = there is a session (resumable, recorded, artifacts stay in `sessions/{sid}/`); not passed
= stateless (`runs/{run_id}/`, leaving no trace). Protocol endpoints honour only `params._meta[session_id]`:
`arguments.session_id` is no longer treated as a session switch (an old client passing it that way gets a stateless call), and
that key is still stripped by `StripSessionID`, so it never leaks into the prompt. `agent_impl.go` enforces UUID validation on
`session_id` (it is concatenated into the storage path) and **errors out on an invalid value** instead of silently falling back
to `runs/`: the entry layer has already validated it, so an invalid value here is an internal bug, and the cost of quietly
switching paths is artifacts landing in a directory that is deleted as soon as the run ends.

RPC / MCP's `user_id` is **always present**: `verifyAPIKey` / `verifyOAuth` only admit credentials bound to a user, and
`authorizeBinding` rejects an empty `UserID` outright (`svc_auth_bindings.user_id` is `uuid NOT NULL`, so this is a defensive
hard check). Those two entry points therefore have no "tenant-level / ownerless" calls, and the entry side no longer writes the
`if authInfo.ID != ""` fallback branch.

### 6.2 The per-line model of session memory (must-read for troubleshooting)

**There is exactly one read implementation and one write implementation, both in the agent layer**: `loadSessionHistory`
(read) and `recordSessionRound` (write, `defer`red inside `Stream`, written on success / failure / termination) in
`agent_impl.go`. Entry points only do three things: pass `session_id`, pass `user_id`, inject `_run_type` (`call` / `rpc` /
`mcp` / `agent` / `flow`). The console chat no longer reads history itself; `admin/agents/chat.go` only passes `_user_message`
(fed to the model, including the attachment list) and `_record_input` (persisted, the user's own words).

**Write condition**: write only when both `session_id` and `user_id` are non-empty; missing either makes it a one-off run with
no record. Retention: reads take the latest 50 records totalling at most 256 KiB (`trimHistoryByBudget`), writes keep the
latest 100.

**One `session_id` can hold several lines.** The ownership key of `session_records` is
`(owner_type, owner_id, session_id)`, while the `sessions` row records only the **creator** (`owner_*`) — the two are not the
same thing:

| Writer | `owner_type` | `owner_id` | Trigger |
|---|---|---|---|
| the current agent | `agent` | that agent's id | a normal turn ends |
| `call_agent` sub-agent | `agent` | the sub-agent's id | the parent session passes through (`caller.go`) |
| agent node inside a flow | `agent` | the agent id configured on the node | `rpc_target.go` passes the same sid through |
| the flow itself | `flow` | that flow's id | that flow's `session_memory` switch |

So "the console history is missing a few turns" is usually not data loss but a question of **which line is being read**:

- the model context (`loadSessionHistory`) reads the `owner = current agent + its own id` line — a sub-agent's intermediate
  turns and the flow's own records never enter the context;
- the console's `GET /admin/sessions/:id/messages` reads **the line of the session row's owner** (`admin/agents/session.go`),
  and likewise cannot see other lines;
- **a flow's session memory is a switch, not a number**: `flows.session_memory` decides whether the flow is stateful at all
  (on by default); when it is off, `ResolveMaxSessionRecords()` returns 0 and `persistSessionMemory` returns immediately.
  The number of messages kept comes from the tenant quota (`flow_configs.max_session_records`). So a tenant that turned the
  switch off has no `owner_type='flow'` records at all — check this switch before suspecting data loss.

**Ownership and access control are two different things**: `sessions.owner_*` only answers "who created it"
(display / audit); access control is `VisibleToUser(user_id)`. That is why RPC / MCP / flow can reuse a session row created by
another entry point under the same user (`ResolveOrCreateForUser` returns an existing row as-is and does not change the owner)
— that is the "relay on the same playground" design, not an escalation. The console's session list is still queried by
`owner_type='agent' + owner_id` (`ListSessions`), so `owner_type='flow'` sessions never appear there — that is a list
criterion, not a permission problem.

Troubleshooting SQL (inside a tenant schema):

```sql
-- how many lines this session has, and how many turns each wrote
SELECT owner_type, owner_id, count(*) AS rounds, min(created_at), max(created_at)
  FROM <schema>.session_records
 WHERE session_id = '<sid>'
 GROUP BY owner_type, owner_id
 ORDER BY min(created_at);

-- tenant-wide: sessions written by more than one line (should be empty or very rare by default)
SELECT session_id, array_agg(DISTINCT owner_type) AS lines, count(*) AS records
  FROM <schema>.session_records
 GROUP BY session_id
HAVING count(DISTINCT owner_type) > 1
 ORDER BY records DESC
 LIMIT 20;
```

**Shape of the persisted body**: the console stores the user's own words (`_record_input`; the copy fed to the model carries the
attachment list and goes through `_user_message`); protocol endpoints (RPC / MCP / flow) have no notion of "one sentence", so
the body is the JSON of the business arguments. Both history replay and the model context read `input.content` through
`core.RecordUserContent`, so both shapes appear verbatim in history — that is expected; do not "fix" it because the shapes
differ.

**Session-count quota**: the cap is bucketed by `(owner_type, user_id)` (user override → tenant value → system row, with
**fallback `200` per bucket**; the system row / tenant row / `user_quotas` can tighten it, and `-1` still means unlimited
for a deployment that wants no cap at all). The two paths behave differently:

- **console** (`sessions.Create`, i.e. creating a session / first message / auto-create on upload in `admin/agents`): over the
  limit returns 507 and the human deletes old sessions by hand — chat history does not make that decision for the user;
- **protocol endpoints** (`sessions.CreateWithID`, when RPC / MCP bring their own new `session_id`): over the limit
  automatically reclaims the session with the oldest `updated_at` in that bucket (together with its `session_records`,
  workspace directory and usage counters), frees the slot and creates the new one, logging
  `[QUOTA_ALERT] session count at the limit, reclaimed oldest sessions ... evicted=N`. Endpoint credentials are usually
  bound to a separate account, so console
  sessions are not squeezed out; if it is still over the limit after reclaiming (concurrent grab, cap of 0), it returns
  `-32000` / 507.

**Deleting an agent or a flow deletes its sessions** (`sessions.DeleteByOwner`, called from both delete handlers): rows,
`session_records`, workspace directories and usage counters. Without it they would still count against `max_user_sessions`
while the console can no longer list them (the session panel is per agent / flow), so a user sitting at the cap would be
locked out with nothing left to delete.

---

## 7. Staged implementation checklist

### Stage 1 — foundation (renames + constants + migration) ✅ done

| File | Change |
|---|---|
| `shared/paths/paths.go` | constant renames (`AgentsSubdir` / `TenantSkillsSubdir` / `AgentSkillsSubdir`) + new runtime-domain constants (`UsersSubdir` / `FilesSubdir` / `SessionsSubdir` / `RunsSubdir`); new `AgentsKey` / `UserFilesKey` / `UserSessionKey` / `RunsKey`; new `SafePathEx(p, allowTenantRoot)` |
| `shared/tenants/tenants.go` | new `ListSchemas()` (enumerate tenants; the package previously had only `Get` / `IsActive` / `Invalidate`) |
| startup migration | was hooked as `agent.MigrateLayouts()` in `cmd/main.go`, **removed after it ran** |
| `admin/agents/files.go`, `admin/skills/files.go`, `service/agent/tools/{meta,tool}.go`, `service/agent/agent_impl.go` | **no code change needed**: all paths are computed from constants, so changing the constant values applies globally |
| `service/agent/skills.go`, `service/agent/tools/paths.go` (then named `workspace.go`) | only the path example comments synced |

### Stage 2 — session plumbing ✅ done

| File | Change |
|---|---|
| `shared/paths/paths.go` | new `UserRunsKey` + `ResolveUserSessionDir` / `ResolveUserRunsDir` / `ResolveRunsDir` |
| `service/agent/tools/tool.go` | `Context` gains `SessionID` / `RunID` / `WorkKey` / `AgentRoot`; `StoragePath` re-rooted to the runtime domain; new `AgentPath` (definition-domain reads, for `AGENT.md`); `CallRequest` gains `SessionID` |
| `service/agent/agent_impl.go` | takes `session_id` (enforced UUID validation) / `run_id`; computes `Workspace` / `AgentRoot`; `loadSystemPrompt` moved to `AgentPath`; `Close` cleans up temporary runs directories |
| `admin/agents/chat.go` | `NewAgent(..., {"user_id", "session_id", "run_id"})` |
| `service/rpc/server`, `service/mcp/server` | pass `authInfo.ID` as `user_id`; pass `input.session_id` through explicitly |
| `flow/core/execContext.go`, `flow/executors/runner.go` | `ExecContext.UserID` + `NewFlow(..., userID, ...)`; `ensureSession` passes `execCtx.UserID` |
| `service/flow/executors/{rpc_target,rpc}.go`, `service/websocket/handler.go` | `NewFlow` call sites fill in `authInfo.ID`; `runEndpoint` passes `AuthInfo` + `sessionID` through |
| `service/agent/caller.go`, `tools/meta.go` | `call_agent` passes the parent's `session_id` through |
| `service/agent/tools/exec.go` | mount table `{workDir → /workspace(rw,cwd)}` + `{agentRoot → /agent(ro)}` |
| `shared/repository/sessions/postgres.go` | `Create` / `EnsureExists` create the directory, `Delete` deletes it |
| `service/agent/janitor.go` | **new file**: backstop sweep of `runs/` at startup |
| `admin/agents/files.go` + `admin.go` | new read-only session workspace browsing endpoints `/admin/agents/:id/session/{files,file}` |
| `desktop` | `Workbench.vue` implements the "workspace" panel; `Agents.vue` title changed to "definition"; `store/sessionContext.js` for the shared session context |
| workflow script pool | **no change needed**: sandboxd's `/v1/code` (previously `/v1/script`, earlier `/v1/session`) only mounts its own `workers_dir` / `tmp_dir`, unrelated to agent data directories |

### Stage 3 — supervisor permissions ✅ done (extensions below)

| File | Change |
|---|---|
| `shared/models/protected.go` | `IsMainAgent` / `IsProtectedAgent` / `IsProtectedSkill` (identity predicate decoupled from the protection list, see D17) |
| `service/agent/tools/gate.go` | **new file**: `resolveTarget` (pure string parsing of path → entity directory) + `checkWrite` (identity is supervisor + not an entity root + the target exists and `IsModifiable`; when writing an agent's private skill it also checks whether the **owning agent** is editable) + `resolveRead` / `resolveWrite` (`SafePathEx` + cross-domain write gate) |
| `service/agent/tools/file.go` | the 6 file tools go through `resolveRead` / `resolveWrite`; writes outside its own workspace pass the gate; `delete_file` always deletes a directory recursively (no extra parameter; the boundary is still decided by `resolveWrite`'s entity root / status gate) |
| `service/agent/tools/tool.go` | `SkillRef{ID, Name, Scope}` (after D19: `Scope` = space targeting, agent = its own private / tenant = tenant-level / empty = visibility criterion); `Context.AgentPath` / `TenantPath` |
| `service/agent/tools/meta.go` | the two skill tool sets (`*_own_skill` / `*_shared_skill`, tool name = ownership, see D19); `read_skill` supports `skill_id` targeting (supervisor only); the binding tools `bind_skill` / `unbind_skill` (appends / removes one, idempotent) — the target agent must be `status == 1` and only tenant-level skills are accepted; `update_agent` gains a `discovery` switch; `read_agent` also returns the skill names bound to that agent |
| `service/agent/skills.go` | `findRef` (ID → `Scope` space targeting → visibility criterion) + `checkSkillScope` (own may only touch its own private skills / shared only tenant-level) + `IsMainAgent` predicate + `IsModifiable()` gate + `IsProtectedSkill` (no delete / no rename); dual landing points for tenant-level / private skills; `BindAgentSkill` / `UnbindAgentSkill` / `ListAgentSkills` / `SetAgentSkillDiscovery` (skill binding allowlist IO) |
| `service/agent/tools/meta.go` (D21 addition) | `grant_agent_tool` / `revoke_agent_tool` (category super; the target must be `status == 1` and **may be itself** — changing itself marks `Context.MarkToolsDirty()`, D5 revision); `read_agent` also returns `tool_grants` (each with `effective` / `via_skill` / `note`), `unrestricted` and `discovery` (the latter two read directly from agent instance fields); `update_agent` gains an `unrestricted` switch (through `Agents.SetUnrestricted`) |
| `service/agent/toolgrants.go` + `shared/repository/toolgrants` (D21 addition) | `toolGrantManager`: ref validation (`toolgrant.Catalog`; `builtin:super` is auto-rejected for a non-supervisor target) + **automatic allow / required assignment** (by whether the target's injected skills declare that dependency, expanded to leaf tool refs and intersected) + effective-state echo (reusing `Resolver`); the repository gains `UpsertAgentGrant` (delete then insert by ref, guaranteeing one row per ref) / `DeleteAgentGrant` (exact delete, returning the row count). The "unrestricted tools" switch (`agents.tools_unrestricted`) **is an agent property**, read and written by `shared/repository/agents` (`Get/SetUnrestricted`), with the write entry point in `tools.AgentManager.SetUnrestricted`; `toolgrant.Resolver` receives that value from the caller and no longer queries the DB itself |
| `service/agent/tools/meta.go` (D22 addition) | `allow_agent_call` / `revoke_agent_call` (category super: the caller must be `status == 1`, the callee must not be a supervisor / deprecated / itself); `read_agent` also returns `can_call` / `called_by`; `update_shared_skill` gains a `restricted` switch; `read_skill` fills in `restricted` + `bound_agents` |
| `service/agent/tools/tool.go` + `skills.go` (D28 addition) | `SkillDetail.ToolDeps` + `skillManager.fillToolDeps`: `read_skill` / `use_skill` echo the tool dependencies the skill declares (refs as-is, groups not expanded, dangling entries not removed; always an array). Display only, no activation, and non-supervisors get them too (activation is always narrowed by `allow`); a repository read failure degrades to an empty array without affecting the body |
| `service/agent/relations.go` + `shared/repository/agentrelations` (D22 addition) | `agentRelationsManager`: `AllowCall` / `RevokeCall` / `ListRelations`; the repository gains `AddRelation` (`ON CONFLICT DO NOTHING`) / `RemoveRelation` (returning the row count) — single-row operations **must not** be done through `ReplaceByCaller` / `ReplaceByCallee` (a full replacement would wipe other people's edges too) |
| `shared/repository/skills` (D22 addition) | `SetRestricted`: changes only the `skills.restricted` column. **Do not use `SetVisibility`** — it clears and rebuilds `agent_skill_bindings` inside a transaction, so flipping only the switch would delete the whole allowlist built by `bind_skill` |
| `service/agent/toolgrant/catalog.go` | **admin-class consolidation (D18 / D19)**: new `CategorySuper` (natural group `builtin:super`), moved out of `builtin:agent` / `builtin:skill`; `Catalog` carries an `isMain` view and filters by identity; delete the redundant `preset:agent-manage` / `preset:skill-manage` |
| `service/agent/tools/tool.go` + `registry.go` | `IsSupervisorOnlyTool(name)` + a hard gate in `ExecuteTool` (category super and not a supervisor → refused) |
| `shared/models/models.go` + `sql/agent_default_tool_grants.sql` | the default tool set does **not** contain `*_own_skill` (12 rows, materialised as `required`, see D20; after the D28 revision it contains `use_skill` — otherwise an ordinary agent could not activate the tools of a bound skill); the backfill script follows the same criterion |
| `sql/skill_own_tool_rename.sql` | **new file**: renames `builtin:{create,update,delete}_skill` to `*_own_skill` in the three tables (the tool name is part of the ref; without the rewrite they dangle) |
| `data/tenants/t0/...` | prompt sources synced: `skill-authoring` (the two tool tables + decision table), `agent-authoring` (default tools and permission boundary), `main/AGENT.md` (tool list and procedures) |
| `admin/toolgrants/handlers.go` | `ListToolCatalog?agent_id=` generates the catalog per identity; `SetAgentToolGrants` / `GetAgentEffectiveTools` share the criterion |
| `desktop` | `Agents.vue` loads the catalog per agent (a non-supervisor never sees `builtin:super`); `Skills.vue` keeps the dependency picker complete with a "supervisor only" badge |
| docs | `agent-authoring` / `skill-authoring` SKILL.md + `main`'s `AGENT.md`, rewritten for "you can no longer only delete and recreate" and "file tools are no longer limited to your own workspace" |

**Extensions (in the original plan but not done; open on demand)**:

- `update_agent` supporting `agent_md` (rewriting the AGENT.md body) — for now the body goes through the file tool
  `write_file("/agents/<name>/AGENT.md", ...)`
- `list_skills`' `all` (supervisor-only full listing)
- `set_agent_relations` (**full replacement, two-way** form): shipped per D22 as the paired single-row `allow_agent_call` /
  `revoke_agent_call`
- `set_agent_toolgrants` (**full replacement** form): shipped per D21 as the paired single-row `grant_agent_tool` /
  `revoke_agent_tool`, with no full replacement any more (an LLM omitting one entry in a full replacement is equivalent to
  silently revoking a grant)

### Stage 4 — own skill directory for non-supervisors (D23) ✅ done

| File | Change |
|---|---|
| `shared/paths/paths.go` | `SafePathEx`'s second parameter renamed `allowRootAddr` and its semantics comment rewritten (syntax only; authorization drops down to the gate); new `SkillEntryFile` constant, used by `SkillPath` / `AgentSkillPath` |
| `service/agent/tools/gate.go` | `checkReadOwn` (read gate: the supervisor is unrestricted (D4), a non-supervisor only its own private skill directory) + `checkWriteOwn` (write gate: shape / ownership → not an entity root → own status gate → `AgentID` non-empty → the skill exists and belongs to it → skill status gate); `parseRootAddr` splits "parsing" from "authorization" (the write side no longer reuses the read side, so a read-criterion error is not returned to a write call); `entityLikePath` gains a `selfName` parameter so that a path into its own definition domain gets an actionable root-addressing form |
| `service/agent/tools/file.go` | `delete_file` gains `SKILL.md` entry-file protection (refused on its own under root addressing), aligned with the admin side's `isSkillEntry`; the path descriptions of the 6 tools now read "the supervisor reaches the whole tenant / a non-supervisor only its own private skill directory" |
| `service/agent/skills.go` | ① `checkOwnerAgent` drops the "exempt yourself" rule: a locked agent can still run (`IsRunnable` only stops deprecated ones), and skipping the self-check let a locked agent keep editing its own private skills while contradicting `checkWriteOwn`. ② **renaming became a whole-directory move** (`storage.Rename`, same source as `paths.RenameAgentDir`): it used to be "write the new `SKILL.md` + delete the old one", which leaves orphans in the old directory as soon as a skill has attached files — unreachable before this stage (attached files could not be written), a real bug once opened |
| `admin/skills/admin.go` | same: the admin-side rename was also "write the new entry file + delete the old", now changed to a whole-directory move (same criterion, so the two entry points do not diverge) |
| `service/agent/tools/tool.go`, `shared/models/models.go` | stale comments in `TenantPath` / `DefaultAgentTools` synced (they said "a non-supervisor cannot cross the boundary with `/`", which no longer holds) |
| tests | `gate_test.go`: the read / write allowlist matrix, the double status gate on itself and the skill, `AgentID` missing rejection (so a shared skill is not misjudged as private by `FindForWrite("")`), and the split between self and other guidance; `file_test.go`: `SKILL.md` protection (one case each for a non-supervisor and the supervisor) + attached files still deletable |

**Deliberately not done** (unrelated to this requirement; open when needed):

- `update_own_agent` (a non-supervisor editing its own `AGENT.md` / description) — the write side is still closed; the read
  side was opened to `agents/{self}/` by D32
- remounting `/agent/skills` read-write in the exec sandbox (nested mount) — the supervisor's shell is read-only too, and
  "keeping up with the supervisor" means leaving it as is (see D11 / §5)

### Stage 5 — making the supervisor's tool grants explicit (D25 / D26) ✅ done

| File | Change |
|---|---|
| `service/agent/tools/tool.go` | the admin surface split from `CategorySuper` into `agent-admin` / `skill-admin` / `tool-grant` / `custom-tool`, plus a new `skill-own`; `supervisorOnlyCategoryList` + `supervisorOnlyCategories` are the single criterion for "supervisor only", and `IsSupervisorOnlyTool` uses it |
| `meta.go` / `customtool_meta.go` | the `Category` of the 19 admin-class tools re-tagged into the four groups; the `skill-own` trio split out of `skill` |
| `toolgrant/catalog.go` | the category table extended to 11 groups + a `super` compatibility entry; `builtinGroupCategories` gains 5 groups (excluding `super`, to avoid duplicate cards); `builtinByCategory("super")` special-cased to the union of the four groups; labels rewritten per group |
| `shared/models/models.go` | `SupervisorCoreGrantRefs` (6 groups) + `SupervisorCoreGrants()` + `MainCoreMigrationPending()` (marker `builtin:agent-admin`); `toolGrantsOf` now serves only `DefaultAgentToolGrants` |
| `shared/repository/agents/postgres.go` | `CreateAgent` picks the seed by identity: the supervisor gets `SupervisorCoreGrants()` (`item_type=group`), everything else still `DefaultAgentToolGrants()` |
| `service/agent/agent_impl.go` | deletes the "the supervisor always owns every built-in tool" block; the zero-row fallback is demoted to a fuse that logs an Error; new self-clearing migration gate (`onlyLegacyDefaultGrants`) |
| `admin/toolgrants/handlers.go` | preview from the same source: only "zero rows / core not materialised" is marked `BuiltinDefault` and merged with the baseline, everything else is read from the table honestly; when not migrated, the warning names the script to run |
| `sql/main_core_tool_grants.sql` | backfill for existing tenants: clear the old per-tool core rows → write the 6 group rows; the single-group marker decides whether migration happened, and **a deleted group is not added back** by a re-run |
| `sql/supervisor_on_demand_groups.sql` | the supervisor's 4 `allow` rows; `skill_tool_grants` for the three `*-authoring` skills; removes the supervisor's resident bindings to them |
| `data/tenants/t0/agents/main/AGENT.md` | new four-step flow "bind the skill → read it fully → work → unbind" plus the mapping table; the resident / on-demand boundary |
| `data/tenants/t0/skills/custom-tool-authoring/SKILL.md` | rewritten (the meta tools had long existed, the old text said they did not): two landing paths, the maintenance operation table, a verification checklist |
| `desktop/.../Agents.vue` | removes the "built-in tools not selectable" restriction for the supervisor; core groups get a `core` badge + a delete confirmation; `importCurrentTools` gains `skill-own` |
| tests | `registry_test.go`: `supervisorOnlyCategories` closed in both directions / core groups valid / on-demand groups excluded / item-by-item agreement with the SQL arrays; `supervisor_core_test.go`: the migration gate matrix + both paths from the same source |

**Not done (deliberately left this round)**:

- tiering meta-capability skills against domain skills (whether `skills` should gain a `supervisor_only` semantic) — the former
  depend entirely on admin-class tools and binding them to a non-supervisor only yields a half-broken skill. For now
  `supervisorOnlyCategories` removes them at the catalog layer, which is enough
- whether `shell` should be demoted too: of the three criteria (low frequency / large schema / high risk) only the last one
  applies, so it stays for now
- final deletion of `CategorySuper`: once the existing `builtin:super` grant rows are cleared, delete it together with the
  compatibility expansion

---

## 8. Risks

| # | Risk | Response |
|---|---|---|
| R1 | opening super permissions to an LLM lets it modify production agents by mistake | the D2 gate + no status change (D3; after the D5 revision this is the only hard boundary) + T3 auditing |
| R2 | the migration is irreversible | idempotent rename + existing data confirmed (only one empty file) |
| R3 | sandboxd is remote → host paths invisible | temporary directories must live under `{root_dir}` |
| R4 | `none` isolation cannot enforce a read-only `/agent` | the tool layer polices itself; use nsjail / docker in production |
| R5 | the frontend "workspace" concept is confusing | the tree in `Agents.vue` means the definition domain → title changed to "definition", behaviour unchanged; the "workspace" panel newly reads the session directory |
| R6 | requires recompiling and restarting `nexus.exe`; the frontend on 5173 is unaffected | |
| R7 | non-supervisors can now write data into the **definition domain** — persistent, cross-session, cross-user, semantically different from the workspace (which lives and dies with the session) | the allowlist is limited to its own private skill directory (D23); attached files other than `SKILL.md` are **never read at runtime** (`skillManager.Get` reads only the entry file), so it is not an injection surface today. If "inject the whole skill directory" or plugin-style skill executables are ever introduced, this row must be re-evaluated |

---

## 9. Verification

1. `go build ./...` + `go vet ./...` pass
2. **Three-domain smoke test**: the supervisor's `write_file("/agents/poet/AGENT.md")` succeeds with `status=1` and reports
   "deprecated" with `status=3`
3. **Session persistence**: files produced by a chat land in `users/{uid}/sessions/{sid}/`; after deleting the session the
   directory is gone
4. **Stateless calls**: RPC / MCP without `session_id` → lands in `users/{uid}/runs/{run_id}/`, with **no** `sessions` /
   `session_records` records
5. **Sandbox**: `pwd` inside `exec` is `/workspace`; `ls /agent` shows `AGENT.md` (read-only)
6. **Regression**: the definition file tree on the frontend agent page and skill page content load normally; "workbench → open
   workspace" lists session artifacts
7. **Admin-class consolidation**: an ordinary agent's grant catalog has no `builtin:super`; a direct PUT of
   `builtin:create_agent` is rejected (`ref not found or has no tools`); the supervisor is unaffected (the core set is
   materialised as `required` rows and the four super groups are all in its catalog, see D25)
8. **Cross-agent skill editing**: the supervisor's `write_file("/agents/poet/skills/<s>/SKILL.md")` succeeds with `status=1`;
   refused when the target is deprecated; writing an entity root directly is refused (with a hint to use meta tools)
9. **Binding a skill to another agent**: the supervisor's `bind_skill(agent="poet", skill="english")` succeeds with
   `poet.status=1` and writes immediately (a repeated call changes nothing); `unbind_skill` removes it; refused when `poet` is
   deprecated; a private skill name or an unknown name errors; `read_agent(agent="poet")` returns the `skills` list
10. **Skill visibility switch**: `update_agent(agent="poet", discovery=true)` takes effect (supervisor only); omitting
    `discovery` changes nothing
11. **Skill permission model (D19 / D20)**: an ordinary agent's catalog has `*_own_skill` inside `builtin:skill` (grantable)
    and **no** `*_shared_skill`; a direct PUT of `builtin:create_shared_skill` is rejected (`ref not found or has no tools`);
    a new agent has **no** `*_own_skill` by default (the tool is invisible, a forced call is refused), and only after an admin
    grants it can it `create_own_skill` and persist; using `*_own_skill` on a shared skill is refused (with a hint to use
    `*_shared_skill`); the supervisor can use both sets; after running `sql/skill_own_tool_rename.sql` the three tables no
    longer contain `builtin:{create,update,delete}_skill`

---

## 10. Execution order

```
stage 1 foundation (renames + constants + migration)              ✅ done
   ↓
stage 2 session plumbing (session_id + directory lifecycle + mounts)  ✅ done
   ↓
stage 3 supervisor permissions (three-domain addressing + gate + meta tools + admin-class consolidation)  ✅ done
   ↓
stage 4 skill create/update/delete split by ownership (own / shared tool sets + checkSkillScope + ref migration)  ✅ done
   ↓
stage 5 default tool set tightened (self-build moved out of the default set, granted per agent by admins, see D20)  ✅ done
```
