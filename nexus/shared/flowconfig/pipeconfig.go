package flowconfig

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// FlowLimits 最终生效的 flow 配置（租户/系统两级硬顶 + 回退 + 截断）
// 语义：每个 max_* 既是「硬顶」也是「实例未设置时的兜底值」。
type FlowLimits struct {
	MaxTimeout        int
	MaxRecursionDepth int
	MaxNodeCount      int
	MaxExecCount      int
	MaxLoopTimes      int
	MaxRunRecords     int
	MaxSessionRecords int
}

// fallback 硬编码兜底上限值（flow_configs 未配置时的系统级缺省）
// 语义约定：NULL=未设置（回退下级）；-1=不限制（无限大，有效值不回退）
var fallback = FlowLimits{
	MaxTimeout:        300,
	MaxRecursionDepth: 10,
	MaxNodeCount:      100,
	MaxExecCount:      200,
	MaxLoopTimes:      10000,
	MaxRunRecords:     100,
	MaxSessionRecords: 50,
}

// row 对应 flow_configs 一行，NULL 用 sql.NullInt32 表达
type row struct {
	MaxTimeout        sql.NullInt32
	MaxRecursionDepth sql.NullInt32
	MaxNodeCount      sql.NullInt32
	MaxExecCount      sql.NullInt32
	MaxLoopTimes      sql.NullInt32
	MaxRunRecords     sql.NullInt32
	MaxSessionRecords sql.NullInt32
}

// tableName 返回 flow_configs 的带 schema 全名
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".flow_configs"
}

// Resolve 返回指定租户最终生效的 flow 配置（租户/系统两级回退 + 截断）。
// 数据来源：Redis 共享缓存（见 cache.go），Redis 未启用/不可用时直连数据库。
// 本函数不做进程内缓存：调用方按需自行快照（运行期见 flow.ExecContext.Limits）。
// tenantID 为空字符串表示系统级。
func Resolve(tenantID string) FlowLimits {
	return merge(loadRowCached(tenantID), loadRowCached(""))
}

// Fallback 返回代码内置兜底值（系统行未设置时用），供管理界面展示"不填会退到哪"。
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

// pick 两级回退：tenant -> sys -> fallback。只有 NULL（未设置）才回退下级。
func pick(tenant, sys sql.NullInt32, fb int) int {
	if tenant.Valid {
		return int(tenant.Int32)
	}
	if sys.Valid {
		return int(sys.Int32)
	}
	return fb
}

// clampMax 上限截断：tenantVal 相对 ceiling 取 min，-1 视为无限大。
func clampMax(v, ceiling int) int {
	if ceiling == -1 { // 系统本身不限制
		return v
	}
	if v == -1 { // 租户不限制（无限大），但系统有限制
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

// field 安全取指定字段，r 为 nil（无该行）时返回空 NullInt32
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
