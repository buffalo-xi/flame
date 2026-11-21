# Tenant provisioning resource plan

[中文](tenant-template-pack.zh.md) | English

Where a new tenant's assets come from: a **template pack committed to the repository**.

In one sentence: t0 is used once to generate this pack; **at runtime nothing reads t0, and nothing reads `data/tenants/t0`**.

## Why a pack instead of copying t0

1. t0 is a real tenant on the development machine and does not exist in production — copying it would turn development data
   into a dependency.
2. A whole-schema copy would drag t0's business data (LLM providers, connections, workflows, sessions, users) along, so the new
   tenant would not be clean.
3. The old `tenant_template.sql` is a static snapshot of some past t0 dump and is already stale (it lacks columns that only
   exist in incremental migrations, such as `agents.is_supervisor`); creating tables from it yields tenants with missing
   columns.

## Pack layout

```
nexus/platform/provision/template/
├── schema.sql      # full table definitions (35 tables / constraints / indexes / comments, no schema prefix on object names)
├── seed.sql        # data rows for the seeded assets (fixed uuids)
└── files/
    ├── agents/main/AGENT.md
    ├── skills/skill-authoring/SKILL.md
    ├── skills/agent-authoring/SKILL.md
    ├── skills/custom-tool-authoring/SKILL.md
    └── skills/flow-authoring/SKILL.md
```

Embedded as a whole on the Go side:

```go
//go:embed template
var templateAssets embed.FS
```

## Provisioning flow (`provision.Provision`)

One transaction; the order is the design:

1. `CREATE SCHEMA "<new>"`
2. `SELECT set_config('search_path', '<new>', true)` — the SQL inside the pack carries no schema prefix
3. run `template/schema.sql`
4. run `template/seed.sql` (fixed uuids, **not idempotent**: provisioning happens once)
5. `INSERT users` (the first `manage` user, `primary`, with the password supplied by the caller)
6. materialise `template/files/` → storage `tenants/<new>/<rel>` before committing
7. `INSERT platform.tenants`
8. write `platform.tenant_capabilities`: `is_allowed = true` for every entry of `capability.All()` (**open by default**)
9. `COMMIT`
10. invalidate caches after commit: `sharedtenants.Invalidate` / `quota.Invalidate`

### On default capability grants

A new tenant can use exec / shell / code / browser / egress out of the box because the **rows are written**, not because
defaults were changed:

the semantics of `capabilitypolicy` is "no row = not granted" (deny by default). Changing the default to allow would disable
this platform-side gate for every existing tenant — a tenant could then grant itself anything, which is no gate at all. So the
only option is to materialise 5 rows at provisioning time.

There are still two platform-side controls:

- switch items off one by one: the console's "capabilities" page sets an item to `false` for a tenant (or deletes the row =
  back to not granted)
- a global kill switch: once `platform.capability_gates` blocks something, it overrides the tenant's explicit enablement

For the same capability: `effective = tenant row AND NOT global gate`.

Any failing step rolls the transaction back; files already written are reclaimed with `DeletePrefix("tenants/<new>")` —
guaranteeing "either a complete tenant or nothing at all". Files are written before commit precisely for this reason.

## Seeded assets

`seed.sql` must stay in step with the two SQL files below; change one and you must change the other, otherwise new tenants come
out half-built:

| Source | Content |
| --- | --- |
| `nexus/sql/main_core_tool_grants.sql` | main's 6 required groups: `builtin:file` / `exec` / `shell` / `agent` / `skill` / `agent-admin` |
| `nexus/sql/supervisor_on_demand_groups.sql` | the supervisor agent's 5 allow groups: `builtin:skill-own` / `skill-admin` / `tool-grant` / `custom-tool` / `flow-admin` |
| same | skill tool dependencies: `skill-authoring`→`skill-own`+`skill-admin`; `agent-authoring`→`tool-grant`+`skill-admin`; `custom-tool-authoring`→`custom-tool`; `flow-authoring`→`flow-admin` |
| same | main binds these four skills (the starter kit; its system prompt carries their descriptions) |

Fixed uuids (the grant / binding rows in the seed reference them):

