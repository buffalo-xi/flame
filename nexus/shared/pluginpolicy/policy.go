// Package pluginpolicy provides the platform-level "tenant × plugin" policy: whether it may be used.
//
// Division of labour with {schema}.plugins (both must pass, neither alone is enough):
//
//	platform.tenant_plugins   platform side: may this tenant use it — writable by the platform
//	{schema}.plugins          tenant side: which implementation, which keys, enabled or not — writable by the tenant
//
// Why the authorization does not live in {schema}.plugins: that table is written through the tenant admin side, and
// "may it be used" is a cost and safety guard — a tenant that can open it for itself has no guard at all.
//
// kind: both tables have kind in their primary key (currently always tools). The exported signatures of this package still
// take "a single name" — every caller (Runner / tool catalogue / admin side) serves tool plugins only, and lifting kind
// into the parameters would open a dimension nobody uses. When sources later need a platform authorization, add a variant
// such as ResolveKind and leave the existing functions alone.
//
// The semantics are **two layers** (open by default × a global gate):
//
//		effective = (tenant row ?? open) AND NOT blocked
//
//	  - **open by default**: no row means it may be used, and the platform no longer has to enable tenants one by one. The
//	    plugin itself is trusted code the platform put on disk, while the credentials (api_key and so on) are filled in by
//	    the tenant in {schema}.plugins.configs and the cost falls on the tenant side, so "may it be used" stopped being a
//	    cost guard and became a product switch.
//	  - a tenant row now only means **disabled** (is_allowed = false): turning off one plugin for one tenant, which is
//	    finer than a global gate that hits everybody. The tenant still has to enable it and pick an implementation on its
//	    own side, see {schema}.plugins.
//	  - the global gate (platform.plugin_gates) is still an **emergency stop**: it passes by default (no row = no block),
//	    and once it blocks it overrides even a tenant's explicit enable. It is not a "third fallback layer" — a fallback
//	    chain stops at the first layer that has a value, while a hard gate must be able to override everything, so it is
//	    the 0 multiplied in last rather than a link in the chain.
//	  - 2026-10-21 removed the "system default row" (tenant_id = ''): it was merely an implicit default and a different
//	    thing from the emergency stop, and putting both in one table made admins unable to tell "what I turned off" from
//	    "what was never set", or mistook it for the master switch. The database now closes it off with CHECK (tenant_id <> '').
//	  - 2026-11-02 flipped the default from "refused" to "open" (a product decision): a tenant can use a plugin simply by
//	    enabling it and picking an implementation on its own plugin page, and the platform only steps in to disable one.
//
// Boundaries that still hold: the global gate must override everything; and when the policy cannot be read the caller
// fails closed (see Runner), otherwise one DB hiccup turns "disabled" into "open by default".
//
// History: this package once carried daily_limit / concurrency_limit, but the quota counting was in-process only (cleared
// on restart, counted separately per replica, so "at most N per day" was a lie) and both were removed.
// A real quota later should be redesigned on shared storage (Redis) instead of restoring those two columns.
//
// No caching: one plugin call costs one extra primary-key query, while a cache would introduce the "the platform changed
// the authorization, the tenant waits for the TTL" problem — correctness wins here. In an emergency stop that is a hard
// requirement.
package pluginpolicy

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// KindTools is the tool plugin: currently the only plugin type the platform authorizes (the same value as
// repository/plugins.KindTools and service/plugin.KindTools; each of the three layers declares its own to avoid a
// dependency for one capitalised string).
const KindTools = "tools"

// Policy is the authorization policy of one "tenant × plugin".
//
// Blocked gets a field of its own instead of flattening both cases into Allowed=false: the caller has to say whether "the
// platform disabled it for this tenant" or "the platform disabled it globally" — the first can be restored by the
// platform, the second also needs the platform to lift the gate, but it affects every tenant, so the two sentences point
// at different scopes of action.
type Policy struct {
	Allowed bool
	Blocked bool
}

// Default is the policy when there is no row: **open** (and not blocked).
// Disabling takes an explicit row with is_allowed=false (see Set), because "never set" and "set to off" are two different
// things to show on the admin side — the default value must not be used to guess.
func Default() Policy { return Policy{Allowed: true} }

// tableName is the full policy table name (in the admin schema).
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".tenant_plugins"
}

// gateTableName is the full global gate table name (in the admin schema).
func gateTableName() string {
	return config.Cfg.Database.PlatformSchema + ".plugin_gates"
}

// Resolve returns the effective policy of a tenant for a plugin: (tenant row ?? open) AND NOT global gate.
//
// One SQL statement (two scalar subqueries) rather than two round trips: this function runs before every plugin call, and
// an extra round trip buys nothing.
//
// A failed query returns an error rather than silently falling back to the default: the caller must decide whether to let
// it through, and Runner chooses fail-closed — the default is open, but "the policy cannot be read" does not mean "nobody
// ever disabled it", so it is better to wrongly switch everyone off than to let a plugin that should be disabled through
// during a hiccup.
func Resolve(tenantID, pluginName string) (Policy, error) {
	var p Policy
	err := db.DB.QueryRow(`
		SELECT COALESCE((SELECT is_allowed FROM `+tableName()+`
		                  WHERE tenant_id = $1 AND plugin_name = $2 AND kind = $3), true),
		       COALESCE((SELECT blocked FROM `+gateTableName()+`
		                  WHERE plugin_name = $2 AND kind = $3), false)`,
		tenantID, pluginName, KindTools,
	).Scan(&p.Allowed, &p.Blocked)
	if err != nil {
		return Policy{}, err
	}
	if p.Blocked {
		p.Allowed = false // hard gate: the 0 multiplied in last, overriding the tenant row
	}
	return p, nil
}

