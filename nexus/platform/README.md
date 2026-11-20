# nexus/platform — the platform admin console (multi-tenant management)

[中文](README.zh.md) | English

Platform identities (admin / user) are completely independent of the tenant console: the former live in `platform.users`, the
latter in each tenant's `<schema>.users` — **two tables with the same name that share nothing**, not even their vocabulary
(`admin` / `user` on the platform side, `manage` / `service` on the tenant side). Platform login asks for no tenant; after
signing in, `role` from the token decides the branch:

| role | screens | data surface |
|---|---|---|
| `admin` | admin pages (tenant management + platform users) | all of `/platform/tenants*`, `/platform/users*` |
| `user` | user pages (my tenants) | `/platform/me/tenants` (read, `owner_user_id = self`) + `/platform/me/tenants/:schema/status` (start / stop the tenants they own) |

## Layout

```
nexus/platform/
├─ README.md                      this file (boundary rules)
├─ router.go                      mounts the /platform group (called by nexus/cmd/router.go)
├─ auth/                          platform identity: role vocabulary, token issue / parse, the two middlewares, protected-admin name
├─ handler/                       HTTP layer (thin): login / me / tenants / users
├─ repository/{tenants,pmusers}/  data access for the platform schema
├─ provision/                     provisioning orchestration + template/ pack (//go:embed, must stay under this package)
│                                 template/schema.sql  tables
│                                 template/seed.sql    main + protected skills + grants / bindings
│                                 template/files/      definition-domain files (AGENT.md / SKILL.md)
└─ sql/                           platform.sql (the two platform tables, run by a DBA)
```

## How it is mounted

Two lines in `nexus/cmd/router.go`:

```go
platformGroup := router.Group("/platform")
platform.SetupRoutes(platformGroup)
```

This package has **no startup bootstrap**: the first platform admin (login name fixed to `plat`) is part of the environment's
initial data, so `nexus/cmd/main.go` no longer calls any platform init function.

## Boundary rules (bind every change in this directory)

**Allowed dependencies**

- `flame/nexus/shared/*`: `config`, `db`, `account`, `tenants`, `quota`, `flowconfig`, `storage`, `workspace`, `repository/flowconfigs`
- `flame/tools/*`: `xhttp`, `xjwt`, `xlog`

**Forbidden dependencies**

| Forbidden | Why / what to use instead |
|---|---|
| `nexus/admin/**` | that is the tenant console's handler; "create the first tenant admin", which provisioning needs, is an INSERT written here |
| `nexus/service/**` | tenant runtime logic; for directory conventions use `nexus/shared/paths` |
| `nexus/shared/auth` (the whole package) | it holds the tenant login handler (and depends on gin). For passwords / status use `nexus/shared/account` |

The reverse holds too: **`nexus/**` must not import `nexus/platform/**`**, the only exception being those two mounting lines in
`nexus/cmd`.

> Why this lives inside the `nexus` module instead of being a module of its own at the repository root:
> `nexus/` is the module `flame/nexus`. A root-level platform module would have to `require flame/nexus` (it uses the shared
> packages), while `nexus/cmd` would have to `require` the platform module in order to mount it ⇒ **the modules require each
> other in a cycle**. A root-level module is only possible once the platform is **its own process** (the dependency then points
> one way, platform → nexus, but "suspending a tenant takes effect immediately" degrades to taking effect after a TTL, or has to
> move to LISTEN/NOTIFY).

## Identity walls (two locks)

| Token arriving | Result |
|---|---|
| platform token → `/admin/*` | no `schema` → the tenant-side `parseClaims` rejects it outright |
| tenant token → `/platform/*` | no `scope` → `PlatformAuthMiddleware` rejects it |
| `user` token → `/platform/tenants*`, `/platform/users*` | `RequirePlatformAdmin` → 403 |

## Changing your own password

`POST /platform/me/password` (body `{old_password,new_password}`): you change your own password with the old one. It only
touches yourself, **issues no new token and does not invalidate sessions on other devices** (same semantics as the tenant-side
`/admin/me/password`). Resetting someone else's password still goes through `POST /platform/users/:id/password` (admin only).

