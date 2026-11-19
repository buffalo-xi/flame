# Business error-code conventions

[中文](error-codes.zh.md) | English

For the frontend to support two languages, backend user-visible copy can no longer be "one sentence". This document
defines the shape of `code + params` and how to migrate to it.

## Current state

The `xhttp` envelope (`tools/xhttp/http.go`):

```json
{ "code": 200, "msg": "服务端点不存在: poet", "data": null, "trace": "host/path" }
```

(The `msg` above is the real payload: it is still Chinese, see the constraints below.)

Problems:

- `code` has only three values, `200 / 400 / 507` — it is a **transport-level classification**, not business semantics;
- `msg` is a pre-assembled Chinese string, so the frontend can only display it verbatim and cannot translate it;
- the parameter baked into it (`poet`) is fused with the copy, so even a frontend that splits it with a regex cannot handle
  plurals or word order.

## Target shape

`code` keeps its numeric meaning unchanged (so existing frontend checks keep working), and a new `error` field carries the
business code:

```json
{
  "code": 400,
  "error": "endpoint.not_exposed",
  "params": { "name": "poet" },
  "msg": "服务端点未对外开放: poet",
  "data": null,
  "trace": "host/path"
}
```

Constraints:

- when `error` is absent the frontend falls back to `msg` — a **transitional** path only, see the ordering note below;
- `msg` is **English diagnostic text** (logs, `curl`, old frontends); it is not user-facing copy. Anything a user reads is
  rendered from the `error` code plus the locale bundle, so `msg` must never be the only channel for a user-visible error;