| Entity | uuid |
| --- | --- |
| agent `main` | `a0000000-0000-4000-8000-000000000001` |
| skill `skill-authoring` | `a0000000-0000-4000-8000-000000000011` |
| skill `agent-authoring` | `a0000000-0000-4000-8000-000000000012` |
| skill `custom-tool-authoring` | `a0000000-0000-4000-8000-000000000013` |
| skill `flow-authoring` | `a0000000-0000-4000-8000-000000000014` |

The single source of truth for the protected-skill list is `protectedSkills` in `shared/models/protected.go`
(`models.ProtectedSkillNames()` exposes a read-only copy). **When adding a protected skill, both seed.sql and files/ must be
extended.**

## Regenerating (only needed when the source schema changed)

```powershell
cd d:/proj/flame
$env:PGPASSWORD='pg'
pg_dump -h localhost -p 5432 -U postgres -d flame --schema-only -n t0 --no-owner --no-privileges -f t0_ddl.sql

# convert to the no-schema-prefix version: drop psql meta commands, SET, set_config, CREATE SCHEMA and comment lines, then strip the t0. prefix
$lines = [System.IO.File]::ReadAllLines('d:/proj/flame/t0_ddl.sql', [System.Text.Encoding]::UTF8)
$out = New-Object System.Collections.Generic.List[string]
foreach ($line in $lines) {
  if ($line -match '^\\') { continue }
  if ($line -match '^SET ') { continue }
  if ($line -match 'set_config') { continue }
  if ($line -match '^CREATE SCHEMA ') { continue }
  if ($line -match '^\s*$') { continue }
  if ($line -match '^--') { continue }
  $out.Add(($line -replace '\bt0\.', ''))
}
[System.IO.File]::WriteAllText(
  'd:/proj/flame/nexus/platform/provision/template/schema.sql',
  ($out -join "`n"),
  (New-Object System.Text.UTF8Encoding($false)))
```

> **UTF-8 throughout is mandatory**. PowerShell's `Get-Content` decodes with the system ANSI code page by default, so
> `COMMENT ON … IS '<Chinese>'` is read as garbage and written back that way (`-f` plus `ReadAllLines(..., UTF8)` avoids it).

> **Keep the dump's statement order**. `pg_dump --schema-only` emits `CREATE TABLE` for everything first and only then
> `ALTER TABLE … ADD CONSTRAINT` for the primary / unique / foreign keys. A column-level `REFERENCES` therefore breaks:
> PostgreSQL wants the target's `PRIMARY KEY` to exist already, raises 42830 and the whole script aborts, so
> `CreateTenant` fails on a fresh database (2026-11: the four `*_versions` tables had been hand-written that way).
> If you ever move a constraint up, move it into the FK block at the end, not into the column list.

The seeded files (`files/`) come from t0's definition-domain directories on the development machine and are synced only when the
template changes:

```powershell
cd d:/proj/flame/nexus
$dst = "platform/provision/template/files"
New-Item -ItemType Directory -Force "$dst/agents","$dst/skills" | Out-Null
Copy-Item -Recurse -Force "data/tenants/t0/agents/main" "$dst/agents/"
foreach ($s in 'skill-authoring','agent-authoring','custom-tool-authoring','flow-authoring') {
  Copy-Item -Recurse -Force "data/tenants/t0/skills/$s" "$dst/skills/"
}
```

## Self-check

Without starting the service, run it once inside a transaction (it rolls back afterwards and leaves no trace):

```sql
BEGIN;
CREATE SCHEMA t_pack_verify;
SELECT set_config('search_path', 't_pack_verify', true);
\i 'd:/proj/flame/nexus/platform/provision/template/schema.sql'
\i 'd:/proj/flame/nexus/platform/provision/template/seed.sql'
SELECT name, is_supervisor, status FROM agents;                          -- main / t / 1
SELECT grant_kind, count(*) FROM agent_tool_grants GROUP BY grant_kind;  -- required 6 / allow 5
SELECT count(*) FROM skill_tool_grants;                                  -- 6 (the four authoring skills' dependencies)
SELECT count(*) FROM agent_skill_bindings;                               -- 4 (main binds the four authoring skills)
ROLLBACK;
```

Run it with `psql -v ON_ERROR_STOP=1 -f verify.sql`; any failing statement aborts the run.
