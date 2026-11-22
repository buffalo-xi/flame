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
- **also covered**: Go source comments (including `_test.go`), internal error strings, `msg` diagnostics and log lines are
  **English** (2026-11 decision). Diagnostic text addresses operators and `curl`; it is never what a user reads — user-facing
  copy comes from the `error` code plus the frontend locale bundles, see `nexus/docs/error-codes.md`;
- **legacy files are converted when touched**: a change to a Go file that still carries Chinese must leave that whole file
  English (comments, internal error strings, `msg`) in the same commit. Do not translate half a file — a mixed file reads as
  "this half was reviewed, the other half was not";
- **not covered**: tenant-built agents / skills (follow the user's language — Chinese for Chinese users), runtime data under
  `nexus/data/tenants/**`, and business fixtures inside tests (e.g. `"张三"`) — those are data, not prose.

This rule constrains only **the body text written into files**; it does not affect reply language — replies still follow the
user, see `responseLanguageRule` in `nexus/service/agent/agent_impl.go`.

## Comments are English; what a user reads follows the user

Two things that are easy to conflate, and both directions are wrong in their own way:

- **code comments** written or rewritten by an AI assistant (Go, `.vue` / `.js`, SQL, `_test.go`) are **English**, whatever
  language the user writes in. A comment is read by whoever opens the file next — never by a tenant — and a file whose comments
  alternate between two languages is harder to review than a file in either language;
- **anything a user reads** — labels, buttons, empty states, errors, run text — **follows the user's language**, and it comes
  from the locale bundle. Hardcoding English is the same defect as hardcoding Chinese: it just breaks a different user. The
  error path is code + bundle (`nexus/docs/error-codes.md`), the UI path is `t()` over
  `desktop/src/locales/{zh-CN,en}/**`.

Scope of the comment rule is **the lines you write or rewrite**, not the whole file: a touched file with a large Chinese
comment block is not converted wholesale, because that is how a diff becomes unreviewable. Go is already at zero and gated
(`scripts/check-locale.ps1 -Gate`, run by `scripts/run-tests.ps1`); the desktop backlog is section 7b of the same script —
reported so it can shrink by touch rather than in one sweep.

## Commit messages must be English

Commit subjects and bodies are **English**, in Conventional Commits form: `type(scope): subject`, where `type` is `feat` /
`fix` / `docs` / `chore` / `refactor` / `test` and `scope` is optional (`feat(desktop): bilingual UI for platform views`).
Imperative mood, lower case after the colon, no trailing period. The terminology red lines above still apply inside commit
messages: `list` / `delist`, `expose` / `retract`, `release` / `deploy` / `rollback` / `apply`, `revoke` — never `publish`
or `hide`.

Scope of the rule: the **commit message** only; the body-text rules above (comments, internal error strings and `msg` are
English) stand on their own. History is not rewritten — commits made before these rules keep their original language.

## Documentation is English-primary, with a Chinese mirror

`README.md` is the English primary and `README.zh.md` the Chinese mirror; the same pattern applies to technical documents
under `nexus/docs/`: the primary file is `<name>.md` (English) and the Chinese mirror is `<name>.zh.md` in the same
directory. Both versions carry a language switch on line 3 (`[中文](<name>.zh.md) | English` / `中文 | [English](<name>.md)`)
and both `README` files link to them.

English is the source: write the English version first, then align the Chinese mirror. Editing one version obliges you to
update the other in the same commit — a half-updated pair is worse than a single language.

Every document under `nexus/docs/` now follows this shape (`asset-sharing`, `design-highlights`, `error-codes`,
`i18n-plan`, `sandbox-design`, `tenant-template-pack`, `terminology`, `workspace-layout-and-supervisor-power`). Do not
half-translate a file in place — add or update the `.zh.md` mirror in the same commit.

## In-app help (desktop): one topic per screen, gated by the build

The desktop app ships its own help — a `?` on each screen plus a help center in the top bar. Rules:

- **content**: `desktop/src/help/{zh-CN,en}/<slug>.md`, one file per topic per locale; the first `# heading` is the
  drawer title, so renaming a topic is one edit, not two. zh-CN is the baseline (same as the locale bundles) — this
  is the one place where the desktop rule differs from `nexus/docs/`, where English is primary;
- **reachable**: a topic must be listed in `desktop/src/help/topics.js` *and* linked from its screen with
  `<HelpButton topic="…" />`. A file nobody links to is invisible content;
- **same commit**: changing a user-visible flow obliges updating the topic that describes it, and moving a long
  inline explanation into help means deleting it from the bundle — one fact, one place. A help topic that
  describes last month's flow is worse than no topic at all;
- **short inline, long in help**: an inline hint keeps one conclusion sentence next to the control; the reasoning,
  the failure modes and the exceptions go into the topic;
- **gate**: `npm run check:locales` (wired into `prebuild`) checks the help file set, heading counts and registry
  coverage in addition to the JSON bundles;
- **shared modules**: `desktop/src/help/**`, `src/utils/markdown.js` and `src/composables/useEscClose.js` are neutral
  root modules — both subtrees import them, and neither subtree may import the other (see
  `desktop/src/platform/README.md`).

## Module map

Six Go modules wired together with `replace`, no `go.work`:

| Directory | module | Purpose |
|---|---|---|
| `nexus/` | `flame/nexus` | main service: platform API, tenant API, admin API |
| `sandboxd/` | `flame/sandboxd` | execution sandbox daemon (isolation, worker pool) |
| `sandboxg/` | `flame/sandboxg` | sandbox gateway (external proxy, per-tenant node selection) |
| `browserd/` | `flame/browserd` | standalone browser pool (one Chromium per session, capacity + idle reclamation) |
| `browserg/` | `flame/browserg` | browser gateway (sticky session routing, per-tenant cap, aggregated capabilities) |
| `tools/` | `flame/tools` | local library shared by the five services (`xhttp` / `xsql` / `xlog` / `sandbox` / `code` …) |

Dependency direction is one-way: the five services reference the local library with
`replace flame/tools => ../tools`, and **`tools` never depends on any service in reverse**.

Verification:

- changing one service only: run `go build ./...` in that directory
- changing `tools/`: build all five — `nexus`, `sandboxd`, `sandboxg`, `browserd` and `browserg` — otherwise downstream compile errors stay hidden
- changing `nexus/platform/sql/` or `nexus/platform/provision/template/*.sql`: run
  `powershell -File scripts/verify-sql.ps1` against a local PostgreSQL. SQL never reaches the compiler, so a mangled
  identifier (2026-11: every `s` / `c` silently became `o`, e.g. `platform.tenant_oapabilitieo`) survives `go build`,
  review and the locale checks, and only fails when someone runs it on a fresh environment. The script creates a throwaway
  database, runs every script that can run on an empty schema in dependency order, then applies the tenant template
  (`schema.sql` + `seed.sql`) into a throwaway schema with `search_path` set — the way `CreateTenant` does — and drops the
  database again. The template is dump-ordered: tables first, every PK / FK last, never a column-level `REFERENCES`.
- running the tests: `powershell -File scripts/run-tests.ps1` covers all four modules (separate modules, no `go.work`). The
  sandboxd E2E packages are skipped and listed when Docker is unreachable; `-RequireDocker` turns that into a failure. The
  run ends with the copy-rule gate (`check-locale.ps1 -Gate`, always over all four modules); `-SkipCopyCheck` drops that step.
- checking tenant provisioning through the driver: `FLAME_TEST_DSN='postgres://postgres:pg@localhost:5432/postgres?sslmode=disable'
  go test ./platform/provision -run Template` runs `template/schema.sql` + `seed.sql` the way `CreateTenant` does (one
  multi-statement `Exec` inside a transaction with a transaction-scoped `search_path`) and asserts the seeded `main` agent.
  `verify-sql.ps1` covers the SQL text via psql, but not the driver path; the test skips itself when the variable is unset.
- checking the copy rules: `powershell -File scripts/check-locale.ps1` reports the Chinese left in Go sources, the
  user-visible copy that never reaches a locale bundle (a response without a code, an `ErrCode` whose code has no bundle
entry, hardcoded Chinese display fields), and the
  code-versus-bundle diff for `en` / `zh-CN`. It is a baseline by default; `-Gate` turns the decidable checks (Chinese
comments, Chinese in non-test Go sources, un-bundled Chinese copy in the desktop sources, `ErrJSON`, a Chinese `msg`) into
  an exit code, with the two allowlists at the bottom of the script as the only exceptions.
