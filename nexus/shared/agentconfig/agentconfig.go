// Package agentconfig reads and merges the agent runtime parameters (platform.agent_configs).
//
// The semantics match shared/flowconfig and shared/quota:
//   - an empty tenant_id is the system row (the global default); 't0' is a tenant row (which overrides the system row)
//   - NULL = not set (fallback: tenant row -> system row -> code fallback)
//   - the system row is also the **hard ceiling**: at runtime min(tenant value, system value) applies, so a tenant
//     configuring a higher value gains nothing
//
// The only difference from flow_configs: this package has **no -1 (unlimited)**.
// All six fields are safety valves rather than quotas — max_iterations = -1 removes the loop guard, max_call_depth = -1
// allows unlimited recursion, max_tools / max_output_bytes = -1 hand the context and the memory to the model on
// demand, and max_run_records / max_session_records = -1 let the run log (agent_run_records) and the session memory
// (session_records) grow until the disk fills.
// The write entry points therefore refuse -1 outright (see the CHECK constraints in
// nexus/platform/sql/agent_configs.sql), and
// this package normalises <= 0 into the fallback, so a weird value cannot be written by editing the database directly
// and bypassing the API.
//
// The same file also owns the pieces both HTTP layers share (the platform handler and the tenant-side admin handler):
// Row (one stored row), Effective (the merged view shown as "what actually applies") and Validate* (the write rules plus
// their error codes). Keeping them here means the two entry points cannot drift apart in what they accept.
//
// Caching: a shared Redis cache (see cache.go); when Redis is unavailable it queries the database directly.
package agentconfig

import (
	"database/sql"
	"fmt"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// Limits are the effective runtime parameters. Every value is >= 1, so a consumer can use it directly without a zero-value
// check.
type Limits struct {
	MaxIterations  int
	MaxCallDepth   int
	MaxTools       int
	MaxOutputBytes int
	// MaxRunRecords is how many rows one agent keeps in agent_run_records (the run log, not the memory).
	// It is a retention quota like the flow-side max_run_records, but it still refuses -1: an unbounded
	// log table is a disk leak, not a feature.
	MaxRunRecords int
	// MaxSessionRecords is how many session_records rows one agent keeps per session. It is the agent-side
	// counterpart of flow_configs.max_session_records and replaces the hard-coded 100 that used to live in
	// service/agent, so both owner types read the same number from the same kind of place.
	MaxSessionRecords int
}

// fallback holds the code defaults used while the table / system row is not configured.
//
// These are the values an unconfigured tenant actually runs with, so each one is sized for "an ordinary agent doing
// ordinary work", not for the largest case that can still be defended:
//
//	max_iterations  40  every iteration is a model call plus tool executions; 40 already covers
//	                    read -> edit -> test -> fix. The old 80 doubled the cost ceiling of every tenant that never
//	                    touched this page.
//	max_tools       60  the cost is hidden: every tool ships its JSON schema into every round (~300 tokens each). The
//	                    cap has to cover the resident core plus the on-demand groups it exists for: the supervisor
//	                    already carries 20 resident (required) tools, and flow-admin alone is 16 - at 30 the group was
//	                    truncated before read_flow / run_flow / validate_flow, i.e. the tools the skill is used for.
//	                    60 holds 20 + 36 (every shipped group at once) = 56 with a little headroom; past it the
//	                    truncation warning lists exactly what was dropped.
//	max_output_bytes 64 KiB  already about 16k tokens for one record, and it is a quarter of the 256 KB session-history
//	                    budget, so a single truncation cannot swallow the conversation.
//	max_run_records 100 / max_session_records 50  match the flow side, so the two owner types agree on one order of
//	                    magnitude instead of the old 10000 / 100 under the same name.
//
// The upper bounds a tenant may reach are the system row's job (see platform/sql/agent_configs.sql); these numbers are
// the floor a fresh tenant inherits.
var fallback = Limits{
	MaxIterations:     40,
	MaxCallDepth:      3,
	MaxTools:          60,
	MaxOutputBytes:    64 << 10,
	MaxRunRecords:     100,
	MaxSessionRecords: 50,
}

// Row is one agent_configs row with NULL ("not set") preserved; a nil *Row means "there is no such row".
//
// It is exported so both HTTP layers can build the submitted row, hand it to ValidateTenant, and render it with JSON
// without each of them writing its own copy of the six columns.
type Row struct {
	MaxIterations     sql.NullInt32
	MaxCallDepth      sql.NullInt32
	MaxTools          sql.NullInt32
	MaxOutputBytes    sql.NullInt32
	MaxRunRecords     sql.NullInt32
	MaxSessionRecords sql.NullInt32
}

// tableName is the fully qualified table, following the configured platform schema.
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".agent_configs"
}