- **ordering constraint**: switching `msg` to English is only safe **after** `error` coverage reaches 100% (flow executors,
  `SendErrorResponse`'s 409/403/404, and the 24 frontend domains). Flipping it earlier shows English sentences to Chinese
  users;
- `params` carries displayable scalars only (names, counts, limits) — never internal addresses, SQL or stack traces.

## Naming rules

`<domain>.<action-or-state>`, all lower case, dot separated, with the wording aligned to `nexus/docs/terminology.md`:

| Domain | Examples | Notes |
|---|---|---|
| `auth` | `auth.invalid_credentials` | login failure (not distinguished from "user does not exist") |
| `tenant` | `tenant.not_found`, `tenant.schema_taken` | `schema_taken` maps to 409 |
| `endpoint` | `endpoint.not_found`, `endpoint.not_exposed` | the endpoint domain uses expose / retract |
| `asset` | `asset.not_found`, `asset.not_available` | `not_available` = exists but is not listed to this tenant |
| `flow` | `flow.not_released`, `flow.deprecated` | same word as `models.StatusDeprecated`; the UI says "deprecated" too |
| `agent` | `agent.call_depth_exceeded`, `agent.not_found` | |
| `skill` | `skill.not_found` | |
| `tool` | `tool.not_found`, `tool.contract_readonly` | custom tools (`customtools`); `contract_readonly` covers create / update / delete / upload of TOOL.json |
| `version` | `version.not_found` | the version domain, same domain as release / rollback / apply |
| `llm` | `llm.model_not_found`, `llm.provider_not_found` | |
| `user` | `user.not_found` | platform users / tenant users |
| `session` | `session.not_found` | the session itself; token expiry is `auth.session_expired` |
| `run` | `run.not_found` | a runtime ledger record |
| `mcp` / `rpc` | `mcp.connection_not_found`, `rpc.connection_not_found` | the connection domain, one per protocol |
| `binding` | `binding.not_found` | the grant binding of an endpoint / tool |
| `toolgrant` | `toolgrant.ref_not_found`, `toolgrant.item_not_found` | no usable tool under the ref / item |
| `plugin` | `plugin.not_found`, `plugin.not_enabled_for_tenant` | a plugin is discovered on disk; it is not a tenant entity |
| `globalvar` | `globalvar.not_found`, `globalvar.in_use` | `in_use` = the reference surface could not be scanned; `referenced` = it was scanned and references do exist (with `params.count`) |
| `peer` | `peer.not_found`, `peer.unreachable` | peer market (`platform/handler/peers.go`); `unreachable` = cannot connect or the token is wrong |
| `capability` | `capability.dependency_missing` | dependency admission for capability toggles (`params.deps` is the candidate list joined with " / ") |
| `quota` | `quota.session_storage_exceeded` | maps to 507 |
| `sandbox` | `sandbox.not_ready` | |
| `common` | `common.invalid_params`, `common.internal` | |

File cases uniformly use `<domain>.file_not_found` (`agent.file_not_found` / `skill.file_not_found` /
`tool.file_not_found`): the same "file does not exist" needs different guidance per domain, so do not merge them into
`common.not_found`.

Actionable codes (agents shown, other domains analogous): `agent.deprecated` (once deprecated: no update / delete / release /
apply / switch), `agent.invalid_name`, `agent.invalid_status`, `agent.file_too_large` (`params.limit_mb`),
`agent.skills_dir_readonly`, `agent.last_version_undeletable` (`params.version`), `agent.rename_failed`.
Pure operational failures (`<op> failed: <err>`) get no new code; they all use `common.internal`, with the original error left
in `msg` for logs and curl.

Forbidden:

- using `publish` / `hide` for assets or endpoints (see the terminology document);
- meaningless numbering such as `error.1` / `error.2`;
- baking variables into `error` (`endpoint.not_exposed.poet` is wrong — the name belongs in `params`);
- putting a dotted code into the `error` of `SendErrorResponse` — that one is the **transport-level reason enum**
  (`not_found` / `unauthorized` / `forbidden` / `conflict` …); the two envelopes must not be mixed. Such responses (e.g. the
  409 when tenant provisioning loses the schema race, `mustOwnTenant`'s 403/404) still carry only `message` for now, and the
  frontend falls back to `msg` / `message`.

## Migration

Three steps, which can be rolled out in batches; every step ships independently:

1. **Add the field** (done):
   - `tools/xhttp`: `ErrCode(c, errCode, params, msg, trace)` — the business code is fixed to 400 and existing call sites are
     untouched. There is also `ErrCodeData(...)` — errors that must still carry `data` (`suggested_name` /
     `can_overwrite`), such as a name conflict, go through it; do not drop `data` just to add a code;
   - `nexus/service/rpc/server`: `writeRPCErrorCode(...)`, with the business code under `error.data`.
2. **Convert the hot paths first**: login, tenant, endpoint, quota, flow runs — these cover the vast majority of
   user-visible errors.
   Pilot done: `endpoint.not_found` / `endpoint.not_exposed` (RPC server side);
   login is covered: `auth.invalid_credentials` / `auth.account_not_console` / `auth.account_locked` /
   `auth.account_disabled` (`shared/auth/login.go`, `platform/handler/login.go`), with `common.invalid_params` /
   `common.internal` for invalid arguments and internal failures respectively;
   tenant: `tenant.not_found` (9 places under `platform/handler`, `params.name` carries the schema);
   quota: `quota.session_storage_exceeded` / `quota.user_sessions_exceeded`, with 507 always emitted from `respondQuotaErr`
   via `xhttp.ErrQuotaCode` (quota codes are split by `metric`);
   the flow run entry points (RPC server): `flow.not_released`, `flow.deprecated` through `writeRPCErrorCode`, landing in
   `error.data.error`. The MCP server is not converted, per the last section;
   the flow execution layer (executors' `rpc_target`) and callee-internal errors still carry copy only (no code channel);
   the HTTP side of endpoints / assets (`admin/svcendpoints`, `platform/handler`): `endpoint.not_found`,
   `asset.not_found`, with invalid arguments and missing context mapped to `common.invalid_params` / `common.internal`;
   resource-style not-found has been swept repo-wide (`ErrJSON(c, "... not found")` has zero occurrences left): `agent.*`,
   `skill.*`, `version.*`, `flow.*`, `llm.*`, `user.*`, `session.*`, `run.*`, `mcp.*`, `rpc.*`, `binding.*`, `toolgrant.*`,
   `*.file_not_found`, plus `Authentication info(mation) not found`, all mapped to `common.internal` (that one accounts for
   the bulk of error sites; it is a routing/wiring gap, and `msg` keeps the original text for troubleshooting);
   the failure copy in `admin/agents` (deprecated / invalid argument / too large / read-only / last version) is covered in 31
   places;
   `admin/llmmodels`, `admin/flows`, `admin/runs`, `admin/mcpconnections`, `admin/rpcconnections`, `admin/svcauthbindings` are
   covered in 18 places (`llm.model_*` / `flow.invalid_status` / `run.*` / `mcp|rpc.discovery_failed` /
   `binding.target_not_exposed`);
   `admin/skills` (26), `admin/plugins` (15), `admin/globalvars` (11) are done as well;
   `admin/users` (32) is covered: `user.invalid_name` / `user.name_taken` / `user.primary_readonly` /
   `user.cannot_modify_self` / `user.last_admin_undeletable` / `user.password_*` (required / too short / unchanged /
   incorrect / cannot be set on a service user), with `quota.invalid_value` / `quota.self_lockout` /
   `quota.exceeds_tenant_limit` / `quota.exceeds_system_limit` for quotas (`params.field` carries the field name);
   `admin/customtools` (54) is covered: `tool.invalid_name` / `tool.name_taken` / `tool.not_found` / `tool.invalid_contract` /
   `tool.deprecated` / `tool.contract_readonly` (the same code for all four TOOL.json write entry points) /
   `tool.entry_exists` / `tool.entry_readonly` / `tool.invalid_path` / `tool.file_too_large` / `tool.contract_write_failed` /
   `tool.entry_write_failed` (partial-success semantics) / `tool.capability_denied` / `tool.last_version_undeletable`, with
   `sandbox.not_ready` for the sandbox gate;
   `admin/share` (52) is covered: `asset.*` (`invalid_kind` / `ref_required` / `invalid_status` / `not_owner` /
   `name_conflict` / `invalid_package` / `package_too_large` / `package_too_new` / `kind_mismatch` / `record_write_failed` /
   `install_not_found` / `stale_build` / `peer_not_available`), with name conflicts going through `xhttp.ErrCodeData` to keep
   `data.suggested_name` / `can_overwrite`;
   `platform/handler` (101) is covered: `tenant.*` (`owner_not_found` / `owner_invalid_role` / `invalid_status` /
   `must_suspend_first`), `user.*` (platform accounts, same codes as the tenant side), `peer.*`, `plugin.*`,
   `capability.dependency_missing`, `llm.preset_*`, `asset.*`, with `quota.*` reused for quotas.
   At this point `ErrJSON + Chinese` has zero occurrences left in `nexus` (the one in `admin/agents/session_guard.go` surfaces
   an English constant; Chinese only appears in comments), and the MCP server is not converted, per the last section.
3. **Frontend fallback**: maintain an `error.*` map in the locale files and fall back to `msg` when a key is missing.
   Consumers are already unified: components no longer render `body.msg` / `data.msg` directly but always call
   `formatError(res)` (internally `te(error.xxx) ? t(...) : msg`), so covering codes on the backend requires no frontend
   change.

### Frontend consumption

```js
const text = te(`error.${res.error}`)
  ? t(`error.${res.error}`, res.params || {})
  : res.msg
```

`te()` tells whether the key exists, so a missing translation does not render the raw `error.xxx`.

## Relationship to MCP / RPC

MCP / RPC use JSON-RPC, where the error object must use a **standard error code** (`-32602` etc.) and the business code goes
into `data.error`:

```json
{ "jsonrpc": "2.0", "id": 1,
  "error": { "code": -32602, "message": "服务端点不存在: poet", "data": { "error": "endpoint.not_found", "params": { "name": "poet" } } } }
```

Reason: external clients (Claude Desktop, third-party MCP hosts) only understand JSON-RPC error-code semantics; putting a
business code into `code` makes them read it as a protocol error.

Note: **the MCP server does not need the two `endpoint.*` codes**. It loads endpoints by `protocol=mcp AND exposed=true`, so
unauthorized targets are skipped at registration time and the call never reaches the handler — there is no runtime branch for
"not found / not exposed" (see `nexus/service/mcp/server/server.go`). MCP's consumer is an LLM host, not this project's UI,
so `data.error` is of little value there and no conversion is needed.

## Security constraint

Error copy at external entry points **must not leak existence**: unauthorized and non-existent must use the same `error`
(`endpoint.not_found`), and `params` carries only the name the caller supplied. This matches the existing handling in
`service/rpc/server/server.go` — do not break it while migrating.
