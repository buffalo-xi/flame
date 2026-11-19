// Package capabilitypolicy provides the platform-level policy for "tenant × tool capability" (the key names
// follow sandbox.*).
//
// How it divides the work with the tenant side (both layers apply, neither optional):
//
//	platform.tenant_capabilities  platform side: may this tenant use this kind of capability — written by the platform
//	{schema}.agent_tool_grants    tenant side: which agent is granted it — written by the tenant
//
// Why the platform layer is needed: the sandbox is **shared infrastructure** (one sandboxd serves every tenant).
// The browser is a resident Chromium, and a shell leaves resident processes that may reach the network — the cost
// and the risk sit with the platform, while the switch used to be in the tenant's hands alone. This is the same
// reason pluginpolicy moved call quotas up to the platform: a gate a tenant can open for itself is no gate.
//
// The semantics are **two layers** (deny by default × a global gate):
//
//	effective = (tenant row ?? not granted) AND NOT blocked
//
//   - the tenant row is the only source of authorisation; no row means not granted (the tool shows as
//     unavailable in the catalogue, and the execution layer refuses outright);
//   - the global gate (platform.capability_gates) is an **emergency stop**: once it blocks, it overrides the
//     tenant's explicit enablement. It is not "a third fallback layer" — a fallback chain stops at the first
//     layer that has a value, while a hard gate must override everything, so it is a 0 multiplied in last
//     rather than a link in the chain.
//   - 2026-10-21 removed the "system default row" (tenant_id = ''): it only affected tenants without a row, it
//     is a different thing from the global gate, and keeping it in the same table left administrators unable
//     to tell "a default" from "an emergency stop" — and likely to mistake it for a master switch. The
//     database now forbids it with CHECK (tenant_id <> '').
//   - no caching: one tool resolution costs two extra primary-key lookups, traded for the correctness of
//     "a platform change takes effect immediately". In an emergency-stop scenario that is a hard requirement.
//
// ⚠️ Every entry point goes through capability.Normalize first: 2026-10 renamed sandbox.script to sandbox.code
// and the database may still hold old rows. Normalisation must cover both reads (Resolve / ResolveAll / Gates)
// and writes (Set / SetGate), otherwise you get two synonymous rows, one read by the old key and one written
// under the new one.
//
// Legacy note: before this table existed there were no platform rows, and the migration script backfills on
// "was actually in use" (see nexus/platform/sql/tenant_capabilities.sql); otherwise exec/browser would stop
// working for existing tenants immediately.
package capabilitypolicy

import (
	"database/sql"
	"strings"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/tools/xlog"
)

// Policy is the policy for one "tenant × capability". Today it only holds "allowed"; when concurrency or quota
// limits are added, extend the columns the way pluginpolicy does (the browser session count is the obvious
// candidate).
type Policy struct {
	Allowed bool
}

// Default is the policy when there is no configured row: **not granted**.
func Default() Policy { return Policy{Allowed: false} }

// tableName is the full policy table name (in the management schema).
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".tenant_capabilities"
}

// Resolve returns the effective policy of one tenant for one capability: (tenant row ?? not granted) AND NOT
// the global gate.
//
// A failed query returns an error rather than silently falling back to the default: the caller has to decide
// whether to let it through, and the runtime gate's choice is fail-closed (one database hiccup must not equal
// "everyone is authorised").
func Resolve(tenantID, key string) (Policy, error) {
	key = capability.Normalize(key)
	p := Default()
	err := db.DB.QueryRow(
		`SELECT is_allowed FROM `+tableName()+` WHERE tenant_id = $1 AND capability = $2`,
		tenantID, key,
	).Scan(&p.Allowed)
	if err != nil && err != sql.ErrNoRows {
		return Policy{}, err
	}

	blocked, err := GateBlocked(key)
	if err != nil {
		return Policy{}, err
	}
	if blocked {
		p.Allowed = false
	}
	return p, nil
}

