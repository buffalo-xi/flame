// Package listingpolicy 租户上架资产的审核策略（全局默认 + 租户覆盖）。
//
// 形状与 capability_gates / plugin_gates 一致：缺省是「上架免审」（直接上架），要审核的
// 租户/场景显式写行。这与其它闸门「缺省拒绝」相反，是刻意的：上架审核对租户是日常摩擦，
// 而资产市场的入口本来就已经被「上架方主动上架 + 安装方自己决定装不装」筛过一遍。
// 两级：
//
//	platform.listing_settings  全局默认（单行 id=1，没有行 = 免审）
//	platform.listing_policies  按租户覆盖（有行以行为准）
//
// 放库不放 config.yaml：这是治理策略，改它的人应当是有平台账号的管理员，
// 而不是「能登上服务器改文件再重启」的人。
//
// 只影响「租户上架资产」，不影响「从远端市场拉取」——后者看 peer_servers.install_policy。
package listingpolicy

import (
	"database/sql"
	"strings"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"

	"github.com/lib/pq"
)

func table(name string) string {
	return pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema) + "." + name
}

// Default 全局默认（没有行 = 免审）。
//
// 注意与「读库失败」的区别：没有行是正常的初始态（免审），查询出错则按必须审核返回 ——
// 策略表坏掉时宁可多一次人工确认，也不能让未经审核的内容顺着故障进市场。
func Default() (bool, error) {
	var v bool
	err := db.DB.QueryRow(`SELECT require_review FROM ` + table("listing_settings") + ` WHERE id = 1`).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return v, nil
}

// RequireReview 该租户上架时是否必须审核：有覆盖用覆盖，否则回落全局默认。
//
// 覆盖行读失败一律按「必须审核」返回（fail closed）：策略表出问题时宁可多一次人工确认，
// 也不能让未审内容直接进市场。
func RequireReview(schema string) (bool, error) {
	schema = strings.TrimSpace(schema)
	if schema != "" {
		var v bool
		err := db.DB.QueryRow(`SELECT require_review FROM `+table("listing_policies")+
			` WHERE schema_name = $1`, schema).Scan(&v)
		if err == nil {
			return v, nil
		}
		if err != sql.ErrNoRows {
			return true, err
		}
	}
	return Default()
}

// SetDefault 改全局默认。
func SetDefault(require bool, by string) error {
	var byVal any
	if by = strings.TrimSpace(by); by != "" {
		byVal = by
	}
	_, err := db.DB.Exec(`INSERT INTO `+table("listing_settings")+
		` (id, require_review, updated_at, updated_by) VALUES (1, $1, now(), $2::uuid)
		 ON CONFLICT (id) DO UPDATE SET require_review = EXCLUDED.require_review,
		     updated_at = now(), updated_by = EXCLUDED.updated_by`, require, byVal)
	return err
}

// SetOverride 设/清某租户的覆盖：require 为 nil = 删掉这一行（回到全局默认）。
//
// 用「有没有行」表达「跟不跟随默认」，而不是再存一个三态字符串：
// 三态列迟早会出现「值是 inherit 但还带着另一列」的自相矛盾行。
func SetOverride(schema string, require *bool, by string) error {
	if require == nil {
		_, err := db.DB.Exec(`DELETE FROM `+table("listing_policies")+` WHERE schema_name = $1`, schema)
		return err
	}
	var byVal any
	if by = strings.TrimSpace(by); by != "" {
		byVal = by
	}
	_, err := db.DB.Exec(`INSERT INTO `+table("listing_policies")+
		` (schema_name, require_review, updated_at, updated_by) VALUES ($1, $2, now(), $3::uuid)
		 ON CONFLICT (schema_name) DO UPDATE SET require_review = EXCLUDED.require_review,
		     updated_at = now(), updated_by = EXCLUDED.updated_by`, schema, *require, byVal)
	return err
}

// ListOverrides 已设置覆盖的租户（schema_name → 是否必须审核）。
func ListOverrides() (map[string]bool, error) {
	rows, err := db.DB.Query(`SELECT schema_name, require_review FROM ` + table("listing_policies"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			name string
			v    bool
		)
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, rows.Err()
}
