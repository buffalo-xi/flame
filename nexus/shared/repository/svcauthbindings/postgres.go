package svcauthbindings

import (
	"database/sql"
	"fmt"
	"strings"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// PostgresRepository PostgreSQL 实现的服务端认证绑定存储。
type PostgresRepository struct{}

// ResolveBindingIdentity 解析绑定并带出账号状态（LEFT JOIN，允许用户行缺失）。
// 是否放行由调用方判断（见 service/{mcp,rpc}/server/auth.go 的 authorizeBinding）。
func (r *PostgresRepository) ResolveBindingIdentity(schema, authType, issuer, subject string) (*BindingIdentity, error) {
	var bi BindingIdentity
	var bindingID string
	err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT b.id, b.user_id, COALESCE(u.status, ''), b.restrict_targets, b.exclude_supervisor
			FROM %s.svc_auth_bindings b
			LEFT JOIN %s.users u ON u.id = b.user_id
			WHERE b.auth_type = $1 AND b.external_issuer = $2 AND b.external_subject = $3
			  AND b.is_active = true`, schema, schema),
		authType, issuer, subject,
	).Scan(&bindingID, &bi.UserID, &bi.Status, &bi.RestrictTargets, &bi.ExcludeSupervisor)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	// 只有真的启用了限制才查白名单：未启用时省一次往返，
	// 也让「没跑迁移脚本」的存量租户仍能正常鉴权（不加限制就不碰新列/新表）。
	if bi.RestrictTargets {
		rows, qerr := db.DB.Query(
			fmt.Sprintf(`SELECT target_type, target_id FROM %s.svc_auth_binding_targets WHERE binding_id = $1`, schema),
			bindingID,
		)
		if qerr != nil {
			// fail-closed：策略读不到就不放行（由调用方回 401），绝不回落成「不限制」
			return nil, fmt.Errorf("load allowed targets: %w", qerr)
		}
		defer rows.Close()
		for rows.Next() {
			var tt, tid string
			if serr := rows.Scan(&tt, &tid); serr != nil {
				return nil, serr
			}
			bi.TargetAllow = append(bi.TargetAllow, tt+":"+tid)
		}
		if rerr := rows.Err(); rerr != nil {
			return nil, rerr
		}
	}
	return &bi, nil
}

// List 列出绑定（authType 非空时按类型过滤）。
func (r *PostgresRepository) List(schema, authType string) ([]models.SvcAuthBinding, error) {
	where := ""
	args := []interface{}{}
	if authType != "" {
		where = " WHERE b.auth_type = $1"
		args = append(args, authType)
	}

	// target_count 用子查询带出：绑定数量很少，避免前端为每张卡片再拉一次白名单。
	query := fmt.Sprintf(`SELECT b.id, b.auth_type, b.external_issuer, b.external_subject, b.user_id,
			b.is_active, b.restrict_targets, b.exclude_supervisor,
			(SELECT count(*) FROM %s.svc_auth_binding_targets t WHERE t.binding_id = b.id) AS target_count,
			b.created_at, b.updated_at
		FROM %s.svc_auth_bindings b%s ORDER BY b.created_at DESC`, schema, schema, where)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.SvcAuthBinding, 0)
	for rows.Next() {
		var b models.SvcAuthBinding
		if err := rows.Scan(&b.ID, &b.AuthType, &b.ExternalIssuer, &b.ExternalSubject, &b.UserID,
			&b.IsActive, &b.RestrictTargets, &b.ExcludeSupervisor, &b.TargetCount, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, rows.Err()
}

// GetByID 按 id 取绑定；不存在返回 (nil, nil)。
func (r *PostgresRepository) GetByID(schema, id string) (*models.SvcAuthBinding, error) {
	var b models.SvcAuthBinding
	err := db.DB.QueryRow(`SELECT id, auth_type, external_issuer, external_subject, user_id, is_active,
			restrict_targets, exclude_supervisor,
			(SELECT count(*) FROM `+schema+`.svc_auth_binding_targets t WHERE t.binding_id = `+schema+`.svc_auth_bindings.id),
			created_at, updated_at
		FROM `+schema+`.svc_auth_bindings WHERE id = $1`, id).
		Scan(&b.ID, &b.AuthType, &b.ExternalIssuer, &b.ExternalSubject, &b.UserID, &b.IsActive,
			&b.RestrictTargets, &b.ExcludeSupervisor, &b.TargetCount, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

// Create 新建绑定（id 为空时自动生成）。
func (r *PostgresRepository) Create(schema string, b *models.SvcAuthBinding) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	// 刻意不写 restrict_targets：由列默认值（true）决定，新建即受限且白名单为空 = 一个端点都调不到。
	// 如果这里显式塞 b.RestrictTargets，bool 零值 false 会让「忘填」变成「全开」，正好把安全默认破掉。
	_, err := db.DB.Exec(`INSERT INTO `+schema+`.svc_auth_bindings
		(id, auth_type, external_issuer, external_subject, user_id, is_active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6, NOW(), NOW())`,
		b.ID, b.AuthType, b.ExternalIssuer, b.ExternalSubject, b.UserID, b.IsActive)
	return err
}

// Update 更新绑定（external_subject / user_id / is_active）。
func (r *PostgresRepository) Update(schema string, b *models.SvcAuthBinding) error {
	res, err := db.DB.Exec(`UPDATE `+schema+`.svc_auth_bindings
		SET external_subject = $2, user_id = $3, is_active = $4, updated_at = NOW()
		WHERE id = $1`,
		b.ID, b.ExternalSubject, b.UserID, b.IsActive)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete 删除绑定。
func (r *PostgresRepository) Delete(schema, id string) error {
	_, err := db.DB.Exec(`DELETE FROM `+schema+`.svc_auth_bindings WHERE id = $1`, id)
	return err
}

// BindingTarget 凭证白名单里的一个端点目标（agent / flow）。
//
// 授权按**目标**而不是端点：同一个 agent 通常有 rpc 与 mcp 两条端点，
// 按端点授权会导致「只勾了 rpc，MCP 连接就全空」以及「端点收回后重开、授权失效」。
type BindingTarget struct {
	TargetType string   `json:"target_type"`
	TargetID   string   `json:"target_id"`
	Name       string   `json:"name,omitempty"`
	Protocols  []string `json:"protocols,omitempty"` // 该目标已对外开放的协议（展示用）
	// IsSupervisor 仅候选列表有意义：该目标是不是超级智能体。
	// 界面据此分组置顶并给警告 —— 授权 supervisor 端点等于对外开放租户管理能力。
	IsSupervisor bool `json:"is_supervisor,omitempty"`
}

// GetTargets 读取某绑定的端点访问范围（restrict 开关 + 排除超级智能体 + 白名单）。
func (r *PostgresRepository) GetTargets(schema, bindingID string) (bool, bool, []BindingTarget, error) {
	var restrict, excludeSupervisor bool
	if err := db.DB.QueryRow(`SELECT restrict_targets, exclude_supervisor
		FROM `+schema+`.svc_auth_bindings WHERE id = $1`, bindingID).
		Scan(&restrict, &excludeSupervisor); err != nil {
		if err == sql.ErrNoRows {
			return false, false, nil, sql.ErrNoRows
		}
		return false, false, nil, err
	}

	rows, err := db.DB.Query(`SELECT target_type, target_id
		FROM `+schema+`.svc_auth_binding_targets WHERE binding_id = $1
		ORDER BY target_type, target_id`, bindingID)
	if err != nil {
		return false, false, nil, err
	}
	defer rows.Close()

	out := make([]BindingTarget, 0, 4)
	for rows.Next() {
		var t BindingTarget
		if serr := rows.Scan(&t.TargetType, &t.TargetID); serr != nil {
			return false, false, nil, serr
		}
		out = append(out, t)
	}
	return restrict, excludeSupervisor, out, rows.Err()
}

// SetTargets 全量替换某绑定的端点访问范围（事务：改开关 + 删旧行 + 插新行）。
//
// restrict=true 时把 exclude_supervisor 落成 false：两档互斥，白名单已经精确表达范围，
// 留着一个 true 只会在下次打开对话框时让人困惑「到底哪档生效」。
func (r *PostgresRepository) SetTargets(schema, bindingID string, restrict, excludeSupervisor bool, targets []BindingTarget) error {
	if restrict {
		excludeSupervisor = false
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	res, err := tx.Exec(`UPDATE `+schema+`.svc_auth_bindings
		SET restrict_targets = $1, exclude_supervisor = $2, updated_at = NOW() WHERE id = $3`,
		restrict, excludeSupervisor, bindingID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}

	if _, err = tx.Exec(`DELETE FROM `+schema+`.svc_auth_binding_targets WHERE binding_id = $1`, bindingID); err != nil {
		return err
	}
	for _, t := range targets {
		if _, err = tx.Exec(`INSERT INTO `+schema+`.svc_auth_binding_targets (binding_id, target_type, target_id)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, bindingID, t.TargetType, t.TargetID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTargetCandidates 列出可授权的候选目标：**有对外开放端点**的 agent / flow。
//
// 只过滤「没有对外开放端点」的目标 —— 没端点的目标即使授权也调不到，列出来只会让人以为配好了。
//
// 超级智能体（is_supervisor）不排除，但带标记返回：它的端点是租户管理入口，授权等于把
// 「建 / 改 / 删 agent、改授权」开放给外部 key。所以它是**可见但需要显式勾选**的
// （界面上单独置顶成一组），不能像普通端点那样随手全选带过。
func (r *PostgresRepository) ListTargetCandidates(schema string) ([]BindingTarget, error) {
	rows, err := db.DB.Query(fmt.Sprintf(`SELECT e.target_type, e.target_id,
			COALESCE(a.name, f.name, '') AS name,
			COALESCE(bool_or(COALESCE(a.is_supervisor, false)), false) AS is_supervisor,
			COALESCE(string_agg(DISTINCT e.protocol, ',' ORDER BY e.protocol), '') AS protocols
		FROM %s.svc_endpoints e
		LEFT JOIN %s.agents a ON e.target_type = 'agent' AND a.id = e.target_id
		LEFT JOIN %s.flows f ON e.target_type = 'flow' AND f.id = e.target_id
		WHERE e.exposed = true AND e.target_id IS NOT NULL
		GROUP BY e.target_type, e.target_id, a.name, f.name
		ORDER BY name`, schema, schema, schema))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]BindingTarget, 0, 8)
	for rows.Next() {
		var t BindingTarget
		var protocols string
		if serr := rows.Scan(&t.TargetType, &t.TargetID, &t.Name, &t.IsSupervisor, &protocols); serr != nil {
			return nil, serr
		}
		if protocols != "" {
			t.Protocols = strings.Split(protocols, ",")
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