// Gates is the set of globally blocked plugin names (only blocked=true; a missing row does not block).
//
// A map rather than a full list: the caller has exactly one use — `if gates[name] { turn it off }` — and the zero value
// false is precisely "not blocked".
func Gates() (map[string]bool, error) {
	rows, err := db.DB.Query(
		`SELECT plugin_name FROM `+gateTableName()+` WHERE kind = $1 AND blocked`, KindTools)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// GateBlocked reports whether a single plugin is blocked by the global gate; no row = not blocked.
func GateBlocked(pluginName string) (bool, error) {
	var blocked bool
	err := db.DB.QueryRow(
		`SELECT blocked FROM `+gateTableName()+` WHERE plugin_name = $1 AND kind = $2`,
		pluginName, KindTools,
	).Scan(&blocked)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return blocked, nil
}

// SetGate sets (or overwrites) one plugin's gate. blocked=false is equivalent to deleting the row, but keeps a record.
func SetGate(pluginName string, blocked bool) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+gateTableName()+` (kind, plugin_name, blocked, updated_at)
		 VALUES ($1,$2,$3,CURRENT_TIMESTAMP)
		 ON CONFLICT (kind, plugin_name) DO UPDATE SET
		     blocked=EXCLUDED.blocked,
		     updated_at=CURRENT_TIMESTAMP`,
		KindTools, pluginName, blocked,
	)
	return err
}

// DeleteGate deletes the gate row = back to the default (not blocked). Restoring should go through here rather than
// SetGate(false), so there is no leftover "always false" record suggesting the gate was changed.
func DeleteGate(pluginName string) error {
	_, err := db.DB.Exec(
		`DELETE FROM `+gateTableName()+` WHERE plugin_name = $1 AND kind = $2`,
		pluginName, KindTools,
	)
	return err
}

// List returns the policies of a tenant that **explicitly have a row** (the global gate already multiplied in; a plugin
// that does not appear follows Default(), i.e. open).
// The admin side uses it when rendering the plugin list, avoiding N queries through Resolve one by one.
func List(tenantID string) (map[string]Policy, error) {
	rows, err := db.DB.Query(
		`SELECT plugin_name, is_allowed FROM `+tableName()+`
		 WHERE tenant_id = $1 AND kind = $2`, tenantID, KindTools)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Policy{}
	for rows.Next() {
		var name string
		var p Policy
		if err := rows.Scan(&name, &p.Allowed); err != nil {
			return nil, err
		}
		out[name] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Global gate: the same shape as capabilitypolicy.ResolveAll, the 0 multiplied in last.
	gates, err := Gates()
	if err != nil {
		return nil, err
	}
	for name := range out {
		if gates[name] {
			p := out[name]
			p.Allowed = false
			p.Blocked = true
			out[name] = p
		}
	}
	return out, nil
}

// Rows returns every **raw row** of a tenant_id (an empty string no longer means the system row).
// The platform editor has to tell "this tenant set it" from "it was never set", so the effective value alone is not
// enough.
func Rows(tenantID string) (map[string]Policy, error) {
	rows, err := db.DB.Query(
		`SELECT plugin_name, is_allowed FROM `+tableName()+`
		 WHERE tenant_id = $1 AND kind = $2`, tenantID, KindTools)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Policy{}
	for rows.Next() {
		var name string
		var p Policy
		if err := rows.Scan(&name, &p.Allowed); err != nil {
			return nil, err
		}
		out[name] = p
	}
	return out, rows.Err()
}

// Get reads a tenant's **raw row** (a false second return means it was never set).
// An edit form must bind to it: filling in the effective value would silently turn "unset" into a concrete value.
func Get(tenantID, pluginName string) (Policy, bool, error) {
	var p Policy
	err := db.DB.QueryRow(
		`SELECT is_allowed FROM `+tableName()+`
		 WHERE tenant_id = $1 AND plugin_name = $2 AND kind = $3`,
		tenantID, pluginName, KindTools,
	).Scan(&p.Allowed)
	if err == sql.ErrNoRows {
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, err
	}
	return p, true, nil
}

// Set writes (or overwrites) one row. An empty tenantID is rejected by the table's CHECK constraint, which requires a
// non-empty tenant_id — the system default row is gone, and there is no fallback rewrite here, so a caller's mistake
// surfaces right away.
func Set(tenantID, pluginName string, p Policy) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+` (tenant_id, kind, plugin_name, is_allowed, updated_at)
		 VALUES ($1,$2,$3,$4,CURRENT_TIMESTAMP)
		 ON CONFLICT (tenant_id, kind, plugin_name) DO UPDATE SET
		     is_allowed=EXCLUDED.is_allowed,
		     updated_at=CURRENT_TIMESTAMP`,
		tenantID, KindTools, pluginName, p.Allowed,
	)
	return err
}

// Delete deletes one row (deleting it returns to the default = open).
func Delete(tenantID, pluginName string) error {
	_, err := db.DB.Exec(
		`DELETE FROM `+tableName()+` WHERE tenant_id = $1 AND plugin_name = $2 AND kind = $3`,
		tenantID, pluginName, KindTools,
	)
	return err
}
