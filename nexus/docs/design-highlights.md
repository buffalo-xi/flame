# Design highlights

[中文](design-highlights.zh.md) | English

This document is only about **why it is designed this way**, not how to use it. Every claim is followed by where it lives in
the code, so it can be checked.

Four threads: **calls must be fast**, **recursion must converge**, **tenants must be bounded**, **endpoints must be
reusable**.

---

## 1. Speed: never pay for remote cost you can avoid

### Endpoint calls are in-process function calls

An `rpc` node calling another flow or agent goes through `runEndpoint` — no HTTP / RPC wire. The agent's
`local__<endpoint>` tool does the same: a single registration point inverts the "run a flow" capability outward, which keeps
`executors` and `service/agent` from importing each other in a cycle.

Implementation: `nexus/service/flow/executors/rpc_target.go`, `nexus/service/flow/executors/localendpoint.go`.

**Cost**: this chain assumes the target is in the same process; a multi-node deployment needs a different implementation.
That is a deliberate trade-off, not an oversight.

### stdio subprocess pool

The stdio transport of MCP / RPC has to fork a subprocess and handshake — the most expensive step. `SessionPool` reuses one
session per `conn.ID`, using a sha256 fingerprint of `command + args + work_dir + env` to decide whether a rebuild is
needed; when a subprocess exits it only sets a `closed` flag instead of deleting the map entry, so a concurrent `Get` cannot
misread it and spawn a duplicate. `streamable_http` is a stateless short connection and **stays out of the pool**.

Implementation: `nexus/service/rpc/client/pool.go` (the MCP side is isomorphic).

### Quota checks take an O(1) fast path

On write, read the counter first; only "over limit" or "counter stale" triggers a directory scan. After a scan the real
value is written back so later calls take the fast path again. Besides periodic calibration, staleness has one extra rule —
shorten the calibration interval as the limit approaches — to prevent "thought it was 92%, actually 99%".

Implementation: `nexus/shared/quota/admit.go`.

### The storage layer avoids N LISTs

Recursive listing uses the optional `Walker.WalkPrefix` interface (one page of S3 returns the whole subtree); a file size
comes from `Stat` (a HEAD on S3, not a LIST of the parent). Quota eviction needs `(path, size, mtime)` for the whole tree,
which is why the capability exists.

Implementation: `nexus/shared/storage/storage.go`.

### Config and snapshots

Tenant-level config is read-through cached in Redis: a hit returns immediately, a miss or an unavailable Redis goes back to
the DB and refills the cache. Two details: **negative caching** (cache "this row does not exist" as `"null"`, otherwise an
unconfigured tenant hits an empty DB every time) and **skipping Redis entirely when it is down** rather than waiting for a
timeout.

Nothing calls `Resolve` inside the loop; instead the value is snapshotted once when the run starts (`ExecContext.Limits`).
The endpoint catalog is snapshotted per run for the same reason — one agent loop may call `local__` several times in a row.

Implementation: `nexus/shared/agentconfig/cache.go`, `nexus/shared/quota/cache.go`.

---

## 2. Cycle protection: depth limit + ancestor chain, not a counter

### Why a counter does not work

The execution counter in `ExecContext` can only track "nodes within one run". An endpoint chain creates a new `ExecContext`
at every hop, so each counter restarts from zero. Cycle protection therefore has to rely on **state that travels across
runs**.

### Two gates, each with its own semantics

| Gate | Where | Default limit | What it guards |
|---|---|---|---|
| flow chain depth | `runEndpoint` | `max_recursion_depth` = 10 | pure flow orchestration (an A→B→C→D pipeline) |
| agent chain depth | `service/agent/localendpoint.go` | `max_call_depth` = 3 | every re-entry into an agent runs another full LLM turn |

The agent gate sits **before the endpoint lookup** — once over the limit it does not even query the database.

### "Unlimited" is not accepted here

For other settings `-1` means unlimited (quotas, timeouts), but the depth limits are deliberately narrowed to positive
numbers: a pure flow cycle has only this one gate, so letting a misconfiguration switch it off would leave the door wide
open. `effectiveEndpointDepthLimit` resolves config → fallback → 10 and never allows an unlimited value.

### Ancestor chain: fail early, without hurting fan-out

A depth limit can only say "report after ten hops"; a cycle like `f → a → f` is visible immediately and should not wait.
The chain holds **ancestors only**, so several `rpc` nodes of the same flow calling the same target in parallel (sibling
fan-out) are not a false positive. The check runs after the depth check — "exceeded 10 levels" is easier to understand than
"cycle detected".

### How the chain travels: reserved keys + stripping

Across runs only arguments travel: `_call_depth` / `_call_chain` are injected as reserved keys and stripped at the
`NewFlow` entry point, so the business arguments the model sees stay clean. The map is **copied before injection**, leaving
the caller's copy untouched. `extractCallDepth` accepts both `float64` from JSON deserialization and `int` from an
in-process call; invalid values count as 0 — depth is only used for throttling, so dirty data must not reject a valid call.

### An honest premise

The chain is **reported by the caller**; if it is not passed, nothing is detected. The depth limit therefore has to stay as
the backstop, and external entry points (RPC / MCP) treat the chain as precise interception, not as the only line of
defence.

---

## 3. Multi-tenancy: explicit isolation, three layers of tightening

### One schema per tenant, and SQL qualifies it explicitly

