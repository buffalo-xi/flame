// Package agentconfig 提供智能体运行参数（platform.agent_configs）的读取与合并。
//
// 语义与 shared/flowconfig、shared/quota 一致：
//   - tenant_id = ” 为系统行（全局默认）；'t0' 为租户行（覆盖系统行）
//   - NULL = 未设置（回退：租户行 -> 系统行 -> 代码 fallback）
//   - 系统行同时是**硬顶**：运行期取 min(租户值, 系统值)，租户配得更高也没用
//
// 与 flow_configs 的唯一差别：本包**没有 -1（不限制）**。
// 这四个字段都是「安全阀」而不是「配额」—— max_iterations = -1 等于取消防死循环，
// max_call_depth = -1 等于允许无限递归，max_tools / max_output_bytes = -1 等于把
// 上下文和内存交给模型随意索取。所以写入入口一律拒绝 -1（见 sql/agent_configs.sql
// 的 CHECK 约束），本包再把 <= 0 归一成 fallback，防止绕过接口直接改库写出怪值。
//
// 缓存：Redis 共享缓存（见 cache.go）；Redis 未启用/不可用时直连数据库。
package agentconfig

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// Limits 最终生效的运行参数。每个值都 >= 1，消费点可以直接用，不必再做零值判断。
type Limits struct {
	MaxIterations  int
	MaxCallDepth   int
	MaxTools       int
	MaxOutputBytes int
}

// fallback 代码兜底默认值：与其出现之前的硬编码常量逐一对齐，
// 所以「表没建 / 系统行没配」时的行为与改造前完全一致。
//
//	MaxIterations  ← service/agent 的 maxIterations
//	MaxCallDepth   ← caller.go 的 maxCallDepth
//	MaxTools       ← toolgrant.DefaultMaxTools
//	MaxOutputBytes ← tools.DefaultMaxOutput
var fallback = Limits{
	MaxIterations:  80,
	MaxCallDepth:   3,
	MaxTools:       50,
	MaxOutputBytes: 64 << 10,
}

// row 对应 agent_configs 一行，NULL 用 sql.NullInt32 表达
type row struct {
	MaxIterations  sql.NullInt32
	MaxCallDepth   sql.NullInt32
	MaxTools       sql.NullInt32
	MaxOutputBytes sql.NullInt32
}

func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".agent_configs"
}

// Resolve 返回指定租户最终生效的运行参数（租户行 -> 系统行 -> fallback，再按系统值截断）。
// tenantID 为空字符串表示系统级。
//
// 一次调用读两行（租户行 + 系统行），都走缓存。**不要在循环里调**：
// 运行期应当在 run 开始时快照一次（见 service/agent 的 agentImpl.limits）。
func Resolve(tenantID string) Limits {
	t := loadRowCached(tenantID)
	s := loadRowCached("")
	return Limits{
		MaxIterations:  resolveField(field(t, "MaxIterations"), field(s, "MaxIterations"), fallback.MaxIterations),
		MaxCallDepth:   resolveField(field(t, "MaxCallDepth"), field(s, "MaxCallDepth"), fallback.MaxCallDepth),
		MaxTools:       resolveField(field(t, "MaxTools"), field(s, "MaxTools"), fallback.MaxTools),
		MaxOutputBytes: resolveField(field(t, "MaxOutputBytes"), field(s, "MaxOutputBytes"), fallback.MaxOutputBytes),
	}
}

// Fallback 返回代码内置兜底值，供管理界面展示「不填会退到哪」。
func Fallback() Limits {
	return fallback
}

// loadRow 读取某 tenant_id 的配置行；无该行返回 (nil, nil)，查询出错返回 (nil, err)。
func loadRow(tenantID string) (*row, error) {
	var r row
	err := db.DB.QueryRow(
		`SELECT max_iterations, max_call_depth, max_tools, max_output_bytes FROM `+tableName()+` WHERE tenant_id = $1`,
		tenantID,
	).Scan(&r.MaxIterations, &r.MaxCallDepth, &r.MaxTools, &r.MaxOutputBytes)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// resolveField 单字段的完整合并：租户 -> 系统 -> fallback，再按系统值截断。
//
// 两条归一规则：
//   - 系统值 <= 0（表里不该有，直改库才会出现）视为「未设置」，仍用 fallback 当顶；
//   - 合并结果 <= 0（租户行写了 -1 / 0）一律回落 fallback —— 让消费点永远拿到正数。
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
		v = ceiling // 系统行是硬顶
	}
	if v <= 0 {
		return fb
	}
	return v
}

// field 安全取指定字段，r 为 nil（无该行）时返回空 NullInt32
func field(r *row, name string) sql.NullInt32 {
	if r == nil {
		return sql.NullInt32{}
	}
	switch name {
	case "MaxIterations":
		return r.MaxIterations
	case "MaxCallDepth":
		return r.MaxCallDepth
	case "MaxTools":
		return r.MaxTools
	case "MaxOutputBytes":
		return r.MaxOutputBytes
	}
	return sql.NullInt32{}
}
