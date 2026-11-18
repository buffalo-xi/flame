// Package pmusers 平台用户表（platform.users）的数据访问。
//
// 平台用户（管理员 admin / 用户 user）与各租户 <schema>.users 完全无关
// （同名但不同物：这里是平台登录身份，那里是租户内账号）：
// 密码、状态、角色都在本表，平台登录只查这张表。
//
// 注意：平台表一律显式带 schema 前缀引用，禁止裸写 users ——
// 租户 schema 里也有同名表，漏前缀会静默命中错误的表。
package pmusers

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// User platform.users 一行（Password 为 bcrypt 哈希）。
type User struct {
	ID          string
	Name        string // 登录名，唯一且不可改
	DisplayName string
	Password    string
	Role        string // admin | user
	Status      string // active | disabled | locked
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func tableName() string { return config.Cfg.Database.PlatformSchema + ".users" }

const columns = `id, name, COALESCE(display_name,''), COALESCE(password,''), COALESCE(role,''), COALESCE(status,''),
                 created_at, updated_at`

// scanner 兼容 *sql.Row 与 *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner, u *User) error {
	return s.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Password, &u.Role, &u.Status, &u.CreatedAt, &u.UpdatedAt)
}

// GetByName 按登录名取用户；不存在返回 (nil, nil)。
func GetByName(name string) (*User, error) {
	var u User
	err := scan(db.DB.QueryRow(
		`SELECT `+columns+` FROM `+tableName()+` WHERE name = $1`, strings.TrimSpace(name)), &u)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByID 按 id 取用户；不存在返回 (nil, nil)。
func GetByID(id string) (*User, error) {
	var u User
	err := scan(db.DB.QueryRow(
		`SELECT `+columns+` FROM `+tableName()+` WHERE id = $1`, strings.TrimSpace(id)), &u)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// filter 构造筛选条件（WHERE 子句 + 参数）。
// List 与 Count 必须用同一套条件，抽出来避免两处漂移。
func filter(role, status, q string) (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if r := strings.TrimSpace(role); r != "" {
		args = append(args, r)
		where = append(where, fmt.Sprintf("role = $%d", len(args)))
	}
	if s := strings.TrimSpace(status); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if kw := strings.TrimSpace(q); kw != "" {
		args = append(args, "%"+strings.ToLower(kw)+"%")
		where = append(where, fmt.Sprintf("(LOWER(name) LIKE $%d OR LOWER(display_name) LIKE $%d)", len(args), len(args)))
	}
	return strings.Join(where, " AND "), args
}

// List 列出平台用户。role / status 为空表示不过滤；q 对 name 与 display_name 做不区分大小写的模糊匹配。
// limit <= 0 表示不分页（返回全部）；否则按 LIMIT/OFFSET 取一页，offset 从 0 起。
func List(role, status, q string, limit, offset int) ([]User, error) {
	cond, args := filter(role, status, q)
	sqlStr := `SELECT ` + columns + ` FROM ` + tableName() + ` WHERE ` + cond + ` ORDER BY role, name`
	if limit > 0 {
		args = append(args, limit, offset)
		sqlStr += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}
	rows, err := db.DB.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]User, 0)
	for rows.Next() {
		var u User
		if err := scan(rows, &u); err != nil {
			return nil, err
		}
		items = append(items, u)
	}
	return items, rows.Err()
}

// Count 统计满足条件的平台用户数（与 List 同条件）——分页的 total 用它，而不是 len(items)。
func Count(role, status, q string) (int, error) {
	cond, args := filter(role, status, q)
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM `+tableName()+` WHERE `+cond, args...).Scan(&n)
	return n, err
}

// Create 新建平台用户（id 由调用方生成，Password 需为已哈希值）。
func Create(u *User) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+` (id, name, display_name, password, role, status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,NOW(),NOW())`,
		u.ID, u.Name, u.DisplayName, u.Password, u.Role, u.Status)
	return err
}

// UpdateProfile 更新显示名 / 角色 / 状态（name 不可改：它是登录名与审计锚点）。
func UpdateProfile(id, displayName, role, status string) error {
	_, err := db.DB.Exec(
		`UPDATE `+tableName()+` SET display_name=$2, role=$3, status=$4, updated_at=NOW() WHERE id=$1`,
		strings.TrimSpace(id), displayName, role, status)
	return err
}

// UpdatePassword 重置密码（hash 需为 bcrypt 值）。
func UpdatePassword(id, hash string) error {
	_, err := db.DB.Exec(
		`UPDATE `+tableName()+` SET password=$2, updated_at=NOW() WHERE id=$1`, strings.TrimSpace(id), hash)
	return err
}

// CountActiveAdmins 统计「可登录的管理员」数量。
// 用于「不能停用/降级最后一个管理员」的守卫（否则平台自锁，只能改库救场）。
//
// 唯一调用点是「不能停用/降级最后一个管理员」的守卫（handler/users.go）——
// 否则平台自锁，只能改库救场。
//
// 仍显式判空连接池：database/sql 对 nil *DB 会 panic（真实踩过）。
func CountActiveAdmins() (int, error) {
	if db.DB == nil {
		return 0, errors.New("database not initialized")
	}
	var n int
	err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM ` + tableName() + ` WHERE role = 'admin' AND status = 'active'`).Scan(&n)
	return n, err
}