No reliance on a global `search_path` (unreliable with pooled connections); every query concatenates `authInfo.Schema`. The
code is more verbose, but it can never "query the wrong database after the connection was swapped".

`search_path` is used exactly once, inside the provisioning transaction, and it is **transaction-scoped and parameterised**.

### Provisioning = one transaction + one rollback path

The order is the design: validate everything first (touching no DB) → inside a single transaction `CREATE SCHEMA` + create
tables + seed assets + first admin + registry row → invalidate caches after commit. If writing files fails, `DeletePrefix`
reclaims them, guaranteeing "either a complete tenant or nothing at all".

A concurrent grab is recognised as "schema already taken" via `42P06`, and the API answers 409 so the operator can pick
another candidate instead of a 500.

Implementation: `nexus/platform/provision/provision.go`.

### Quotas and run parameters: system hard cap → tenant → user

Merging follows one rule: `NULL` means unset, fall back to the next level; the result is then clipped against the **system
row**. So a tenant can only tighten within the platform ceiling, and a user can only sub-allocate within the tenant ceiling.
The merge layer also absorbs dirty values (`-1` / `0` always fall back) so consumers always receive a positive number.

User quotas live in a separate `user_quotas` table rather than as extra columns on `users` — the auth table does not carry
resource governance, and a future dimension only touches that table.

Implementation: `nexus/shared/quota/quota.go`, `nexus/shared/agentconfig/agentconfig.go`.

### A session is a working venue, not an asset

A session carries two limits, and both are enforced at write time instead of by a background reaper:

- **Workspace bytes** — the counter is read first (O(1)); over the limit the workspace is scanned and deleted oldest-first
  until this write fits. `skills/` and anything modified in the last five minutes are spared (the first is part of the agent
  definition, the second is very likely what the agent is writing right now), and one request deletes at most 50 files.
  Still no room → 507 `quota.session_storage_exceeded`, so the human cleans up rather than the platform guessing at what is
  disposable.
- **Sessions per user** — the console refuses with 507 `quota.user_sessions_exceeded` and leaves the choice to the human;
  RPC / MCP sessions are recycled oldest-first instead, because their callers bring their own UUIDs and have no delete API,
  so a plain refusal would lock an integration out forever.

Accounting is `Admit` → write → `Commit`: a failed write charges nothing, so the counter never inflates. A failed scan or a
failed counter read **fails open** — a broken counter must not cost a user their write.

Implementation: `nexus/shared/quota/admit.go`.

### Grants bind to the target, not the endpoint

The same agent usually has both an rpc and an mcp endpoint. Granting per endpoint produces "only rpc was ticked, so the MCP
connection is completely empty" and "grants die after the endpoint is released again". The whitelist key is therefore
`target_type:target_id`.

Three tiers: unrestricted / empty set (nothing allowed) / whitelist. There is also a middle tier — "unrestricted but
excluding the super agent" — so it is not "either all on or all off".

### No leakage on unauthorized access

External entry points check in the order **authorization → exposed state → cycles**: an unauthorized caller cannot even
probe "this endpoint exists but is retracted", and unauthorized and non-existent use identical wording.

---

## 4. Endpoints: one row of "target × protocol"

### An endpoint is not a new entity

One row in `svc_endpoints` = `target_type` (agent / flow) + `protocol` (rpc / mcp) + `exposed`. Adding a protocol is adding
a row, with no business logic duplicated. For flow endpoints `input_schema` always equals `flows.arguments`, which rules out
"endpoint input disagrees with flow input".

### `exposed` only governs the outside

- outside (MCP / RPC clients): `exposed = true` is required;
- inside (this tenant's `rpc` nodes / `local__` tools): `exposed` is **not** checked.

Payoff: a sub-flow used for internal orchestration does not have to be exposed just to be callable. The cost is that
internal calls must never be reachable from outside — hence none of the external checks may be skipped.

### Locate by name, not by id

The endpoint name always equals the entity name, uniqueness is enforced at creation, and renaming the entity syncs it via
`syncEndpoints`; ids are regenerated by "export → import into another tenant", which leaves every id in a diagram dangling.

Lookup must be **an exact match**: the name filter of `ListAll` is `ILIKE '%name%'`, so `poet` matches `poet-2` and the
model may call the wrong endpoint by name.

### Retract ≠ delete

`retract` only sets `exposed = false`, keeping the endpoint record and its config; `delete` is what removes the row.
Retracting therefore does not invalidate target-based credential grants.

The terminology red lines are in `nexus/docs/terminology.md` (Chinese): the endpoint domain uses `expose` / `retract`, never
`hide`, never `publish`.

---

## 5. Known trade-offs

Listed here so later readers do not mistake them for omissions:

- **In-process calls are a speed win and a scalability ceiling.** A multi-node deployment needs a different chain (stop
  signals already broadcast over Redis).
- **The depth limit depends on configuration.** Narrowing `-1` is deliberate and correct for now; long term, "may not be set
  to unlimited" should be enforced by admin UI validation.
- **The ancestor chain is reported by the caller.** Forgery or omission can only be caught by the depth limit.
- **`schema` concatenation is spread across repositories.** Safe, but it dilutes responsibility for correctness; a
  tenant-wrapping query builder could be enforced later.
- **`List` defaults to `limit=10` and truncates silently.** Currently avoided by the `ListAll` convention plus comments.
