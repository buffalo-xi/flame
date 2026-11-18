// Package tenants 租户注册表（platform.tenants）的数据访问 —— 平台侧 CRUD。
//
// 注意：租户侧热路径上的白名单读取在 shared/tenants（带 TTL 缓存），两处共用同一张表。
// 平台侧任何写操作之后必须调用 shared/tenants.Invalidate(schema)，
// 否则"停用租户"最多要等一个 TTL 周期才生效。
package tenants

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// Tenant platform.tenants 一行。
type Tenant struct {
	Schema      string
	DisplayName string
	Status      string
	Plan        string
	OwnerUserID string // 负责人（platform.users.id）；空表示无（管理员代管）
	Remark      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func tableName() string { return config.Cfg.Database.PlatformSchema + ".tenants" }

const columns = `schema_name, COALESCE(display_name,''), COALESCE(status,''), COALESCE(plan,''),
                 COALESCE(owner_user_id::text,''), COALESCE(remark,''), created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner, t *Tenant) error {
	return s.Scan(&t.Schema, &t.DisplayName, &t.Status, &t.Plan, &t.OwnerUserID, &t.Remark, &t.CreatedAt, &t.UpdatedAt)
}

// Get 按 schema 取租户；不存在返回 (nil, nil)。
func Get(schema string) (*Tenant, error) {
	var t Tenant
	err := scan(db.DB.QueryRow(
		`SELECT `+columns+` FROM `+tableName()+` WHERE schema_name = $1`, strings.TrimSpace(schema)), &t)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Exists schema 是否已注册（含已停用/软删）。
func Exists(schema string) (bool, error) {
	t, err := Get(schema)
	return t != nil, err
}

// filter 构造筛选条件（WHERE 子句 + 参数）。
// List 与 Count 必须用同一套条件，抽出来避免两处漂移。
func filter(status, q string) (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if s := strings.TrimSpace(status); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if kw := strings.TrimSpace(q); kw != "" {
		args = append(args, "%"+strings.ToLower(kw)+"%")
		where = append(where, fmt.Sprintf("(LOWER(schema_name) LIKE $%d OR LOWER(display_name) LIKE $%d)", len(args), len(args)))
	}
	return strings.Join(where, " AND "), args
}

// List 列出租户。status 为空表示不过滤；q 匹配 schema_name / display_name（不区分大小写）。
// limit <= 0 表示不分页（返回全部）；否则按 LIMIT/OFFSET 取一页，offset 从 0 起。
func List(status, q string, limit, offset int) ([]Tenant, error) {
	cond, args := filter(status, q)
	sqlStr := `SELECT ` + columns + ` FROM ` + tableName() + ` WHERE ` + cond + ` ORDER BY schema_name`
	if limit > 0 {
		args = append(args, limit, offset)
		sqlStr += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}
	return query(sqlStr, args...)
}

// Count 统计满足条件的租户数（与 List 同条件）——分页的 total 用它，而不是 len(items)。
func Count(status, q string) (int, error) {
	cond, args := filter(status, q)
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM `+tableName()+` WHERE `+cond, args...).Scan(&n)
	return n, err
}

// ListByOwner 列出某用户名下的租户（用户身份进管理台时用）。
func ListByOwner(ownerUserID string) ([]Tenant, error) {
	return query(
		`SELECT `+columns+` FROM `+tableName()+
			` WHERE owner_user_id = $1 AND status <> 'deleted' ORDER BY schema_name`,
		strings.TrimSpace(ownerUserID))
}