// ResolveAll returns the effective verdict for every **known capability** of a tenant (deny by default:
// anything unconfigured is false).
//
// The shape is map[string]bool rather than map[string]Policy because callers only have two uses — computing
// availability for the catalogue and deciding the runtime gate — and both only care whether it is on.
func ResolveAll(tenantID string) (map[string]bool, error) {
	rows, err := db.DB.Query(
		`SELECT capability, is_allowed FROM `+tableName()+` WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool, len(capability.All()))
	for _, key := range capability.All() {
		out[key] = false // deny by default: no tenant row means not granted
	}
	for rows.Next() {
		var key string
		var allowed bool
		if err := rows.Scan(&key, &allowed); err != nil {
			return nil, err
		}
		// Normalise the old key: without it a sandbox.script row falls outside All() and Snapshot, which filters
		// old keys, shows "not granted" for something that is in fact on.
		out[capability.Normalize(key)] = allowed
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The global gate: a 0 multiplied in last, overriding everything above (an emergency stop must cover the
	// tenant's explicit enablement).
	gates, err := Gates()
	if err != nil {
		return nil, err
	}
	for key := range out {
		if gates[key] {
			out[key] = false
		}
	}
	return out, nil
}

// Snapshot takes a tenant's capability snapshot (every key present, false by default).
//
// A failed read returns **everything not granted** (fail-closed) and logs it: capabilities are a cost guard, and
// one database hiccup relaxing into "everyone may execute" is far more dangerous than refusing one call.
// Both the catalogue display and the runtime gate go through it — the two must agree, or the UI says available
// and execution refuses.
func Snapshot(schema string) map[string]bool {
	all, err := ResolveAll(schema)
	if err != nil {
		hint := ""
		if strings.Contains(err.Error(), "does not exist") {
			// Give an actionable clue when the rollout order was wrong (the table is missing), otherwise "execution
			// refused" alone takes a long time to diagnose
			hint = " - run nexus/platform/sql/tenant_capabilities.sql first"
		}
		xlog.Error("failed to read tenant capabilities schema=%s (treating everything as not granted): %v%s", schema, err, hint)
		all = map[string]bool{}
	}
	out := make(map[string]bool, len(capability.All()))
	for _, key := range capability.All() {
		out[key] = all[key]
	}
	return out
}

// ---- the global hard gate (platform.capability_gates) ----
//
// The semantics are the **opposite** of a tenant row: allow by default. No row = not blocked, and only
// blocked=true blocks.
// That default is deliberately inverted — for a gate, "forgot to configure it" should mean "not blocking",
// otherwise adding a gate would quietly switch everyone's capabilities off.
//
// It has only two states (blocking / not blocking), with no separate "unset" semantics: setting false is
// equivalent to deleting the row, so there is none of the three-state confusion of a "system default".

// gateTableName is the full name of the global gate table.
func gateTableName() string {
	return config.Cfg.Database.PlatformSchema + ".capability_gates"
}

// Gates is the set of blocked capabilities (only keys with blocked=true; anything absent is not blocked).
//
// A map rather than a full list: callers only have one use — `if gates[key] { turn it off }` — and the zero
// value false is exactly "not blocked".
func Gates() (map[string]bool, error) {
	rows, err := db.DB.Query(`SELECT capability FROM ` + gateTableName() + ` WHERE blocked`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		// Normalise the old key: an emergency-stop row still spelled sandbox.script means the stop does nothing
		// without it.
		out[capability.Normalize(key)] = true
	}
	return out, rows.Err()
}

// GateBlocked reports whether one capability is blocked by the global gate; no row means not blocked.
func GateBlocked(key string) (bool, error) {
	key = capability.Normalize(key)
	var blocked bool
	err := db.DB.QueryRow(
		`SELECT blocked FROM `+gateTableName()+` WHERE capability = $1`, key,
	).Scan(&blocked)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return blocked, nil
}

// SetGate sets (or overwrites) a capability's gate. blocked=false is equivalent to deleting the row, but leaves
// a record behind.
func SetGate(key string, blocked bool) error {
	key = capability.Normalize(key)
	_, err := db.DB.Exec(
		`INSERT INTO `+gateTableName()+` (capability, blocked, updated_at)
		 VALUES ($1,$2,CURRENT_TIMESTAMP)
		 ON CONFLICT (capability) DO UPDATE SET
		     blocked=EXCLUDED.blocked,
		     updated_at=CURRENT_TIMESTAMP`,
		key, blocked,
	)
	return err
}

// DeleteGate deletes a gate row = restores the default (not blocked). Restoring should go through here rather
// than SetGate(false), so there is no leftover "always false" record to make people believe the gate was
// changed.
func DeleteGate(key string) error {
	key = capability.Normalize(key)
	_, err := db.DB.Exec(`DELETE FROM `+gateTableName()+` WHERE capability = $1`, key)
	return err
}

// Rows returns every **raw row** under one tenant_id (an empty string no longer means a system row).
// The platform editor has to tell "this tenant set it" from "it was never set", so the effective value alone is
// not enough.
func Rows(tenantID string) (map[string]Policy, error) {
	rows, err := db.DB.Query(
		`SELECT capability, is_allowed FROM `+tableName()+` WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Policy{}
	for rows.Next() {
		var key string
		var p Policy
		if err := rows.Scan(&key, &p.Allowed); err != nil {
			return nil, err
		}
		out[capability.Normalize(key)] = p
	}
	return out, rows.Err()
}

// Get returns one tenant's **raw row** (a false second return means it was never set).
// An edit form must bind to this: filling it in from the effective value silently turns "unset" into a concrete
// value.
func Get(tenantID, key string) (Policy, bool, error) {
	key = capability.Normalize(key)
	var p Policy
	err := db.DB.QueryRow(
		`SELECT is_allowed FROM `+tableName()+` WHERE tenant_id = $1 AND capability = $2`,
		tenantID, key,
	).Scan(&p.Allowed)
	if err == sql.ErrNoRows {
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, err
	}
	return p, true, nil
}

// Set writes (or overwrites) one row. An empty tenantID is rejected by the database's CHECK (tenant_id <> '') —
// the system default row is gone, so there is no fallback rewrite here; the caller's mistake surfaces
// immediately.
func Set(tenantID, key string, p Policy) error {
	key = capability.Normalize(key)
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+` (tenant_id, capability, is_allowed, updated_at)
		 VALUES ($1,$2,$3,CURRENT_TIMESTAMP)
		 ON CONFLICT (tenant_id, capability) DO UPDATE SET
		     is_allowed=EXCLUDED.is_allowed,
		     updated_at=CURRENT_TIMESTAMP`,
		tenantID, key, p.Allowed,
	)
	return err
}

// Delete removes one row (deleting it returns to the default = not granted).
func Delete(tenantID, key string) error {
	key = capability.Normalize(key)
	_, err := db.DB.Exec(
		`DELETE FROM `+tableName()+` WHERE tenant_id = $1 AND capability = $2`,
		tenantID, key,
	)
	return err
}
