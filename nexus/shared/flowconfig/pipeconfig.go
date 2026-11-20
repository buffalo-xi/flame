package flowconfig

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// FlowLimits are the effective flow configuration (two-level hard ceiling for tenant / system, plus fallback and
// clamping).
// Semantics: every max_* is both "a hard ceiling" and "the fallback when the instance does not set it".
type FlowLimits struct {
	MaxTimeout        int
	MaxRecursionDepth int
	MaxNodeCount      int
	MaxExecCount      int
	MaxLoopTimes      int
	MaxRunRecords     int
	MaxSessionRecords int
}

// fallback holds the hard-coded default limits (the system-level default while flow_configs is not configured).
// Semantic convention: NULL = not set (fall back to the next level); -1 = unlimited (infinity, the effective value never
// falls back).
var fallback = FlowLimits{
	MaxTimeout:        300,
	MaxRecursionDepth: 10,
	MaxNodeCount:      100,
	MaxExecCount:      200,
	MaxLoopTimes:      10000,
	MaxRunRecords:     100,
	MaxSessionRecords: 50,
}

// row maps one flow_configs row, with NULL expressed through sql.NullInt32.
type row struct {
	MaxTimeout        sql.NullInt32
	MaxRecursionDepth sql.NullInt32
	MaxNodeCount      sql.NullInt32
	MaxExecCount      sql.NullInt32
	MaxLoopTimes      sql.NullInt32
	MaxRunRecords     sql.NullInt32
	MaxSessionRecords sql.NullInt32
}

// tableName returns the schema-qualified name of flow_configs.
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".flow_configs"
}

// Resolve returns the effective flow configuration of a tenant (two-level tenant / system fallback plus clamping).
// The source is the shared Redis cache (see cache.go); while Redis is unavailable it queries the database directly.
// This function keeps no in-process cache: the caller snapshots it as needed (at runtime see flow.ExecContext.Limits).
// An empty tenantID means system level.
func Resolve(tenantID string) FlowLimits {
	return merge(loadRowCached(tenantID), loadRowCached(""))
}

// Fallback returns the built-in code defaults (used while the system row is not set), so the admin UI can show "what it
// falls back to when left empty".
func Fallback() FlowLimits {
	return fallback
}

func loadRow(tenantID string) (*row, error) {
	q := `SELECT max_timeout, max_recursion_depth, max_node_count, max_exec_count,
		max_loop_times, max_run_records, max_session_records FROM ` + tableName() + ` WHERE tenant_id = $1`

	var r row
	err := db.DB.QueryRow(q, tenantID).Scan(
		&r.MaxTimeout, &r.MaxRecursionDepth, &r.MaxNodeCount, &r.MaxExecCount,
		&r.MaxLoopTimes, &r.MaxRunRecords, &r.MaxSessionRecords,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// pick is the two-level fallback: tenant -> sys -> fallback. Only NULL (not set) falls back to the next level.
func pick(tenant, sys sql.NullInt32, fb int) int {
	if tenant.Valid {
		return int(tenant.Int32)
	}
	if sys.Valid {
		return int(sys.Int32)
	}
	return fb
}

// clampMax clamps to the ceiling: tenantVal takes min against ceiling, and -1 counts as infinity.
func clampMax(v, ceiling int) int {
	if ceiling == -1 { // the system itself is unlimited
		return v
	}
	if v == -1 { // the tenant is unlimited (infinity), but the system is limited
		return ceiling
	}
	if v > ceiling {
		return ceiling
	}
	return v
}

func merge(tenant, sys *row) FlowLimits {
	l := FlowLimits{
		MaxTimeout: clampMax(
			pick(field(tenant, "MaxTimeout"), field(sys, "MaxTimeout"), fallback.MaxTimeout),
			pick(sql.NullInt32{}, field(sys, "MaxTimeout"), fallback.MaxTimeout),
		),
		MaxRecursionDepth: clampMax(
			pick(field(tenant, "MaxRecursionDepth"), field(sys, "MaxRecursionDepth"), fallback.MaxRecursionDepth),
			pick(sql.NullInt32{}, field(sys, "MaxRecursionDepth"), fallback.MaxRecursionDepth),
		),
		MaxNodeCount: clampMax(
			pick(field(tenant, "MaxNodeCount"), field(sys, "MaxNodeCount"), fallback.MaxNodeCount),
			pick(sql.NullInt32{}, field(sys, "MaxNodeCount"), fallback.MaxNodeCount),
		),
		MaxExecCount: clampMax(
			pick(field(tenant, "MaxExecCount"), field(sys, "MaxExecCount"), fallback.MaxExecCount),
			pick(sql.NullInt32{}, field(sys, "MaxExecCount"), fallback.MaxExecCount),
		),
		MaxLoopTimes: clampMax(
			pick(field(tenant, "MaxLoopTimes"), field(sys, "MaxLoopTimes"), fallback.MaxLoopTimes),
			pick(sql.NullInt32{}, field(sys, "MaxLoopTimes"), fallback.MaxLoopTimes),
		),
		MaxRunRecords: clampMax(
			pick(field(tenant, "MaxRunRecords"), field(sys, "MaxRunRecords"), fallback.MaxRunRecords),
			pick(sql.NullInt32{}, field(sys, "MaxRunRecords"), fallback.MaxRunRecords),
		),
		MaxSessionRecords: clampMax(
			pick(field(tenant, "MaxSessionRecords"), field(sys, "MaxSessionRecords"), fallback.MaxSessionRecords),
			pick(sql.NullInt32{}, field(sys, "MaxSessionRecords"), fallback.MaxSessionRecords),
		),
	}
	return l
}

// field safely reads the named field, returning an empty NullInt32 when r is nil (no such row).
func field(r *row, name string) sql.NullInt32 {
	if r == nil {
		return sql.NullInt32{}
	}
	switch name {
	case "MaxTimeout":
		return r.MaxTimeout
	case "MaxRecursionDepth":
		return r.MaxRecursionDepth
	case "MaxNodeCount":
		return r.MaxNodeCount
	case "MaxExecCount":
		return r.MaxExecCount
	case "MaxLoopTimes":
		return r.MaxLoopTimes
	case "MaxRunRecords":
		return r.MaxRunRecords
	case "MaxSessionRecords":
		return r.MaxSessionRecords
	}
	return sql.NullInt32{}
}
