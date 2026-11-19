# Terminology (read before changing code)

Each of the four domains has its own action verbs — **do not borrow across domains**, and do not rephrase at one layer just
to sound smoother.

This file is only an **entry summary and a set of red lines**; the full mapping, state machine and shipped scope live in
`nexus/docs/terminology.md`. If the two ever disagree, fix this file to match the main document — never the other way round.

| Domain | Action | State / field | Storage |
|---|---|---|---|
| Asset marketplace | `list` / `delist` | `listed` / `delisted` | `platform.shared_assets.status`, `listed_by` |
| Endpoints | `expose` / `retract` | `exposed` (bool) | `svc_endpoints.exposed`, mirror JSON key `exposed` |
| Grant / credential | `revoke` | — | `revoke_agent_tool` and friends |
| Version / deployment | `release` / `deploy`; roll back with `rollback`; load with `apply` | `is_current` | version directory `versions/` + `is_current` |

The version domain is already shipped: `rollback` flips only `is_current` (the draft is left alone), `apply` writes only the
draft (production is left alone); `revoke` reads the current grant, and neither rollback nor apply writes it back.

**Never mix them**:

- do not use `publish` / `published` for assets or endpoints (the asset domain is `listing`, the endpoint domain is `expose`);
- do not use `revoke` for "taking an endpoint back from outside" — that is `retract`;
- do not use `hide` for retracting an endpoint — that is callability, not visibility;
- UI copy and code / database must use the same word: assets `list` / `delist` (UI: 上架 / 下架), endpoints `expose` /
  `retract` (UI: 对外开放 / 收回对外).

**When you spot a mix-up**: report it; do not rename on your own. A rename has to change, together: symbols, API fields,
database columns / state values and the mirror JSON, the frontend, the docs, and migration scripts.

## System definition-domain files and built-in tool copy must be English

"System entities" = `main` (the preinstalled super agent) + the four preinstalled skills (`skill-authoring` /
`agent-authoring` / `custom-tool-authoring` / `flow-authoring`); the single source of truth for that list is
`protectedAgents` / `protectedSkills` in `nexus/shared/models/protected.go` — **do not invent a second list**. These files are
part of the product's system prompt, so whatever is written into them must be **English**, regardless of the user's or
tenant's language:

- coverage: `nexus/platform/provision/template/files/agents/main/AGENT.md`;
  `nexus/platform/provision/template/files/skills/{skill-authoring,agent-authoring,custom-tool-authoring,flow-authoring}/**`
  (`SKILL.md`, `references/`, `templates/`, and the comments and prose inside `scripts/`);
- after changing them you must sync the same files under `nexus/data/tenants/t0/` (templates are only materialised when a new
  tenant is provisioned), verify each one with `Get-FileHash`, and compare the file sets on both sides (the template is
  embedded via `//go:embed template`, so new tenants also require a rebuild);
- the **model-visible copy** of built-in tools must be English too: `ToolMeta.Description`, the tool `Label`, and the
  `description` / `enum` text of argument schemas (`nexus/service/agent/tools/**`). They are all English today — keep it that
  way and never add Chinese;
- **not covered**: tenant-built agents / skills (follow the user's language — Chinese for Chinese users) and Go source
  comments / internal error strings (keep the repository's existing Chinese convention).

This rule constrains only **the body text written into files**; it does not affect reply language — replies still follow the
user, see `responseLanguageRule` in `nexus/service/agent/agent_impl.go`.

## Commit messages must be English

Commit subjects and bodies are **English**, in Conventional Commits form: `type(scope): subject`, where `type` is `feat` /
`fix` / `docs` / `chore` / `refactor` / `test` and `scope` is optional (`feat(desktop): bilingual UI for platform views`).
Imperative mood, lower case after the colon, no trailing period. The terminology red lines above still apply inside commit
messages: `list` / `delist`, `expose` / `retract`, `release` / `deploy` / `rollback` / `apply`, `revoke` — never `publish`
or `hide`.

Scope of the rule: the **commit message** only. Go source comments and internal error strings stay Chinese (see above), and
history is not rewritten — commits made before this rule keep their original language.

## Documentation is English-primary, with a Chinese mirror

`README.md` is the English primary and `README.zh.md` the Chinese mirror; the same pattern applies to technical documents
under `nexus/docs/`: the primary file is `<name>.md` (English) and the Chinese mirror is `<name>.zh.md` in the same
directory. Both versions carry a language switch on line 3 (`[中文](<name>.zh.md) | English` / `中文 | [English](<name>.md)`)
and both `README` files link to them.

English is the source: write the English version first, then align the Chinese mirror. Editing one version obliges you to
update the other in the same commit — a half-updated pair is worse than a single language.

Documents not yet migrated (`tenant-template-pack.md`, `workspace-layout-and-supervisor-power.md`) stay Chinese until they
are translated; do not half-translate a file in place.

## Module map

Four Go modules wired together with `replace`, no `go.work`:

| Directory | module | Purpose |
|---|---|---|
| `nexus/` | `flame/nexus` | main service: platform API, tenant API, admin API |
| `sandboxd/` | `flame/sandboxd` | execution sandbox daemon (isolation, worker pool, browser) |
| `sandboxg/` | `flame/sandboxg` | sandbox gateway (external proxy, per-tenant node selection) |
| `tools/` | `flame/tools` | local library shared by the three services (`xhttp` / `xsql` / `xlog` / `sandbox` / `code` …) |

Dependency direction is one-way: the three services reference the local library with
`replace flame/tools => ../tools`, and **`tools` never depends on any service in reverse**.

Verification:

- changing one service only: run `go build ./...` in that directory
- changing `tools/`: build all three — `nexus`, `sandboxd` and `sandboxg` — otherwise downstream compile errors stay hidden
