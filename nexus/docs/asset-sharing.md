# Cross-tenant asset distribution (platform review + copy-based install)

[中文](asset-sharing.zh.md) | English

> Status: **all three chains are implemented** (skills / custom tools / agents — list / review / install / withdraw). Scope:
> agents / skills / custom tools flowing from tenant A to tenant B.
> Settled: **platform review + copy-based distribution**; no cross-tenant references, no two-way sync, no automatic upgrades.
>
> Shipped this round:
>
> | Layer | Location |
> |---|---|
> | File packaging / restore / redaction / naming | `shared/assetpack` (with unit tests) |
> | Table access | `shared/repository/sharedassets` |
> | Tenant-side API | `admin/share` (`/admin/share/*`) |
> | Platform-side API | `platform/handler/assets.go` (`/platform/assets/*`) |
> | DDL | `platform/sql/shared_assets.sql` (**must be executed first**, otherwise those endpoints report a missing table) |
>
> `kind` supports `skill` / `custom_tool` / `agent`. How an agent is installed is in §7 step 4: skills are rebound **by name**,
> tool grants are rebuilt by `ref`, the three switches (`is_supervisor` etc.) follow the package, and `agent_relations` are not
> copied (after installing you must reconfigure "who may call me" in the target tenant).

## 1. Why it has to be "copy"

| Fact | Consequence |
|---|---|
| One PG schema per tenant; `agents` / `skills` / `custom_tools` / grant tables all live inside that schema, **there is no tenant column** | referencing by id across tenants would bypass isolation |
| Files live under `tenants/<schema>/{agents,skills,tools}/…`, and `Context.StoragePath` asserts "must be under this tenant's directory" | cross-tenant file reads are blocked at the storage layer |
| At runtime entities are resolved per schema (agent → skills → tool grants → capability) | reference-style sharing would require redoing permission checks, cache invalidation and delete cascades |
| `platform` may not import `nexus/admin`; `nexus/**` may not import `platform` (only two lines in `cmd`) | packaging / install logic has to live in `shared/*`, shared by both sides |

Once installed it is the target tenant's own entity and evolves independently; `forked_from` is for provenance and the "upstream
has a newer snapshot" hint only, it creates no dependency.

## 2. The three units and their dependency closure

| Unit | DB rows | File tree | Dependencies |
|---|---|---|---|
| `skill` | `skills` (tenant-level: `agent_id IS NULL`) | `skills/<name>/` | `skill_tool_grants` (`ref` is a **string**: `custom:<name>` / group name / tool name) |
| `custom_tool` | `custom_tools` | `tools/<name>/` (`entry` + `lib/` + `templates/`…) | none; but it needs the `sandbox.*` and `net.egress` capabilities |
| `agent` | `agents` | `agents/<agent>/` (`AGENT.md` + private `skills/`) | `agent_skill_bindings` (uuid), `agent_tool_grants` (string), `agent_relations` (uuid), `config` |

P0 rules:

- only **one** unit per listing;
- an agent's tenant-level skill dependencies are **not inlined**; on install they are matched by name against the target
  tenant's existing skills, and misses go into `warnings`. Inline bundling is P1.
- **almost any entity can be listed / exported**: protected names (`main` / `skill-authoring` / `agent-authoring` /
  `custom-tool-authoring`), over-privileged agents and deprecated state are never blocked, only surfaced as `warnings`.
  Content and identity are handled separately — `agent_relations` do not travel with the package (they cannot be remapped
  across tenants), while the agent's definition attributes `is_supervisor` / `tools_unrestricted` / `skill_discovery` /
  `default_language` **do** (see §7, §12.1): they describe "how this agent runs", they take effect per package after install
  and are named in `warnings` rather than silently downgraded.
- **private skills** (`tenants/<schema>/agents/<agent>/skills/<name>/`) can **only** be listed / exported together with their
  owning agent:
  - travelling with the agent preserves ownership — the whole `skills/` directory goes into the package, metadata goes into
    `payload.private_skills`, and install rebuilds one row per skill (new id, `agent_id` = the new agent) together with its own
    `skill_tool_grants`;
  - **they cannot be listed / exported alone**: the body lives inside the agent directory, and the only landing place for a
    standalone package would be "promote it to tenant level" — ownership is lost forever and the visibility allowlist does not
    travel (the import result is "`restricted=true` with zero bindings → nobody can discover it"). The API rejects it outright
    and says "export the agent it belongs to"; to really share it, promote it to a tenant-level skill in the skill settings
    first.
- the install side likewise does not reject on protected names: collisions are absorbed by name conflicts (`suggested_name`),
  otherwise your own exported `main` / `skill-authoring` could not be installed back.

## 3. Where files land

| Stage | Path | Owner |
|---|---|---|
| List (read source) | `{root_dir}/tenants/<source_schema>/skills/<name>/` | source tenant |
| Snapshot (stored after listing) | `{root_dir}/platform-assets/<asset_id>/<build>/` | platform |
| Install (write target) | `{root_dir}/tenants/<target_schema>/skills/<installed_name>/` | target tenant |

- New constants `paths.PlatformAssetsSubdir = "platform-assets"` and `paths.PlatformAssetKey(assetID, build)`; `{root_dir}` is
  `storage.root_dir` (the platform data root, not a per-agent directory).