// CountByOwner 统计某用户名下的租户数（停用该用户前提示用）。
func CountByOwner(ownerUserID string) (int, error) {
	var n int
	err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM `+tableName()+` WHERE owner_user_id = $1 AND status <> 'deleted'`,
		strings.TrimSpace(ownerUserID)).Scan(&n)
	return n, err
}

// Create 写入注册表行（schema 本身由 provision 负责创建，见 nexus/platform/provision）。
func Create(t *Tenant, createdBy string) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+`
		   (schema_name, display_name, status, plan, owner_user_id, remark, created_at, updated_at, created_by)
		 VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,NOW(),NOW(),NULLIF($7,'')::uuid)`,
		t.Schema, t.DisplayName, t.Status, t.Plan, t.OwnerUserID, t.Remark, createdBy)
	return err
}

// Update 改显示名 / 备注 / 负责人（ownerUserID 传空串表示清空负责人）。
func Update(schema, displayName, remark, ownerUserID string) error {
	_, err := db.DB.Exec(
		`UPDATE `+tableName()+`
		    SET display_name=$2, remark=$3, owner_user_id=NULLIF($4,'')::uuid, updated_at=NOW()
		  WHERE schema_name=$1`,
		strings.TrimSpace(schema), displayName, remark, strings.TrimSpace(ownerUserID))
	return err
}

// SetStatus 改租户状态（active / suspended / deleted / pending）。
func SetStatus(schema, status string) error {
	_, err := db.DB.Exec(
		`UPDATE `+tableName()+` SET status=$2, updated_at=NOW() WHERE schema_name=$1`,
		strings.TrimSpace(schema), strings.TrimSpace(status))
	return err
}

// SetQuota 覆盖某租户的配额行（platform.quota_configs）。
// 调用方写完需自行 quota.Invalidate(schema)（本包不引 shared/quota，避免与配额语义耦合）。
func SetQuota(schema string, maxAgents, maxSkills, maxFlows *int,
	maxSessionStorageBytes *int64, maxUserSessions *int) error {
	return setQuotaRow(schema, maxAgents, maxSkills, maxFlows, maxSessionStorageBytes, maxUserSessions)
}

// SetSystemQuota 覆盖**系统行**（tenant_id = ”）：租户未单独配置时的默认上限。
// 调用方写完需自行 quota.InvalidateAll()（系统行一变，每个租户缓存的那一格都过时了）。
func SetSystemQuota(maxAgents, maxSkills, maxFlows *int,
	maxSessionStorageBytes *int64, maxUserSessions *int) error {
	return setQuotaRow("", maxAgents, maxSkills, maxFlows, maxSessionStorageBytes, maxUserSessions)
}

// QuotaRow quota_configs 的原始一行：nil = NULL = 未设置（**不做**系统行回退）。
type QuotaRow struct {
	MaxAgents *int
	MaxSkills *int
	MaxFlows  *int

	// MaxSessionStorageBytes 单会话空间上限（字节）；nil = 回退系统行。
	MaxSessionStorageBytes *int64
	// MaxUserSessions 单用户会话数上限；nil = 回退系统行。
	MaxUserSessions *int
}

// GetQuotaRow 读原始配额行；无该行时返回零值结构（全 nil）。
//
// 管理界面必须看原始值，才能区分"未设置"与"显式设成某个数"；
// 需要"最终生效值"请走 shared/quota.Resolve（它会做 tenant -> sys -> fallback 回退）。
func GetQuotaRow(tenantID string) (*QuotaRow, error) {
	var agents, skills, flows sql.NullInt32
	var sessBytes sql.NullInt64
	var userSessions sql.NullInt32
	err := db.DB.QueryRow(
		`SELECT max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions
		   FROM `+quotaTable()+` WHERE tenant_id = $1`,
		strings.TrimSpace(tenantID)).Scan(&agents, &skills, &flows, &sessBytes, &userSessions)
	if err == sql.ErrNoRows {
		return &QuotaRow{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &QuotaRow{
		MaxAgents:              nullInt(agents),
		MaxSkills:              nullInt(skills),
		MaxFlows:               nullInt(flows),
		MaxSessionStorageBytes: nullInt64(sessBytes),
		MaxUserSessions:        nullInt(userSessions),
	}, nil
}

// setQuotaRow 覆盖 quota_configs 的某一行（tenantID 传空串即系统行）。
//
// 语义沿用既有约定：NULL = 未设置（回退系统行）、-1 = 不限制、正数 = 上限。
// 用「事务内先删后插」而不是 UPSERT：不依赖该表上唯一的约束名/形态。
func setQuotaRow(tenantID string, maxAgents, maxSkills, maxFlows *int,
	maxSessionStorageBytes *int64, maxUserSessions *int) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	tbl := quotaTable()
	id := strings.TrimSpace(tenantID)
	if _, err := tx.Exec(`DELETE FROM `+tbl+` WHERE tenant_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO `+tbl+`
		   (tenant_id, max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		id, quotaArg(maxAgents), quotaArg(maxSkills), quotaArg(maxFlows),
		quotaArg64(maxSessionStorageBytes), quotaArg(maxUserSessions)); err != nil {
		return err
	}
	return tx.Commit()
}

func quotaTable() string {
	return config.Cfg.Database.PlatformSchema + ".quota_configs"
}

// nullInt sql.NullInt32 → *int（Invalid → nil）
func nullInt(v sql.NullInt32) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}

// quotaArg nil → NULL（未设置），否则写入原值（-1 表示不限制）。
func quotaArg(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// nullInt64 sql.NullInt64 → *int64（Invalid → nil）
func nullInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// quotaArg64 同 quotaArg，用于 bigint 列（会话空间上限）。
func quotaArg64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func query(sqlStr string, args ...any) ([]Tenant, error) {
	rows, err := db.DB.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Tenant, 0)
	for rows.Next() {
		var t Tenant
		if err := scan(rows, &t); err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}