> The password the initial data gives `plat` is a temporary value: change it with this endpoint right after the first login.

## Users managing the tenants they own

A platform "user" has two self-service write paths (both under `/platform/me/...`, **neither in the admin group**):

| Endpoint | Purpose | Permission |
|---|---|---|
| `PUT /platform/me/tenants/:schema/status` | start / stop (body `{status: active\|suspended}`) | `owner_user_id == self`, otherwise **403**; unknown tenant **404** |
| `GET/PUT /platform/me/tenants/:schema/flowconfigs` | read / write the **flow runtime parameters** of a tenant they own (`GET` returns `{system, tenant}`) | same |
| `POST /platform/me/tenants/:schema/primary/password` | reset the password of that tenant's **root account `primary`** (body `{password}`) | same; a missing `primary` row **404** |

- Admins use the `/platform/tenants/:schema/...` group (any tenant); both paths **share one validation core**
  (`loadFlowBundle` / `saveFlowConfigs` in `handler/flowconfigs.go`), so their criteria cannot drift.
- The owner check lives in `mustOwnTenant` (shared by status and flow parameters); any violation is 403 `{error:"forbidden"}`.
- Status only allows `active ⇄ suspended` (`pending` / `deleted` are internal states, writable from neither side); the flow
  fields follow the same rules as the tenant-side `/admin/flowconfigs`: all 7 may be empty = fall back to the system level,
  `-1` = no limit, and **must not exceed the system level**.