- The snapshot is the **single source of truth for distribution**: install copies from it and never reads back from the source
  tenant (cross-tenant reads are blocked by the storage assertion, and once the source tenant is suspended or deleted nobody
  could install any more).
- The snapshot directory mirrors the source directory (relative paths preserved), and install copies the whole tree.
- This prefix is **not inside any sandbox mount** (neither `/workspace`, `/agent` nor `tools/` covers it), `safePath` rejects
  `..`, and tenant-side path helpers cannot reach it by construction.
- Withdrawing / deleting an asset = `storage.DeletePrefix("platform-assets/<asset_id>")`; already-installed copies are
  unaffected (copy semantics).
- All reads and writes go through `shared/storage` `List / ReadFile / WriteFile / DeletePrefix` (works with local / nfs / s3);
  keys are logical paths separated by `/` — never call `os.Open` directly.

## 4. Data model

DDL: `nexus/platform/sql/shared_assets.sql` (idempotent, styled after `platform/sql/platform.sql`). Three tables:

| Table | Purpose |
|---|---|
| `platform.shared_assets` | one row = one listed snapshot (`payload` entity row snapshot + `manifest` dependencies and file list + who listed it) |
| `platform.shared_asset_tenants` | visibility allowlist (read only when `visibility='allowlist'`; a row = allowed) |
| `platform.shared_asset_installs` | who installed which version (the basis of P1's "upstream has a newer snapshot") |

Key conventions:

- **file content never enters the DB**, only the list in `manifest.files` (path / size / sha256);
- listing the same name again = overwrite the same row with `build + 1`, the new snapshot goes to a new directory
  `platform-assets/<id>/<newver>/`, and the old version directory is kept (for rollback and audit);
- `status`: `draft` (saved, not submitted) / `pending` (awaiting review) / `listed` / `rejected` / `delisted`. The tenant side
  can only do `pending ⇄ draft` (withdraw); everything else is platform-only;
- the lister is a **tenant-side account** (`{schema}.users`), not a platform user; the platform reviewer is `reviewed_by`
  (`platform.users`). The two must not be mixed.

### Lister fields

| Column | Purpose |
|---|---|
| `source_schema` | listing tenant (= schema) |
| `listed_by` | lister id (`users.id`), for audit |
| `listed_by_name` | **display-name snapshot** taken at listing time |

Only the name snapshot is stored; no cross-schema lookup into `users`: after a user is renamed or suspended, historical assets
must still be able to say who published them, and a lookup would make history drift.

External JSON (**JSON uses `schema`, only the SQL column is `schema_name`**, consistent with `tenantView` /
`createTenantRequest`):

```json
"listed_by": {
  "tenant": { "schema": "t3", "display_name": "R&D Team 1" },
  "user":   { "id": "0f3c…", "display_name": "Zhang San" }
}
```

- `tenant.display_name` is resolved live from `platform.tenants` (renaming a tenant shows through; that is a tenant property);
- `user.display_name` comes from the snapshot;
- `user.name` (login name) is deliberately not returned: it is not stored, and returning it would force a lookup.

Install records work the same way: `installer: { tenant: {schema, display_name}, user: {id, display_name} }`.

## 5. Listing (tenant side)

`POST /admin/share/listings`, body `{kind, ref}` (`ref`: id for skill / agent, name for custom_tool).

1. The caller needs `manage`; entity state is not gated (protected names / over-privilege / private ownership / deprecated only
   produce `warnings`, see §2).
2. Read the entity row through a whitelist of columns, never copying the whole row (`created_by` / `updated_by` / `endpoints` /
   build number do not travel).
3. **Strip sensitive values**:
   - `custom_tools.configs` is always emptied, keeping only the `config_fields` declaration;
   - `agents.config` is kept, but values whose key matches
     `(?i)(api[_-]?key|token|secret|password|credential|authorization)` are blanked and a warning is recorded.
4. **Package the files**: recurse the directory, capped at **2000 files / 64 MiB per file / 128 MiB total**; `SKILL.md`,
   `AGENT.md` and `custom_tools.entry` must exist, otherwise the listing is rejected. For `agent` the **whole** directory is
   packed (including `skills/` private skills); no subdirectory is skipped any more. The part of the dependencies that are
   **this tenant's own custom tools** is mounted into the same package as `_deps/custom_tools/<name>/**`
   (`PackOptions.Trees`) — otherwise installing in the target tenant would produce a grant row pointing at a tool that does
   not exist. `builtin:<x>` and groups ship with the platform and are not included.
5. **Dependency declarations** go into `manifest.deps` (declared only, not resolved into entities):
   - `skill` → `[{type:"tool", ref}]` (from `skill_tool_grants`)
   - `agent` → `{skills:[name], private_skills:[name], tools:[{type,ref}]}`
   - `custom_tool` → `{requires:["sandbox.exec"], egress:bool}`
6. Persist with `status='pending'` awaiting platform review — **but the default is no review, i.e. straight to `listed`**; only
   tenants explicitly set to "review required" enter `pending` (see below).

### Listing review policy (platform configurable)

Two levels, lenient by default (listing needs no review):

| Level | Storage | Semantics |
|---|---|---|
| Global default | `platform.listing_settings` (single row `id=1`) | no row = no review |
| Per-tenant override | `platform.listing_policies` (`schema_name` primary key) | a row wins; deleting it falls back to the default |

API: `GET /platform/listing-policy`, `PUT /platform/listing-policy` (global),
`PUT /platform/listing-policy/:schema` (`{require_review: true|false|null}`, `null` = delete the override).
UI: platform "local market → listing policy".

- without review (**the default**) a listing goes **straight to `listed`**; `listed_by` is still recorded and `reviewed_by` stays
  empty (nobody reviewed it by hand, so do not fake it);
- a tenant set to "review required" lands in `pending` and waits for the platform's verdict;
- re-listing follows the same policy: no review means straight to `listed` with the version incremented;
- it only affects "listing", **not "pulling from a remote"** — the latter follows `peer_servers.install_policy`, a different
  thing;
- when the policy cannot be read, treat it as "review required" (fail closed); switching back to review required does not
  retroactively affect already-listed content.

## 6. Review (platform side)

`POST /platform/assets/:id/review`, body `{action: approve|reject|delist, note}`.

Delete (`DELETE /platform/assets/:id`) is **not on the state machine**: delisting is "withdraw from the market but keep the
record", deleting is "remove entirely" — the two are parallel, not sequential, so a listed asset can be deleted directly; it
does not touch copies already installed by tenants. `pending` / `rejected` / `draft` are still refused — those are flows the
tenant has not finished (to dispose of them, reject them).

Entry point: the platform console (`desktop/src/platform`) → top bar "local market" (`views/AdminAssetsView.vue`), logged in as a
platform admin (`plat`, `platform.users.role=admin`). The state machine is one-way: `approve`/`reject` only apply to `pending`,
`delist` only to `listed`, and the UI greys out buttons based on the current state rather than letting a click fail; a rejection
must carry a reason, which is the only sentence the lister sees under "my assets". The same page can change visibility
(`all` ↔ `allowlist`, full replacement). To script it, call the API directly: `/platform/login` for a token →
`GET /platform/assets?status=pending` → `POST /platform/assets/:id/review`.

The review page lays out five checks:

1. are dependencies complete (did anyone list the `custom:<name>` tool too; do the declared skills exist by that name)
2. do `manifest.deps.requires` match the description (especially `net.egress`)
3. are file count / size at the cap; re-scan file content for the credential regex
4. does it squat on a protected name (a hint only, no blocking — protection only constrains delete / rename)
5. does the name conflict with an existing asset

## 7. Install (tenant side)

`POST /admin/share/install`, body `{asset_id, name?, status?}`.

**Order: files first, then DB** (file deletion is idempotent, DB rollback is more expensive).

1. Visibility: `status='listed'` and (`visibility='all'` or `shared_asset_tenants` has this tenant).
2. Pre-checks (any failure returns 400 immediately, no half-finished install):
   - the target tenant is `status='active'` and the caller has `manage`
   - quota: `quota.Resolve(schema)` still has room under `max_agents` / `max_skills`
   - capabilities: check `manifest.deps.requires` item by item against `platform.tenant_capabilities`; `egress` against
     `net.egress`
   - name: the original name by default, validated by `ValidName` and unique within the tenant; a conflict returns **409 +
     a suggested name** (`name-2`) and the frontend retries after confirmation (no silent rename)
   - protected names are not rejected separately (protection only constrains delete / rename); a collision goes through the
     "suggested name" branch above
3. Copy files: `platform-assets/<id>/<ver>/` → `tenants/<target>/…/<name>/`. The `_deps/custom_tools/<name>/**` entries in the
   manifest are the **bodies of dependency custom tools**; they do not land inside the asset's own directory but are installed
   under `custom_tools/<tool>/` with the prefix stripped (`assetpack.BodyFiles` splits bodies, `assetpack.Attachments` splits
   attachments, `RestoreFilesUnder` writes them with the prefix stripped).
4. Write the DB in a single transaction (schema-qualified + `pq.QuoteIdentifier`):
   - new uuid (`uuid.NewString()`); `created_by` / `updated_by` = the caller
   - `skill`: `agent_id = NULL` (always tenant-level), `restricted` **does not travel** (always `false`: an allowlist cannot be
     remapped across tenants, and `true` would only yield a skill "nobody is authorized for → nobody can discover it"),
     `status` defaults to 1
   - an `agent`'s **private skills**: insert one row per entry in `payload.private_skills` (new id, `agent_id` = the new agent,
     `status` defaults to 1) and rebuild `skill_tool_grants` as well; deprecated private skills are neither packed nor rebuilt
   - `custom_tool`: `configs = {}` (the tenant fills it in), `status` defaults to **1 (enabled)** — callable right after install,
     no longer disabled by default (disable it yourself if you want to re-check the script); on overwrite, `configs` /
     `created_by` / `status` keep the target's existing values (configuration is credentials, and the packaged value is empty)
   - **tools shipped inside the package** (`_deps/custom_tools/**`): installed after the agent / skill, also enabled; if the
     target already has a tool with that name it is **not overwritten** (local script and config win), and if `sandbox.exec` /
     `net.egress` is missing it is skipped — all of this only produces `warnings`, never a failed install
   - `agent`: `is_supervisor` / `tools_unrestricted` / `skill_discovery` are **set from the package** (old assets without those
     fields → `false`; an overwrite import keeps the target's existing values), and so is `default_language` (a BCP-47 string;
     an empty string means "not configured", and an old asset without the field keeps the target's existing value), `status`
     defaults to 1 (only 1 / 3 accepted)
   - grant rewrite: `skill_tool_grants` / `agent_tool_grants` are rebuilt entry by entry; `agent_skill_bindings` bind only
     "tenant-level skills with the same name in the target tenant that are not deprecated", the rest are dropped (private
     skills belong to their agent by construction and need no binding)
   - `agent_relations` are **not copied in P0** (they express "who may call whom", which cannot be remapped across tenants)
   - agents and workflows share one namespace: a workflow with the same name in the target tenant is handled as a name conflict
     too (409 + suggested name)
   - an agent's skills are matched **by name** (`skills.agent_id IS NULL` tenant-level skills): a missing same-name skill, or a
     same-name skill that is deprecated → skipped with a `warnings` entry; `agent_tool_grants` are fully replaced and rebuilt
     from `{item_type, ref, grant_kind}`, with a missing `grant_kind` defaulting to `allow` (old assets may not record it)
5. Everything dropped / rewritten goes into `warnings` (the same convention as `contract.warnings` and the directory `issues`).
6. Write one `shared_asset_installs` row.
7. DB failure → delete the files written in step 3; if both fail, log and report an error, never leave a half-broken state.

Response: `{ok, installed:{name,id}, warnings:[...], skipped_files:[...]}`.

### 6.1 Two things that differ for custom tools

1. **The contract lives in files** (`TOOL.json`, see `models.ToolFile`). So install is "restore the files first → read the
   contract back from the target directory → use it to generate the table row", not copying fields from the payload. When
   installing under a new name, the `name` inside the contract is rewritten to the new name before writing back, otherwise
   `Validate` keeps reporting "name does not match the directory name".
2. **Enabled by default** (`status=1`): callable right after install; if you think the script needs a review first, disable it
   yourself afterwards. `configs` is always empty — configuration values must be supplied by the consumer (calls fail until
   then, and install names it in `warnings`).

Capability pre-checks happen before install and a missing capability is a 400 (not a warning): a tool without `sandbox.exec`
cannot run at all after install, and the same goes for egress tools without `net.egress`.

## 8. API draft

Tenant side (`nexus/admin/assets/`, prefix `/admin/share`):

| Method | Path | Purpose |
|---|---|---|
| GET | `/admin/share/assets?kind=&q=` | installable list (listed + visible) |
| GET | `/admin/share/assets/:id` | details (dependencies, warnings, file list, whether installed) |
| POST | `/admin/share/listings` | list `{kind, ref}` |
| GET | `/admin/share/mine` | what I listed (with status and rejection reason) |
| POST | `/admin/share/mine/:id/withdraw` | withdraw (pending→draft) or delete a draft |
| POST | `/admin/share/install` | install |
| GET | `/admin/share/installs` | install records (including the "newer version available" flag) |

Platform side (`nexus/platform/handler/assets.go`, prefix `/platform/assets`):

| Method | Path | Purpose |
|---|---|---|
| GET | `/platform/assets?status=&kind=&q=&page=&page_size=` | review list (pagination same as `/platform/tenants`: `page` starts at 1, `page_size<=0` means no paging, cap 100, `total` is the filtered total) |
| GET | `/platform/assets/:id` | details (payload preview, file list, dependencies, warnings, lister) |
| POST | `/platform/assets/:id/review` | `approve` / `reject` / `delist` |
| DELETE | `/platform/assets/:id` | hard delete: `listed` / `delisted` are deletable directly, **no delisting required first**; `pending` / `rejected` / `draft` are refused |
| GET/PUT | `/platform/assets/:id/visibility` | `all` / `allowlist` + adding/removing allowlist entries |

Each list row shows **listed by Zhang San · R&D Team 1**, with `user_id` and the listing time on hover; the frontend concatenates
`listed_by.user.display_name` + `listed_by.tenant.display_name`, the server never returns a pre-joined display string.

Console entry: top bar "assets" (`desktop/src/tenant/components/AssetMarket.vue`, API config in `config/admin.js`'s `share`). Its
three tabs map exactly onto the APIs above: asset market (`/assets` list + details + install), my listings (`/mine` + withdraw +
listing dialog), install records (`/installs`).

## 9. Code locations (constrained by the boundary rules)

- `nexus/shared/assetpack/`: packaging / unpacking / dependency resolution / ID remapping / schema-qualified IO. Depends only on
  `shared/{db,storage,paths,models,capability,quota}`.
- `nexus/admin/assets/`: the 7 tenant-side endpoints.
- `nexus/platform/handler/assets.go`: review and visibility.
- Both sides call `assetpack` and neither imports the other; the allowed-dependency list in `platform/README.md` must add
  `shared/assetpack`.

## 10. Phases and acceptance

**P0**: the three tables + `assetpack` + the APIs above + two pages (tenant "list / install", platform "review").

Acceptance:

1. publish a skill → review → tenant B installs it: `SKILL.md` and grants are there, `configs` is empty.
2. publish a script tool → after install it is **enabled**, `config_fields` is complete, values must be filled in by hand.
3. publish an agent → after install the three switches follow the package, skill bindings are matched by name, missing
   dependencies produce `warnings`.
4. install the same name a second time → 409 with a suggested name.
5. unreviewed assets (`pending` / `rejected` / `delisted`) cannot be installed.
6. snapshot and installed copy are independent: after deleting the asset the target tenant's skill still works.

**P1**: build number + "upstream has a newer snapshot" (hint only, re-import, no diff merge), finer allowlists, bundles (agent
inlining skills/tools).

**P2**: marketplace features (search / categories / ratings), dependency lock files.

## 11. Explicitly out of scope

Cross-tenant references, two-way sync, automatic upgrades that overwrite tenant changes, cross-tenant `agent_relations`, and
carrying the **values** of `custom_tools.configs`.

## 12. Moving across servers (manual package, P0)

The goal is "an asset reviewed on the test machine can also be installed on production". The approach is that the **consumer
pulls / receives**, not that a remote user lists into this instance:

- listing always happens on the **lister's own machine** (local tenant + local platform review); the only cross-server action is
  "export → import";
- remote users may not call `/admin/share/listings` directly: `listed_by` points at a local `{schema}.users.id` and
  `source_schema` points at a local `platform.tenants` row, neither of which a remote account has, so the insert would produce a
  row whose lister cannot be looked up;
- no runtime remote references: a peer going down must not affect assets already imported here.

### 12.1 Package format

The `platform-assets/<id>/<build>` snapshot is packed into a zip (`archive/zip`, standard library, cross-platform):

```
package.json   { format: "flame-asset/1", exported_at, origin:{server,asset_id,source_schema,build,listed_by},
                 manifest:{kind,name,entry,deps,files[]}, payload:{…} }
files/<rel>    every file in the snapshot (relative paths preserved)
```

The package **does not contain** grant rows or `agent_relations` — they cannot be remapped across environments. But
`manifest.deps.agents` records the **list of names** of "whom this agent may call" (taken from `agent_relations` at export time):
grants are not migrated, yet dependencies must be checkable before install (see `/import/inspect` in §12.5).

An agent's three switches (`is_supervisor` / `tools_unrestricted` / `skill_discovery`) **are in the package** (same-named fields
in `payload`, always written as explicit booleans): they describe "how this agent runs inside a tenant", and without them the
target tenant would get a copy that behaves differently. Import applies them from the package and says so in `warnings` ("set as
supervisor per package"); when an older package lacks these fields, an overwrite import keeps the target's existing values and a
fresh install uses `false` (you cannot assert `false`, or one overwrite would silently downgrade).

`payload.default_language` travels the same way and for the same reason: it is part of the agent's definition (frozen into a
release), so a copy installed without it would answer in a different language than the author configured. It is a BCP-47 string
rather than a boolean — an empty string means "not configured", and an older package that lacks the field entirely keeps the
target's existing value on an overwrite.

### 12.2 The import side (trust nothing upstream)

`POST /platform/asset-imports` (multipart: `file` / `origin?` / `force?`, platform admins only) does, in order:

1. upload cap 128 MiB, then apply `assetpack.DefaultLimits` again after extraction (2000 files / 64 MiB per file / 128 MiB
   total);
2. path safety: reject absolute paths, `..`, backslashes and non-normalised `a/../b` (the usual extraction traversal tricks);
3. manifest and content must correspond exactly: count, byte sizes and `sha256` all matching — any mismatch **rejects the whole
   package** (no partial import);
4. `assetpack.ValidateName`: the name must be usable as a directory; protected names are no longer rejected (see §2);
5. **re-run `StripSecrets`**: never trust the pass upstream did (their version may be older / mutated / missing altogether);
6. write to `platform-assets/<new local uuid>/<ver>/`, reclaiming with `DeletePrefix` on failure;
7. insert the `shared_assets` row: `status = pending` (**their review does not mean our approval**), `visibility = all`, with
   `origin_server` / `origin_asset_id` / `imported_at` / `imported_by` for provenance.

Deduplication and overwrite: the unique index is upgraded to `(origin_server, kind, source_schema, lower(name))`, while locally
listed rows keep `origin_server = ''` and stay unique. Import looks up an existing row by the same key:

- none → new uuid, version taken from the package's `origin.build` (`< 1` counts as 1);
- exists and no `force` → 400 whose `msg` states "source + name + current build" and whose `data.existing` carries
  `{id, build, status}`: the UI pops one confirmation, and **no checkbox has to be ticked in advance** (forgetting it would only
  waste a round trip);
- with `force` → **reuse the row id** (install records keep pointing at the same asset), version = `max(local build + 1, packaged
  build)` (the local build only grows), replace the content and go back to `pending`, reporting "overwritten / bumped to vN".

An imported row's `source_schema` is "a tenant on the other machine", and a same-named schema here is pure coincidence, so
display uses `origin.server` instead of the tenant name (the `origin` field; locally listed rows return `null`).

### 12.3 Pulling from a peer (P1, implemented)

This replaces "the package comes from a manual upload" with an HTTP pull; validation and persistence are **the same code**
(`importAssetPackageBytes`).

**Producer egress** (the consumer reads it; token auth, unrelated to platform login identity)

| Method | Path | Notes |
|---|---|---|
| GET | `/platform/market/catalog?kind=&since=` | lists `listed` only; `since` (RFC3339) is for incremental |
| GET | `/platform/market/assets/:id/package` | serves packages for `listed` only, consistent with the catalog |

Two egress rules: only `listed` (local review approval is the sole precondition for peer visibility); and only **locally
originated** rows (`origin_server = ''`) — imported rows are not forwarded, otherwise A→B→A would form a cycle, the same asset
would be copied over and over, and provenance could be forged by renaming. The package endpoint likewise 404s for imported rows,
otherwise guessing an id would bypass this restriction.

Because of the second rule, configuring a peer to point at this machine (`localhost` self-testing) is **allowed**: the row pulled
from yourself has a non-empty `origin_server`, never appears in the egress catalog, and converges in one round instead of
self-propagating. If forwarding imported rows is ever allowed, the "a peer may not point at this machine" check must come back at
the same time.

Tokens: `platform.market_tokens` (`name` + `token_hash` + `enabled` + `last_used_at`); the plaintext `flmt_<64hex>` is
**returned only once at creation**, and the database holds only `sha256`. Revoking means deleting or disabling;
`/platform/market/*` all goes through `MarketTokenMiddleware` and can never obtain platform admin rights.

**Open market (optional: no token)**

A peer may skip token validation — suitable for intranets, or deployments where the assets are meant to be public anyway. The
switch is in the admin UI: at the top of "remote market → market egress token", and it is **not in config.yaml** (why below):

| Method | Path | Notes |
|---|---|---|
| GET | `/platform/market-settings` | `{open, updated_at?, updated_by?}` |
| PUT | `/platform/market-settings` | `{open}`: change the egress policy |

Stored in `platform.market_settings` (single-row table, `id` always 1). **No row = off**: forgetting to configure must never mean
wide open, and fail-closed does not rely on seed data being materialised. Note the path is `market-settings`, not
`market/settings` — the whole `/platform/market/*` segment is authenticated by market tokens, so an admin entering there gets a
401.

Why not in the config file: this is a governance decision, and whoever changes it should be an administrator holding a platform
account, not "someone who can log into the server, edit a file and restart". Precedents for this class of decision are
`capability_gates` / `plugin_gates` — they also live in the database, not in config.yaml.

Once open, `/platform/market/*` accepts requests without `Authorization` and the catalog response returns `token` as
`anonymous`. Two rules must be kept apart:

- **a token supplied but invalid** is still 401 — open does not mean unvalidated. A mistyped token should surface at
  integration time, otherwise "configured a token but it never took effect" is impossible to diagnose;
- `market_tokens` is no longer the only entry point, so `last_used_at` only covers the token-bearing part;
- the middleware looks it up per request (no in-process cache): with multiple instances a cache would keep a "just switched off"
  flag alive for a while, and this switch is a security-relevant one; `/platform/market/*` is low-frequency anyway.

On the consumer side (`platform.peer_servers.token`) an empty string means "the peer is open": outbound requests carry no
`Authorization`. The API expresses this state as `auth_mode` (`token` | `none`), which is derived from the token and not stored
separately (an extra column would allow self-contradictory rows like "mode is token but the token is empty"):

- `POST/PUT /platform/peers` with `auth_mode: "none"` → clear the token. It must be passed explicitly, because the existing
  semantics of an empty `token` in a PUT is "do not change", so there would be no way back to an open market;
- the response returns both `auth_mode` and `has_token` (the latter for older frontends).

**Consumer entry points** (platform admins)

| Method | Path | Notes |
|---|---|---|
| GET/POST | `/platform/peers` | peer list / create (`name`, `base_url`, `auth_mode`, `token`, `install_policy`) |
| PUT/DELETE | `/platform/peers/:id` | change config (`token` empty = unchanged; `auth_mode: none` = clear) / delete |
| GET | `/platform/peers/:id/catalog` | proxy the peer's catalog, marking whether it is already imported locally and the local version |
| POST | `/platform/peers/:id/import` | `{asset_id, force}`: pull the package → run the §12.2 import pipeline |

- `platform.peer_servers` stores the **outbound** token in plaintext (outbound calls need it) and only echoes `has_token` in admin
  APIs, never the plaintext again;
- `install_policy` defaults to `review`: imports are always `pending`. Set to `auto` and they go straight to `listed`, but
  `origin_*` and `imported_by` are still recorded and the warnings say "auto-approved this time";
- pulling is deliberately **two steps** (catalog first, then import one by one), with no batch mode: one import at a time keeps
  warnings visible per item, and overwriting is a human decision (`force`);
- `origin_server` uses the peer's site name (`peers.name`), so the same asset imported from two sites coexists as two rows and
  they do not overwrite each other (the unique key includes `origin_server`).

**Tenant-side entry (pull-through, the platform does not import on their behalf)**

The platform only decides "which peers are visible to which tenants" (`visible_to` / `tenant_schemas`) and does not import one by
one for tenants:

| Method | Path | Notes |
|---|---|---|
| GET | `/admin/share/remote-assets?peer=&kind=` | peer catalogs visible to this tenant; each entry carries `local{asset_id,build,status}` / `installed` / `stale` |
| POST | `/admin/share/pull-asset` | `{peer, asset_id, force}`: pull on demand → the same import pipeline |

- the request body carries the peer **name**, not a URL: addresses come only from platform config and tokens stay server-side, so
  the browser neither talks to the peer directly nor can obtain an `flmt_…`;
- pulls are deduplicated **per asset** (`sharedassets.FindByOriginAsset`): several tenants pulling the same one download it
  once, then install it separately; `force: true` skips the fast path (used by "pull a new version"), and the overwrite decision
  is left to Import;
- the fast path trusts the peer's asset id only; **name collision checks** still go through `assetimport.FindByOriginName`
  (origin + kind + source schema + name). A peer's "withdraw then re-list" produces a new id, which lands in a name conflict:
  the API then returns `400 + data.conflict{name,build,status,asset_id}` and the frontend retries with `force: true` after
  confirmation — the `ConflictError` copy only states facts (source / name / current build) and never instructions like "tick a
  checkbox" that only hold in one particular screen; the tenant side composes its own "overwrite" prompt;
- the stored status follows the peer's `install_policy`: `auto` → straight to `listed`, the frontend goes on to install; `review`
  → `pending`, appearing in the asset market only after platform approval;
- catalog caching 60 s, caching failures too (negative cache); an unreachable site only makes that site return an error and never
  affects local assets or other peers;
- frontend: the "asset market → remote market" tab in the tenant console, grouped by peer, with button labels following the local
  state (pull and install / awaiting platform review / install / pull new version).

### 12.4 Seeing "which market this came from" on the tenant side

A tenant always has exactly one market (the local `platform.shared_assets`); peers are sources. After connecting several peers,
`origin_server` is what distinguishes them:

- list API `GET /admin/share/assets?kind=&q=&origin=`: an empty `origin` means all sources, `local` means locally listed only, any
  other value means only rows imported from that peer;
- the response additionally carries an `origins: [{server, label, count}]` summary (**ignoring the origin filter**, other filters
  unchanged) so the frontend can render a "source" dropdown directly — otherwise selecting `test` would make the other sources
  disappear from the options;
- frontend copy: when `listed_by.origin.server` is present, show **"source: test site"** (with the import time in the details)
  instead of calling the remote site the "lister";
- the platform review list works the same way (`GET /platform/assets?...&origin=`), so "listed locally" and rows imported from
  each site are distinguishable at a glance.

A site name is an identity: when you move machines, change the name, or just disable it (`enabled=false`) — do not delete it and
reuse the name.

### 12.5 Tenant-built packages: card export / list import (no platform involved)

The sections above all require "list first, then review, then distribution by the platform or a market". But often a user simply
wants to **copy one of their own agents / skills / workflows / tools to another machine**, without platform review and without
adding a row to the market.

Two endpoints do exactly that, shared by all four asset kinds:

| Endpoint | Notes |
|---|---|
| `GET /admin/share/export?kind=&ref=` | pack and download a zip on the spot (`ref`: id for skill / agent / workflow, name for custom_tool) |
| `POST /admin/share/import` (multipart: `file` / `name?` / `status?` / `kind?` / `overwrite?`) | upload a zip and install it as a tenant entity |
| `POST /admin/share/import/inspect` (multipart: `file` / `name?` / `kind?`) | **pre-check before installing (read-only)**: parse the package and return the dependency list, never writing anything |

Differences from §12.2:

- **neither `platform-assets/` nor `shared_assets` is touched**: export writes the content to this tenant's `.export-tmp/<uuid>`
  and turns it into a zip, deleting as it goes; import lays the package out in `.import-tmp/<uuid>` and deletes it when done.
  Neither side has a platform asset row, so nothing enters the market, the asset list, or the install ledger.
- **packaging is the same source as listing**: `planFor(kind, info, ref)` is the assembly logic shared by listing and export
  (payload whitelisting, dependency declarations, `StripSecrets` redaction), so "export then import" yields the same thing as
  "list then install" and cannot drift over time.
- **validation and install are the same source too**: the package is fully validated by `assetpack.ReadPackage` (size / paths /
  manifest / sha256), then `assetimport.ValidatePackage` judges kind / name / entry file; install goes through `installFrom`
  (the same function as market install), so name conflicts, quotas, capability pre-checks, default status and warnings are all
  identical.
- **there is almost no entity that "cannot be exported / listed"**: protected names (`main` etc.), over-privileged agents and
  deprecated state can all take these two paths, only producing `warnings`; the three switches travel with the package, while
  `agent_relations` and `endpoints` do not. The sole exception is a **private skill**: its body lives in
  `agents/<agent>/skills/<name>`, so once separated from the owning agent the only landing place is "promote to tenant level"
  (ownership lost forever, the visibility allowlist not travelling, and importing it back yields either something nobody can
  discover or an orphan copy with the same name as a private skill) — so `export` and listing both reject it, saying "export the
  agent it belongs to". A private skill travelling with its agent is complete (`payload.private_skills` + the file tree).
- `kind` is an assertion of "what the user thinks they are importing": importing a skill package inside the agent page is
  rejected outright, so nobody ends up with something they cannot find afterwards.
- **dependency pre-check** (`/import/inspect`): the same package-reading validation as import (the same
  `readUploadedPackage`), but read-only. It returns `deps[]`, each with `kind` (skill / tool / agent / capability), `scope`
  (tool breakdown: custom / mcp / rpc / local / builtin / plugin / group / preset), `state`
  (ok / bundled / disabled / deprecated / missing), `via` (`skill:<name>` = a transitive dependency declared by a skill),
  `items` / `total` (members expanded from a group reference) and `detail`; plus `existing` (same-name and whether it can be
  overwritten), `bundled` (tools / private skills shipped in the package) and `notes`.
  Conventions: group references consult the **group-level** state of `toolgrant.Catalog.Groups()` (which distinguishes
  "connection does not exist" from "connection exists but is disabled"), single tools consult the leaves of `ToolsOfRef`; a
  bundled `custom:` counts as `bundled` rather than missing; an agent's callees come from `manifest.deps.agents` (names taken
  from `agent_relations` at listing time, grant rows still not travelling). Runtime dynamic choices (the model deciding on the
  fly which skill / tool to use) cannot be decided statically, which `notes` says explicitly.
