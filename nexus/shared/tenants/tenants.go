// Package tenants 租户注册表（platform.tenants）的只读白名单。
//
// 用途（热路径）：/login 与 MCP/RPC 的 resolveTenant 在把 schema 拼进 SQL 之前，
// 先确认它是"已注册且 active"的租户 ——
//
//  1. 收窄注入面：schema 会被拼进标识符位置（SELECT ... FROM <schema>.users）；
//     字符集校验只能挡住一部分，白名单把取值限制为"已注册的租户名"。
//  2. 让停用/删除租户立即生效：此前只要 DB 里 schema 还在，就永远能登录/调用。
//  3. 统一口径：控制台登录与服务侧解析从此共用同一判定。
//
// 缓存：不再使用进程内 sync.Map，改为 Redis 共享缓存（见 cache.go）；
// Redis 未启用/不可用时直连数据库。平台侧写库后调用 Invalidate 主动失效。
package tenants

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// 状态词表（与 platform.tenants.status 的 CHECK 约束一致）。
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusDeleted   = "deleted"
	StatusPending   = "pending"
)

// PrimaryUserName 租户根账号的登录名：平台开通租户时创建的第一个 manage 用户
// （platform/provision.DefaultAdminName 的值就取自这里）。
//
// 租户侧把它当**受保护账号**：不许修改（资料 / 角色 / 状态）也不许删除 —— 保证"平台开通时
// 交付的那个账号"始终是有效的 manage 且可登录。守卫实现在 nexus/admin/users 的 updateUser。
//
// 常量放 shared 是因为两侧都不能 import 对方（platform 不能引 nexus/admin，admin 不能引
// nexus/platform），只能共享这一份，别在任一侧另写字面量。
const PrimaryUserName = "primary"

const (
	// ttl 命中的注册表行的缓存有效期
	ttl = 2 * time.Minute
	// negativeTTL "未注册"负缓存的存活时间（防坏请求反复打库），刻意比正向短
	negativeTTL = 30 * time.Second
)

// Tenant 注册表一行的只读视图。
type Tenant struct {
	Schema      string
	DisplayName string
	Status      string
	Plan        string
	OwnerUserID string
}

func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".tenants"
}

// Get 返回注册表行；未注册返回 (nil, nil)。
//
// 查询出错时返回错误且**不写缓存** —— 调用方必须把错误当作"无法判定"并拒绝请求
// （fail-closed），不要当作"未注册"或"放行"静默处理。
func Get(schema string) (*Tenant, error) {
	key := strings.TrimSpace(schema)
	if key == "" {
		return nil, nil
	}
	if db.DB == nil {
		return nil, errors.New("database not initialized")
	}
	if t, ok := loadCache(key); ok {
		return t, nil // t 为 nil 表示"未注册"（负缓存命中）
	}

	var (
		t     Tenant
		disp  sql.NullString
		plan  sql.NullString
		owner sql.NullString
	)
	err := db.DB.QueryRow(
		`SELECT schema_name, COALESCE(display_name,''), COALESCE(status,''), COALESCE(plan,''),
		        COALESCE(owner_user_id::text,'')
		   FROM `+tableName()+` WHERE schema_name = $1`, key).
		Scan(&t.Schema, &disp, &t.Status, &plan, &owner)
	switch {
	case err == sql.ErrNoRows:
		storeCache(key, nil, negativeTTL)
		return nil, nil
	case err != nil:
		// 不缓存错误：下次请求重试，避免一次抖动被固定住
		return nil, err
	}

	t.DisplayName = disp.String
	t.Plan = plan.String
	t.OwnerUserID = owner.String
	storeCache(key, &t, ttl)
	return &t, nil
}

// IsActive 判断 schema 是否为「已注册且 active」的租户（热路径，走缓存）。
//
// 查询出错时返回错误：调用方应拒绝请求（fail-closed），不要当成 true/false。
func IsActive(schema string) (bool, error) {
	t, err := Get(schema)
	if err != nil {
		return false, err
	}
	return t != nil && t.Status == StatusActive, nil
}

// ListSchemas 返回全部租户 schema（按名排序）。
//
// 供启动时的一次性任务使用（如目录迁移），不走缓存。不筛 status：
// 已停用/已删除的租户目录同样要迁移，避免留下旧布局。
func ListSchemas() ([]string, error) {
	if db.DB == nil {
		return nil, errors.New("database not initialized")
	}
	rows, err := db.DB.Query(`SELECT schema_name FROM ` + tableName() + ` ORDER BY schema_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, 8)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
