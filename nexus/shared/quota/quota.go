// Package quota 提供租户资源配额的读取与资源计数（platform.quota_configs）。
//
// 语义（沿用 flow_configs 约定）：
//   - tenant_id = ” 为系统行（全局默认）；'t0' 为租户行（覆盖系统行）
//   - NULL = 未设置（回退：租户行 -> 系统行 -> 代码 fallback）
//   - -1 = 不限制（有效值，不回退）
//
// 系统行同时是**硬顶**（与 flowconfig 同规则）：运行期取 min(租户值, 系统值)，
// 系统行为 -1 时不限 —— 所以租户行配得比系统行高也没用，会被截断到系统值；
// 系统行未设置时，用代码 fallback（100 / 300 / 300 / -1 / -1）当硬顶：
// 会话相关的两项默认不限制（单会话空间、单用户会话数），其余三项仍是有限值。
//
// 缓存：不再使用进程内 sync.Map，改为 Redis 共享缓存（见 cache.go）；
// Redis 未启用/不可用时直连数据库。
package quota

import (
	"database/sql"
	"strings"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// Limits 最终生效的配额上限（-1 表示不限制）。
type Limits struct {
	MaxAgents int
	MaxSkills int
	MaxFlows  int

	// MaxSessionStorageBytes 单个会话工作区的字节上限（超限时删该会话最旧的文件）。
	MaxSessionStorageBytes int64
	// MaxUserSessions 单个用户拥有的会话数量上限（超限拒绝新建，不删旧会话）。
	MaxUserSessions int
}

// fallback 代码兜底默认值（表未建、或表里未配置时使用）。
//
// 会话相关的两项取 -1（不限制）：会话是「工作场地」而不是可数资产，
// 默认设限的后果是到顶后连一轮都跑不了（存储 507 / 建会话 507），
// 而这两项在系统行、租户行、user_quotas 三处都能显式收紧。
var fallback = Limits{
	MaxAgents: 100, MaxSkills: 300, MaxFlows: 300,
	MaxSessionStorageBytes: -1, // 单会话工作区不限
	MaxUserSessions:        -1, // 单用户会话数不限
}

// row 对应 quota_configs 一行，NULL 用 Null 类型表达
type row struct {
	MaxAgents              sql.NullInt32
	MaxSkills              sql.NullInt32
	MaxFlows               sql.NullInt32
	MaxSessionStorageBytes sql.NullInt64
	MaxUserSessions        sql.NullInt32
}

func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".quota_configs"
}

// Resolve 返回指定租户最终生效的配额（租户行 -> 系统行 -> fallback）。
// 数据来源：Redis 共享缓存（见 cache.go）；Redis 未启用/不可用时直连数据库。
// tenantID 为空字符串表示系统级。
func Resolve(tenantID string) Limits {
	t := loadRowCached(tenantID)
	s := loadRowCached("")
	return Limits{
		MaxAgents: clampMax(
			pick(field(t, "MaxAgents"), field(s, "MaxAgents"), fallback.MaxAgents),
			pick(sql.NullInt32{}, field(s, "MaxAgents"), fallback.MaxAgents),
		),
		MaxSkills: clampMax(
			pick(field(t, "MaxSkills"), field(s, "MaxSkills"), fallback.MaxSkills),
			pick(sql.NullInt32{}, field(s, "MaxSkills"), fallback.MaxSkills),
		),
		MaxFlows: clampMax(
			pick(field(t, "MaxFlows"), field(s, "MaxFlows"), fallback.MaxFlows),
			pick(sql.NullInt32{}, field(s, "MaxFlows"), fallback.MaxFlows),
		),
		MaxSessionStorageBytes: clampMax64(
			pick64(field64(t, "MaxSessionStorageBytes"), field64(s, "MaxSessionStorageBytes"), fallback.MaxSessionStorageBytes),
			pick64(sql.NullInt64{}, field64(s, "MaxSessionStorageBytes"), fallback.MaxSessionStorageBytes),
		),
		MaxUserSessions: clampMax(
			pick(field(t, "MaxUserSessions"), field(s, "MaxUserSessions"), fallback.MaxUserSessions),
			pick(sql.NullInt32{}, field(s, "MaxUserSessions"), fallback.MaxUserSessions),
		),
	}
}

// Fallback 返回代码内置兜底默认值（系统行未设置时用），供管理界面展示"不填会退到哪"。
func Fallback() Limits {
	return fallback
}