- Cache invalidation after a write: status → `sharedtenants.Invalidate(schema)`; flow → `flowconfig.Invalidate(schema)`.
  **Both take effect immediately** (a suspended tenant's logins and MCP/RPC resolution are refused at once). A password reset
  has no cache to invalidate.
- The **root account password** path only runs `UPDATE <schema>.users SET password` (`WHERE name = primary`); role, status and
  display name are untouched — the same treatment `primary` gets on the tenant side (`updateUser` refuses to modify it, and the
  **password reset is deliberately kept**). It solves "the initial password the platform delivered was forgotten": the tenant
  side cannot get in (a password change needs the old one) and the platform console does not touch tenant `users`. It issues no
  token and invalidates no session (to kick someone off immediately, suspend the tenant).
- An admin is never an owner (`CreateTenant` / `UpdateTenant` both require the owner to be `role=user`), so the two paths do
  not overlap.

## Paging the tenant list

`GET /platform/tenants?q=&status=&page=&page_size=`: `page` starts at 1; a missing `page_size` or `<=0` means **no paging**
(everything is returned, for old callers), capped at 100. The response is `{items,total,page,page_size}` where **`total` is the
filtered total** (not the size of this page); the frontend pager must use it.

> Each row costs three usage COUNTs (`quota.CountAgents/Skills/Flows`), so a larger `page_size` is more DB pressure.

## Creating a tenant: schema auto-discovery + a fixed login name

- `GET /platform/tenant-schemas/next` → `{schema}`: a suggestion, computed as **`t` + 0, 1, 2 …** (`t0`, `t1`, `t2` …), taking
  the first free one and skipping system-reserved names. The path uses its own static prefix `tenant-schemas`:
  `/tenants/next-schema` would sit at the same level as `/tenants/:schema` and could collide.
- `POST /platform/tenants`: an empty `schema` = the backend discovers one by the same rule (the console does not let you type
  it); an empty `admin_name` = `provision.DefaultAdminName` (`primary`). Explicit values still win, for scripted callers.

> `primary` is a **protected account on the tenant side**: `updateUser` in `nexus/admin/users` refuses to modify it (profile /
> role / status), and the tenant side **has no delete-user endpoint** — that is what keeps "the account the platform delivered"
> a valid, loginable `manage`. The constant has one source, `shared/tenants.PrimaryUserName` (platform and admin do not import
> each other, so this is the only thing they can share).
- **Occupancy** (`provision.SchemaAvailable`) needs both facts to say "free": no schema of that name in the database
  (`information_schema.schemata`) **and** no row in the registry (including suspended / soft-deleted ones). Checking only one of
  them runs into `CREATE SCHEMA` (an orphan schema, or a registry row whose schema was already DROPped).
- **It is checked again before writing**; if something is taken between the check and `CREATE SCHEMA` (PG `42P06` counts too),
  `Provision` returns `ErrSchemaTaken` → the endpoint answers **HTTP 409 + `{error:"conflict", message}`**, the frontend says
  "already taken", discovers a new value and **keeps the dialog open** until the operator confirms and provisions again (**the
  schema is never changed silently**).

## Flow runtime parameters (tenant row)

`GET /platform/tenants/:schema/flowconfigs` → `{system, tenant}` (the system level is read-only, for comparison); `PUT` on the
same path writes only the `platform.flow_configs` row with `tenant_id = :schema`, validated by exactly the same criteria as the
tenant-side `/admin/flowconfigs` (a tenant value must not exceed the system level). Implemented in `handler/flowconfigs.go`,
reusing `shared/repository/flowconfigs` directly (no `nexus/admin/**`).

> The tenant runtime reads a TTL cache in `shared/flowconfig`; the platform side calls `flowconfig.Invalidate(schema)` after a
> write, so **the next read in this process sees it** (the TTL only covers "changed the database behind the API's back" and
> multi-instance deployments).

## System settings

The first two domains are **system rows** with `tenant_id = ''` (global default / hard ceiling); the third is a globally shared
preset table. All admin-only:

| Domain | Endpoint | Semantics |
|---|---|---|
| Quota ceiling | `GET/PUT /platform/system/quota` | also a **hard ceiling**: at runtime `min(tenant value, system value)`, so a higher tenant row has no effect |
| Flow runtime parameters | `GET/PUT /platform/system/flowconfigs` | both the **fallback** when a tenant has not set anything and the **hard ceiling** on tenant values (runtime `clampMax` takes the min) |
| LLM provider presets | `GET/POST /platform/system/llm-provider-presets`, `PUT/DELETE /platform/system/llm-provider-presets/:id` | full CRUD on `platform.llm_provider_presets` (tenants read only; picking a preset is **copy-and-fill**, there is no foreign key, deleting one does not affect tenants) |

Both `GET`s of the first two domains also return `fallback` (the value baked into the code) so the UI can say "leave it empty
and you get this": `flowconfig.Fallback()` / `quota.Fallback()`, which keeps the frontend from hardcoding a number that drifts
from the shared package.

The write endpoints of the first two domains only validate "the value is `-1` (no limit) or non-negative"; there is **no
"not above the parent" check** (they *are* the parent).

The preset domain: `id` is **generated by the program** (`uuid.NewString()`, there is no DB default), length is validated
against the column widths (name 100 / icon 255 / protocol 50 / url 1024), `protocol` has no CHECK (the frontend offers
vertexai / anthropic / googleai / openai plus custom values); `extra_params` takes no part in read or write (a new row gets the
table default `{}`, an edit leaves the column alone).

Implemented in `handler/system.go` and `handler/llmpresets.go`; the frontend is the "system settings" page in the console's top
bar (domain navigation on the left, cards on the right).

## Tenant quota: raw value vs effective value

`GET /platform/tenants/:schema/quota` returns four things:

| Field | Meaning |
|---|---|
| `quota` | the tenant's **raw row** (`null` = not set) — the edit form binds this |
| `system` | the system row's raw value (`null` = not set) — shown as "system ceiling" |
| `fallback` | the value baked into the code (agents 100 / skills 300 / flows 300 / per-session space `-1` / sessions per user `-1`) — it is the ceiling while the system row is unset |
| `effective` | the effective value = `min(tenant value, system ceiling)`, taken straight from `quota.Resolve(schema)`; **the frontend never recomputes** the clamp |

`PUT` on the same path writes the raw row, then calls `quota.Invalidate(schema)`.

> **The list endpoint (`/platform/tenants`) returns the effective value** (`tenantView` goes through `quota.Resolve`), so do not
> use list data as an edit form: if a tenant row holds 500 and the effective value is 100, filling the form from the list and
> saving flattens 500 to 100.

## Disabling a plugin

Plugins are **open to every tenant by default** (since 2026-11-02): a tenant only has to enable one and configure an
implementation on its own "plugins" page. The platform side no longer enables them one by one, and
`platform.tenant_plugins` (created by `sql/tenant_plugins.sql`, idempotent) degrades into a **disable record** — only a row with
an explicit `is_allowed = false` means "disabled for this tenant", and no row means open. The tenant side's `{schema}.plugins`
still owns "which implementation, which key, enabled or not"; both have to allow it.

| Endpoint | Semantics |
|---|---|
| `GET /platform/plugins` | aggregate view for the plugin page: the on-disk catalog (with implementations) + every tenant + each tenant's raw row + the tenant-side enabled state |
| `GET /platform/tenants/:schema/plugins` | on-disk catalog × this tenant's policy (effective value + raw row) × tenant-side enabled state |
| `PUT /platform/tenants/:schema/plugins/:name` | writes this tenant's row (`allowed=false` disables it); an omitted field keeps the current value |
| `DELETE /platform/tenants/:schema/plugins/:name` | removes this tenant's row = back to the default (open) |

The resolution rule lives in `shared/pluginpolicy`: **no row = open**, a row is read through `is_allowed`. When `Runner.Invoke`
cannot read the policy at execution time it **does not let it through** (fail-closed): the plugin catalog degrades to "none
available" and a log line is written — the default being open does not mean "unreadable" is the same as "nobody disabled it".
The global kill switch is still `platform.plugin_gates`: open by default, and once it blocks, it outranks this table and affects
every tenant.

Why it was flipped: the plugin itself is trusted code the platform puts on disk, while credentials and cost live on the tenant
side (`{schema}.plugins.configs` holds the tenant's own key), so enabling them one by one was pure operations overhead. You can
still disable per tenant, or pull the global switch to stop the bleeding.

Upgrade note: on an old database, running the 2026-11-02 section of `sql/tenant_plugins.sql` deletes the redundant rows with
`is_allowed = true`; rows that were already `is_allowed = false` keep meaning "disabled" and behave the same (still unavailable).

> History: this table used to carry `daily_limit` / `concurrency_limit`, but the counters only lived inside a process (reset on
> restart, counted per replica), so "at most N per day" was not true and they were removed. A real quota has to be redesigned on
> shared storage (Redis).

> The platform console cannot import `service/plugin` (see "boundary rules"), so the on-disk plugin catalog is forwarded through
> the minimal view in `shared/plugininfo` (injected by `service/plugin.SetDefault`).

## Platform users

`POST /platform/users`: the account name matches `^[A-Za-z0-9_-]{1,64}$`, the password is at least `account.MinPasswordLength`
(8). **The display name may be empty** (both sides trim before writing; an empty one makes the UI fall back to the account name)
— `POST` and `PUT` agree, neither requires it.

**The protected admin (fixed `plat`) is protected**: `PUT /platform/users/:id` always refuses it (profile / role / status are all
immutable, and the check sits before "you cannot touch yourself", so `plat` cannot change itself either); **there is no
delete-user endpoint**, so "must not be deleted" holds by construction. The login name is hardcoded in
`auth.BootstrapAdminName` (the same kind of thing as `shared/tenants.PrimaryUserName` on the tenant side: "the account the
system ships with"), the config no longer has a `name` field, and nexus no longer creates it at startup (see "tables and
bootstrap"). `userView` returns `protected: true/false` and the frontend greys out "edit / suspend" from that,
**never from a hardcoded name**. A password reset is not blocked (same reasoning as above: it does not change identity, it
restores access).

`GET /platform/users?role=&status=&q=&page=&page_size=`: paging follows exactly the same criteria as `/platform/tenants`
(`page` from 1; a missing `page_size` or `<=0` = no paging, capped at 100; `{items,total,page,page_size}` with `total` the
filtered total). A "user" row carries `tenant_count` (tenants they own) — that is **one `CountByOwner` per row**, so do not
inflate `page_size`.

`GET /platform/users/:id/tenants`: the tenants a platform user owns (the "tenants owned" block of the console's detail card).
Same source as `/platform/me/tenants` (`tenantsrepo.ListByOwner`, which already excludes `status=deleted`), returns
`{items,total}`.

`userView` returns `created_at` / `updated_at` (used by the info rows of the detail card).

## Self-lock guards (not optional)

- You cannot suspend / demote **the account you are signed in as**
- You cannot suspend / demote **the last active admin** (that locks the platform out and leaves hand-editing the database)
- Suspending a "user" who owns tenants: allowed, but it returns a `warning` suggesting the ownership be transferred first
  (**no cascade, no automatic transfer**)

## A write must invalidate its cache

| Write | Call |
|---|---|
| create / suspend / delete / change the status of a tenant | `sharedtenants.Invalidate(schema)` (otherwise a suspension takes up to a TTL period to apply) |
| change a tenant's quota | `quota.Invalidate(schema)` |
| change a tenant's flow config | `flowconfig.Invalidate(schema)` |
| change a **system row** (flow / quota) | `flowconfig.InvalidateAll()` / `quota.InvalidateAll()` |

> `Resolve` caches the **already merged** result (tenant -> system -> fallback), so **changing a system row must clear
> everything**: deleting only the `""` slot leaves every tenant's cached slot stale.
>
> Invalidation only covers **this process's memory**: a single-process deployment makes it global; with several replicas or a
> split into several processes, the other instances can only wait for the TTL (2 minutes). For cross-process immediacy, add PG
> `LISTEN/NOTIFY` (the writer calls `pg_notify` in the same transaction and each instance keeps a LISTEN connection that clears
> its cache), or a "config version number + poll every 3–5 seconds".

## Tables and bootstrap

1. `psql ... -f nexus/platform/sql/platform.sql` (the two tables + indexes)
   - cross-tenant asset distribution has its own `sql/shared_assets.sql` (snapshots / visibility / install records), which
     **must run after `platform.sql`** (its foreign key references `platform.tenants`). Design:
     [docs/asset-sharing.md](../docs/asset-sharing.md).
2. Load the **environment's initial data**: it must contain a `users` row that is active, has role `admin` and the fixed login
   name `plat`. The platform admin is no longer bootstrapped by nexus — `nexus/cmd/main.go` has no platform startup bootstrap and
   the config no longer has `platform.bootstrap_admin` / `platform.ddl_dsn`. Without that row the console has no account to sign
   in with (the login name `plat` is `auth.BootstrapAdminName`, and the console can neither modify nor delete it).
3. Provisioning a tenant (the console's "new tenant", or `POST /platform/tenants`) works from here; the implementation is in
   `provision/`:

   - one transaction: `CREATE SCHEMA` → transaction-scoped `set_config('search_path', …)` → `template/schema.sql` →
     `template/seed.sql` → the first `manage` user → materialise `template/files/` into `tenants/<schema>/` before committing →
     the `platform.tenants` row → `platform.tenant_capabilities` all open by default (`capability.All()` with `is_allowed=true`
     per item; the platform can disable item by item and the global gate can stop everything)
   - a failure rolls back as a whole (PG supports transactional DDL), and the directories already written are reclaimed with
     `DeletePrefix`, so there is no half-provisioned tenant
   - after the commit: `sharedtenants.Invalidate` + `quota.Invalidate`
   - the schema is auto-discovered by `provision.NextSchema` (not editable in the console); the first admin's login name is the
     fixed `primary`

   The template pack's contents and how to regenerate it are in
   [docs/tenant-template-pack.md](../docs/tenant-template-pack.md). In one line: `schema.sql` is dumped from t0, `seed.sql` and
   `files/` are maintained as shipped assets, and **the runtime never reads t0**.