// fieldNames are the six columns in the order JSON, validation and diagnostics walk them. The names are the database /
// request keys, so an error's params.field uses the same word as the form the user is editing.
var fieldNames = [6]string{"max_iterations", "max_call_depth", "max_tools", "max_output_bytes", "max_run_records", "max_session_records"}

// values returns the six columns in fieldNames order; a nil row reads as "everything unset".
func (r *Row) values() [6]sql.NullInt32 {
	if r == nil {
		return [6]sql.NullInt32{}
	}
	return [6]sql.NullInt32{r.MaxIterations, r.MaxCallDepth, r.MaxTools, r.MaxOutputBytes, r.MaxRunRecords, r.MaxSessionRecords}
}

// JSON renders the raw row in the shape the frontend expects (NULL -> null). A nil row yields four nulls, so "no row" and
// "a row that is entirely unset" look the same to the UI instead of one of them losing its keys.
func (r *Row) JSON() map[string]any {
	v := r.values()
	out := make(map[string]any, len(fieldNames))
	for i, name := range fieldNames {
		if v[i].Valid {
			out[name] = v[i].Int32
		} else {
			out[name] = nil
		}
	}
	return out
}

// JSON renders the effective values; unlike Row.JSON they are never null (every field has a concrete number).
func (l Limits) JSON() map[string]any {
	return map[string]any{
		fieldNames[0]: l.MaxIterations,
		fieldNames[1]: l.MaxCallDepth,
		fieldNames[2]: l.MaxTools,
		fieldNames[3]: l.MaxOutputBytes,
		fieldNames[4]: l.MaxRunRecords,
		fieldNames[5]: l.MaxSessionRecords,
	}
}

// Error codes returned by ValidateValues / ValidateTenant. The user-facing copy lives in the desktop bundles
// (desktop/src/locales/{en,zh-CN}/error.json, see nexus/docs/error-codes.md); the text on FieldError is English
// diagnostics for logs and curl, as with every other code.
const (
	CodeInvalidValue       = "agentconfig.invalid_value"
	CodeExceedsSystemLimit = "agentconfig.exceeds_system_limit"
)

// FieldError is one rejected value, shaped so the HTTP layer can answer with an error code instead of a sentence:
// Code is the envelope's `error`, Params the scalars the bundled copy interpolates, and the Error() text is diagnostics.
type FieldError struct {
	Field  string
	Code   string
	Params map[string]any
	text   string
}

func (e *FieldError) Error() string { return e.text }

// ValidateValues rejects every set value below 1. This table deliberately has no -1: all six fields are safety valves,
// and "unlimited" would switch the loop, recursion, context and disk guards off at once.
func ValidateValues(r *Row) *FieldError {
	v := r.values()
	for i := range fieldNames {
		if v[i].Valid && v[i].Int32 < 1 {
			return &FieldError{
				Field:  fieldNames[i],
				Code:   CodeInvalidValue,
				Params: map[string]any{"field": fieldNames[i], "min": 1},
				text:   fmt.Sprintf("%s must be an integer >= 1 (this config does not support -1 for unlimited)", fieldNames[i]),
			}
		}
	}
	return nil
}