// loadRow 读取某 tenant_id 的配置行；无该行返回 (nil, nil)，查询出错返回 (nil, err)。
func loadRow(tenantID string) (*row, error) {
	var r row
	err := db.DB.QueryRow(
		`SELECT max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions
		   FROM `+tableName()+` WHERE tenant_id = $1`,
		tenantID,
	).Scan(&r.MaxAgents, &r.MaxSkills, &r.MaxFlows, &r.MaxSessionStorageBytes, &r.MaxUserSessions)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Row 配额原始一行（nil = NULL = 未设置，回退系统行）。
//
// 管理界面必须看原始值才能区分「未设置」与「显式设成某个数」；要最终生效值请用 Resolve。
type Row struct {
	MaxAgents              *int
	MaxSkills              *int
	MaxFlows               *int
	MaxUserSessions        *int
	MaxSessionStorageBytes *int64
}

// GetRow 读某租户（空串 = 系统行）的原始配额行；无该行时返回全 nil 结构。
func GetRow(tenantID string) (*Row, error) {
	r, err := loadRow(strings.TrimSpace(tenantID))
	if err != nil {
		return nil, err
	}
	if r == nil {
		return &Row{}, nil
	}
	return &Row{
		MaxAgents:              nullIntPtr(r.MaxAgents),
		MaxSkills:              nullIntPtr(r.MaxSkills),
		MaxFlows:               nullIntPtr(r.MaxFlows),
		MaxUserSessions:        nullIntPtr(r.MaxUserSessions),
		MaxSessionStorageBytes: nullInt64Ptr(r.MaxSessionStorageBytes),
	}, nil
}

// SetRow 覆盖某租户（空串 = 系统行）的配额行并失效缓存。
// nil = 写 NULL（回退系统行）；-1 = 不限；正数 = 上限。
//
// 用「事务内先删后插」而不是 UPSERT：不依赖该表上唯一约束的形态。
func SetRow(tenantID string, v *Row) error {
	id := strings.TrimSpace(tenantID)
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM `+tableName()+` WHERE tenant_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO `+tableName()+`
		   (tenant_id, max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		id, nilIfNil(v.MaxAgents), nilIfNil(v.MaxSkills), nilIfNil(v.MaxFlows),
		nilIfNil64(v.MaxSessionStorageBytes), nilIfNil(v.MaxUserSessions)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if id == "" {
		InvalidateAll()
	} else {
		Invalidate(id)
	}
	return nil
}

func nullIntPtr(v sql.NullInt32) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func nilIfNil(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nilIfNil64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// field 安全取指定 int 字段，r 为 nil（无该行）时返回空 NullInt32
func field(r *row, name string) sql.NullInt32 {
	if r == nil {
		return sql.NullInt32{}
	}
	switch name {
	case "MaxAgents":
		return r.MaxAgents
	case "MaxSkills":
		return r.MaxSkills
	case "MaxFlows":
		return r.MaxFlows
	case "MaxUserSessions":
		return r.MaxUserSessions
	}
	return sql.NullInt32{}
}

// field64 安全取指定 int64 字段（字节数用，避开 int32 上限）。
func field64(r *row, name string) sql.NullInt64 {
	if r == nil {
		return sql.NullInt64{}
	}
	switch name {
	case "MaxSessionStorageBytes":
		return r.MaxSessionStorageBytes
	}
	return sql.NullInt64{}
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

// pick64 同 pick，用于 int64（字节数）。
func pick64(tenant, sys sql.NullInt64, fb int64) int64 {
	if tenant.Valid {
		return tenant.Int64
	}
	if sys.Valid {
		return sys.Int64
	}
	return fb
}

// clampMax 上限截断：v 相对 ceiling 取 min，-1 视为无限大（与 flowconfig 同规则）。
// 例：系统顶 100、租户配 500 → 100；系统顶 -1（不限）→ 原样返回租户值。
func clampMax(v, ceiling int) int {
	if ceiling == -1 {
		return v
	}
	if v == -1 {
		return ceiling
	}
	if v > ceiling {
		return ceiling
	}
	return v
}

// clampMax64 同 clampMax，用于 int64（字节数）。
func clampMax64(v, ceiling int64) int64 {
	if ceiling == -1 {
		return v
	}
	if v == -1 {
		return ceiling
	}
	if v > ceiling {
		return ceiling
	}
	return v
}

// ---------------------------------------------------------------------------
// 单用户覆盖（{schema}.user_quotas）
//
// 语义：**无行**或该列 NULL = 跟随租户；有值 = 覆盖，但不得超过租户值
// （租户管理员只能在平台给的天花板内分配）。
//
// 单独一张表而不是往 users 上加列：认证表不承载资源治理，
// 将来再加配额维度（总空间/文件数/并发）只动这张表。
// ---------------------------------------------------------------------------

// UserQuota 单用户的配额覆盖值，各自 Valid=false 表示跟随租户。
type UserQuota struct {
	MaxSessions            sql.NullInt32
	MaxSessionStorageBytes sql.NullInt64
}

// UserQuotaInput 覆盖值入参：nil = 跟随租户；两个都为 nil 时删除覆盖行。
type UserQuotaInput struct {
	MaxSessions            *int
	MaxSessionStorageBytes *int64
}

// ResolveUserSessions 返回某用户最终生效的会话数上限。
// 租户值（已 clamp 到系统行）是硬顶；用户只能在它之内调整。
func ResolveUserSessions(schema, userID string) int {
	base := Resolve(schema).MaxUserSessions
	if userID == "" {
		return base
	}
	q, ok, err := loadUserQuota(schema, userID)
	if err != nil || !ok || !q.MaxSessions.Valid {
		return base
	}
	return clampMax(int(q.MaxSessions.Int32), base)
}

// ResolveUserSessionStorage 返回某用户「每个会话」的空间上限：
// user 覆盖 → 租户 max_session_storage_bytes（clamp）。
func ResolveUserSessionStorage(schema, userID string) int64 {
	base := Resolve(schema).MaxSessionStorageBytes
	if userID == "" {
		return base
	}
	q, ok, err := loadUserQuota(schema, userID)
	if err != nil || !ok || !q.MaxSessionStorageBytes.Valid {
		return base
	}
	return clampMax64(q.MaxSessionStorageBytes.Int64, base)
}

// UserSessionsCeiling 返回该用户能配置的会话数硬顶（= 租户值），供界面提示与后端校验。
func UserSessionsCeiling(schema string) int {
	return Resolve(schema).MaxUserSessions
}

// UserSessionStorageCeiling 返回该用户能配置的会话空间硬顶（= 租户值）。
func UserSessionStorageCeiling(schema string) int64 {
	return Resolve(schema).MaxSessionStorageBytes
}

// SetUserQuota 写入/清除某用户的覆盖值（两个都为 nil = 删除行，恢复跟随租户）。
func SetUserQuota(schema, userID string, in UserQuotaInput, updatedBy string) error {
	if in.MaxSessions == nil && in.MaxSessionStorageBytes == nil {
		_, err := db.DB.Exec(`DELETE FROM `+schema+`.user_quotas WHERE user_id = $1`, userID)
		return err
	}
	_, err := db.DB.Exec(
		`INSERT INTO `+schema+`.user_quotas
		     (user_id, max_sessions, max_session_storage_bytes, updated_by, updated_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (user_id) DO UPDATE
		    SET max_sessions              = EXCLUDED.max_sessions,
		        max_session_storage_bytes = EXCLUDED.max_session_storage_bytes,
		        updated_by                = EXCLUDED.updated_by,
		        updated_at                = now()`,
		userID, in.MaxSessions, in.MaxSessionStorageBytes, nullUUID(updatedBy))
	return err
}

// loadUserQuota 读 user_quotas 一行；无行返回 (nil,false,nil)。
// 表不存在（老租户还没补表）时同样按"无行"处理 —— 配额照常按租户值工作，
// 而不是因为查不到表就拒绝建会话。
func loadUserQuota(schema, userID string) (*UserQuota, bool, error) {
	var q UserQuota
	err := db.DB.QueryRow(
		`SELECT max_sessions, max_session_storage_bytes
		   FROM `+schema+`.user_quotas WHERE user_id = $1`, userID,
	).Scan(&q.MaxSessions, &q.MaxSessionStorageBytes)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &q, true, nil
}

func nullUUID(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ---------------------------------------------------------------------------
// 资源计数（配额检查用）
//
// 与 flowconfig 一样，本包直接查库而不走 repository 接口，
// 避免为了"计数"而在三个仓储接口上各加一个方法。
// ---------------------------------------------------------------------------

// CountAgents 统计租户下的智能体数量。
func CountAgents(schema string) (int, error) {
	return countRows(schema, "agents")
}

// CountSkills 统计租户下的 skill 数量。
func CountSkills(schema string) (int, error) {
	return countRows(schema, "skills")
}

// CountFlows 统计租户下的工作流数量。
func CountFlows(schema string) (int, error) {
	return countRows(schema, "flows")
}

// CountUserSessions 统计某用户在指定 owner 类型下的会话数量。
// 走 sessions 表已有的 idx_sessions_user_latest，索引扫描，代价可忽略。
func CountUserSessions(schema, ownerType, userID string) (int, error) {
	var n int
	err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM `+schema+`.sessions WHERE owner_type = $1 AND user_id = $2`,
		ownerType, userID,
	).Scan(&n)
	return n, err
}

// countRows 统计某表行数。table 仅由本包内常量传入，不接受外部输入。
func countRows(schema, table string) (int, error) {
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + schema + `.` + table).Scan(&n)
	return n, err
}