- **market install and upload import follow the same rules**:
  - pre-check before install: `POST /admin/share/install/inspect` (read-only, `{asset_id, name?}`) shares the criteria with
    `/import/inspect` — both use `resolveInstallTarget` and return
    `{kind,name,deps,summary,missing,existing,bundled,notices,notes}`;
  - same name: by default return `suggested_name` + `can_overwrite=true`, the frontend pops one confirmation and retries with
    `overwrite=true`; overwrite semantics are the next bullet (common to all four kinds);
  - release: only when "release a version right after a successful install" is ticked (**off by default**); a failure only goes
    into the install notices and never rolls back the install;
  - identical UI: both entry points share the form order (install as → pre-check → post-install state → release now), the same
    pre-check rendering and result page components (`AssetDepCheck` / `AssetInstallResult`), differing only in "where the content
    comes from" (pick a file vs pick an asset); failures are always inline and retryable in place, and the result page stays until
    "done" is clicked.
- **overwrite by name (all four kinds)**: `POST /admin/share/import` also accepts `overwrite=1`. By default a name clash still
  returns `suggested_name` (rename, leaving the existing entity alone); with `overwrite=1` the existing entity is overwritten.
  Common rules: "identity / the target tenant's grants and external-exposure configuration / version history" are always
  preserved, and only the content brought by the package is replaced.
  - agent: the file tree is laid down again (files unique to the old version do not linger); config / private skills / skill
    bindings / tool grants / the three switches are **fully replaced** from the package; the only value kept from the target is
    `endpoints` (external-exposure configuration belongs to the target tenant and is not in the package). Downgrading "the last
    supervisor in this tenant" is refused by the repository — that is protection; the original value is kept and a warning is
    issued.
  - skill: the file tree is laid down again, `description` / `metadata` / `user_invocable` / `command_*` are replaced from the
    package, `skill_tool_grants` are **fully replaced**; `restricted` + `agent_skill_bindings` (visibility grants) and `status`
    (lifecycle) **keep the target's existing values**, and the `versions/skills/<name>/` history is not cleared. When the target
    ends up "`restricted=true` with zero bindings" it self-heals to visible-to-all (otherwise nobody could discover it after the
    overwrite).
  - custom tool: the whole `custom_tools/<name>/` is deleted before laying down the new one (old scripts / data files do not
    linger) and `TOOL.json` contract fields are replaced from the package; `configs` (configuration is credentials), `created_by`
    and `status` **keep the target's existing values**, and the `versions/tools/<name>/` history is not cleared. Bundled tools
    still follow "skip if a same-name tool exists" and are never overwritten.
  - workflow: only the draft row changes (diagram / arguments / outputs / variables / limits / description); `status`,
    `endpoints`, breakpoints and `flow_versions` are **all preserved**; **production still runs the released version**, and the
    warning says so ("release a new version for it to take effect"); a target in deprecated state is rejected outright
    (`UpdateFlow` does not allow changing a deprecated workflow).
  - the frontend pops one confirmation first (stating the replacement scope per kind) and then retries with this field.
  - **you can release a version right after importing**: the import dialog has a "release a version right after a successful
    import" checkbox, **off by default**. It releases the current draft (through each kind's own version endpoint); a failure does
    not affect the import result and only produces a notice; disabled or deprecated entities cannot be released (the API refuses
    and explains why).
  - overwriting does **not** clear the entity's version domain (the history belongs to the entity being overwritten); leftovers
    are only cleared when creating a new same-name entity.
  - known trade-off: if the target's same-name entity is a **private skill** it is not a conflict (`FindByName` only looks at
    tenant-level skills) and a tenant-level skill is created; that agent still prefers its own private copy when resolving by
    name.
- export notices (which credentials were blanked, which private skills were carried along) go into the response header
  `X-Export-Warnings` (each entry URL-encoded, `|` separated); the frontend decodes them and lists them one by one in a dialog.