// ValidateTenant rejects a set value below 1 and any value above the system level.
//
// An unset system level means unlimited, which is the write-side rule the flow settings use too. Note the runtime is
// stricter in exactly one case: with no system row it clamps to the code fallback, so a tenant may store a value the run
// will not use. The tenant read endpoints therefore also return Effective, so the UI can show what will actually apply.
func ValidateTenant(tenant, sys *Row) *FieldError {
	if err := ValidateValues(tenant); err != nil {
		return err
	}
	tv, sv := tenant.values(), sys.values()
	for i := range fieldNames {
		// A system value <= 0 cannot come from the API (CHECK constraint); only a direct database edit produces it, and
		// the runtime already treats it as "not set", so it must not turn into an upper bound here either.
		if !tv[i].Valid || !sv[i].Valid || sv[i].Int32 <= 0 {
			continue
		}
		if tv[i].Int32 > sv[i].Int32 {
			return &FieldError{
				Field:  fieldNames[i],
				Code:   CodeExceedsSystemLimit,
				Params: map[string]any{"field": fieldNames[i], "limit": sv[i].Int32},
				text:   fmt.Sprintf("%s cannot exceed the system value %d", fieldNames[i], sv[i].Int32),
			}
		}
	}
	return nil
}

// Effective merges two already-loaded rows into the values that will actually apply:
// tenant row -> system row -> fallback, then clamped by the system value (which itself falls back to the code default
// when unset). An empty tenantID at the call site means "system level", i.e. pass nil as the tenant row.
//
// The HTTP layers use it to show "what is in force" right next to the raw values, so a stored value that the runtime
// clamps is visible instead of silently ignored.
func Effective(tenant, sys *Row) Limits {
	t, s := tenant.values(), sys.values()
	return Limits{
		MaxIterations:     resolveField(t[0], s[0], fallback.MaxIterations),
		MaxCallDepth:      resolveField(t[1], s[1], fallback.MaxCallDepth),
		MaxTools:          resolveField(t[2], s[2], fallback.MaxTools),
		MaxOutputBytes:    resolveField(t[3], s[3], fallback.MaxOutputBytes),
		MaxRunRecords:     resolveField(t[4], s[4], fallback.MaxRunRecords),
		MaxSessionRecords: resolveField(t[5], s[5], fallback.MaxSessionRecords),
	}
}

// Resolve returns the effective runtime parameters of a tenant (tenant row -> system row -> fallback, then clamped by the
// system value). An empty tenantID means system level.
//
// One call reads two rows (tenant + system), both through the cache. **Do not call it in a loop**: at runtime the
// parameters should be snapshotted once at the start of a run (see agentImpl.limits in service/agent).
func Resolve(tenantID string) Limits {
	return Effective(loadRowCached(tenantID), loadRowCached(""))
}

// Fallback returns the built-in code defaults, so the admin UI can show "what it falls back to when left empty".
func Fallback() Limits {
	return fallback
}

// loadRow reads the configuration row of a tenant_id; a missing row returns (nil, nil) and a query error returns
// (nil, err).
func loadRow(tenantID string) (*Row, error) {
	var r Row
	err := db.DB.QueryRow(
		`SELECT max_iterations, max_call_depth, max_tools, max_output_bytes, max_run_records, max_session_records FROM `+tableName()+` WHERE tenant_id = $1`,
		tenantID,
	).Scan(&r.MaxIterations, &r.MaxCallDepth, &r.MaxTools, &r.MaxOutputBytes, &r.MaxRunRecords, &r.MaxSessionRecords)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// resolveField is the full merge of one field: tenant -> system -> fallback, then clamped by the system value.
//
// Two normalisation rules:
//   - a system value <= 0 (it should not exist in the table; only a direct database edit produces it) counts as "not set",
//     and the fallback still acts as the ceiling;
//   - a merged result <= 0 (a tenant row wrote -1 / 0) always falls back — a consumer must always get a positive number.
func resolveField(tenant, sys sql.NullInt32, fb int) int {
	ceiling := fb
	if sys.Valid && int(sys.Int32) > 0 {
		ceiling = int(sys.Int32)
	}

	v := fb
	switch {
	case tenant.Valid:
		v = int(tenant.Int32)
	case sys.Valid:
		v = int(sys.Int32)
	}
	if v > ceiling {
		v = ceiling // the system row is the hard ceiling
	}
	if v <= 0 {
		return fb
	}
	return v
}
