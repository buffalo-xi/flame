package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// ErrNoReleasedVersion 实体没有正式版本（version 表里没有 is_current）。
//
// 正式入口（MCP / RPC / 端点 / flow 之间的直调）遇到它必须拒绝，
// 不能悄悄退回草稿 —— 那样「正式运行用正式版本」就没有意义了。
// 调试入口（编辑页调试器 / run_flow 试跑）读的是草稿，不经过这里。
var ErrNoReleasedVersion = errors.New("尚未发布正式版本")

// ErrCannotDeleteCurrentVersion 当前版本不能删除。
//
// 正式入口（MCP / RPC / 端点 / flow 之间的直调）跑的就是 is_current 那一行，
// 删掉等于把线上定义抽走，所有正式调用会一起变成「尚未发布正式版本」。
// 要删就先「切到线上」把指针切到别的版本 —— 那是一个动作，删除是另一个动作。
var ErrCannotDeleteCurrentVersion = errors.New("当前版本不能删除，请先把线上切到其它版本")

// FlowVersion 一条工作流版本记录（flow_versions）。
//
// 列表接口不查 diagram（可能几十 KB）；需要整份定义时走 GetCurrentFlowVersion。
type FlowVersion struct {
	FlowID        string      `json:"flow_id"`
	Version       int         `json:"version"`
	Name          string      `json:"name"`
	Description   xsql.String `json:"description"`
	IsCurrent     bool        `json:"is_current"`
	Note          string      `json:"note"`
	CreatedBy     xsql.UUID   `json:"created_by"` // uuid 列：用 String 扫会报 "无法将 []uint8 转换为 string"
	CreatedByName xsql.String `json:"created_by_name"`
	CreatedAt     xsql.Time   `json:"created_at"`
}

// ListFlowVersions 列出某工作流的全部版本（新到旧）。
func (r *PostgresRepository) ListFlowVersions(authInfo *auth.AuthInfo, flowID string) ([]FlowVersion, error) {
	rows, err := db.DB.Query(`SELECT v.flow_id, v.version, v.name, v.description, v.is_current,
			v.note, v.created_by, COALESCE(u.name, ''), v.created_at
		FROM `+authInfo.Schema+`.flow_versions v
		LEFT JOIN `+authInfo.Schema+`.users u ON u.id = v.created_by
		WHERE v.flow_id = $1
		ORDER BY v.version DESC`, flowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FlowVersion
	for rows.Next() {
		var v FlowVersion
		if err := rows.Scan(&v.FlowID, &v.Version, &v.Name, &v.Description, &v.IsCurrent,
			&v.Note, &v.CreatedBy, &v.CreatedByName, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReleaseFlowVersion 把当前工作副本（flows 那一行）冻结成一个新版本并设为当前。
//
// 版本号在行锁内取 max+1：两个并发发布会排队，不会算出同一个号撞主键。
// 历史版本只把 is_current 置 false，内容不动。
func (r *PostgresRepository) ReleaseFlowVersion(authInfo *auth.AuthInfo, flowID, note string) (*FlowVersion, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	version, createdAt, err := releaseFlowVersionTx(tx, authInfo.Schema, flowID, note, authInfo.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.flowVersionMeta(authInfo, flowID, version, createdAt, note), nil
}

// RollbackFlowVersion 把「正在使用的版本」切回某个历史版本 —— 纯指针操作，不产生新版本。
//
// 与 ApplyFlowVersionToDraft 是两个动作，对应两种意图：
//   - 回滚：线上换回旧的那份产物，画布（草稿）不动；
//   - 载入：画布换成旧版本内容继续改，线上不动。
//
// 合成一个动作会有两种代价：回滚时顺手覆盖别人正在写的草稿，或者每次回滚都多发一版，
// 让历史堆满内容相同的副本（也就回答不了「同样的内容为什么有 v1 和 v3」）。
func (r *PostgresRepository) RollbackFlowVersion(authInfo *auth.AuthInfo, flowID string, target int) (*FlowVersion, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// 锁实体行：与发布串行，避免「发布到一半指针被切走」
	var status int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&status); err != nil {
		return nil, err
	}
	if !models.IsModifiable(status) {
		return nil, fmt.Errorf("该工作流已废弃，不能切换版本")
	}

	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND version = $2)`, flowID, target).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("version v%d: %w", target, sql.ErrNoRows)
	}

	if _, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flow_versions SET is_current = false
		WHERE flow_id = $1 AND is_current`, flowID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flow_versions SET is_current = true
		WHERE flow_id = $1 AND version = $2`, flowID, target); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.getFlowVersion(authInfo, flowID, target)
}

// ApplyFlowVersionToDraft 把某个版本的内容写回工作副本（画布），**不发布**。
//
// 用途是「基于旧版本继续改」：写完就是「有改动」状态，需要再点发布才影响线上。
// 线上当前版本一个字节都不动 —— 这正是「载入」与「回滚」分开的理由。
func (r *PostgresRepository) ApplyFlowVersionToDraft(authInfo *auth.AuthInfo, flowID string, target int) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&status); err != nil {
		return err
	}
	if !models.IsModifiable(status) {
		return fmt.Errorf("该工作流已废弃，不能载入版本")
	}

	res, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flows f
		SET description = v.description, diagram = v.diagram, arguments = v.arguments,
		    outputs = v.outputs, env_vars = v.env_vars, flow_vars = v.flow_vars,
		    timeout = v.timeout, max_recursion_depth = v.max_recursion_depth,
		    max_node_count = v.max_node_count, max_exec_count = v.max_exec_count,
		    max_run_records = v.max_run_records, max_session_records = v.max_session_records,
		    updated_at = now(), updated_by = NULLIF($3, '')::uuid
		FROM `+authInfo.Schema+`.flow_versions v
		WHERE f.id = $1 AND v.flow_id = $1 AND v.version = $2`,
		flowID, target, authInfo.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("version v%d: %w", target, sql.ErrNoRows)
	}
	return tx.Commit()
}

// DeleteFlowVersion 删掉一条历史版本：只清历史行，不进线上、不动画布。
//
// 当前版本一律拒绝（ErrCannotDeleteCurrentVersion）：它是正式调用的定义来源，
// 也是「线上正在跑什么」的唯一记录（flow_runs 不存版本号）。想停服是另一个动作，
// 不该藏在「删除历史」里。
//
// 已归档的 flow 也拒绝 —— 与发布 / 回滚 / 载入同一条规矩。
//
// 版本号不复用：删掉 v2 之后下次发布仍是 MAX(version)+1，不会再发出一个 v2。
// 复用号码会让「回滚到 v2」指向的东西随删除漂移，历史说明也就对不上号了。
func (r *PostgresRepository) DeleteFlowVersion(authInfo *auth.AuthInfo, flowID string, target int) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// 先锁实体行：发布与回滚都会切 is_current，串行之后这里读到的才是最终归属
	var status int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&status); err != nil {
		return err
	}
	if !models.IsModifiable(status) {
		return fmt.Errorf("该工作流已废弃，不能删除版本")
	}

	var isCurrent bool
	err = tx.QueryRow(`SELECT is_current FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND version = $2`, flowID, target).Scan(&isCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("version v%d: %w", target, sql.ErrNoRows)
	}
	if err != nil {
		return err
	}
	if isCurrent {
		// 只剩这一条时不说「先切到别的版本」—— 没有别的版本可切，用户会卡死
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM `+authInfo.Schema+`.flow_versions
			WHERE flow_id = $1`, flowID).Scan(&total); err != nil {
			return err
		}
		if total <= 1 {
			return fmt.Errorf("v%d 是唯一的版本，不能删除；如需清理历史请先发布一个新版本", target)
		}
		return ErrCannotDeleteCurrentVersion
	}

	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND version = $2`, flowID, target); err != nil {
		return err
	}
	return tx.Commit()
}

// getFlowVersion 读一条版本记录（回滚后返回给前端刷新列表用）。
func (r *PostgresRepository) getFlowVersion(authInfo *auth.AuthInfo, flowID string, version int) (*FlowVersion, error) {
	var v FlowVersion
	err := db.DB.QueryRow(`SELECT v.flow_id, v.version, v.name, v.description, v.is_current,
			v.note, v.created_by, COALESCE(u.name, ''), v.created_at
		FROM `+authInfo.Schema+`.flow_versions v
		LEFT JOIN `+authInfo.Schema+`.users u ON u.id = v.created_by
		WHERE v.flow_id = $1 AND v.version = $2`, flowID, version).
		Scan(&v.FlowID, &v.Version, &v.Name, &v.Description, &v.IsCurrent,
			&v.Note, &v.CreatedBy, &v.CreatedByName, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetCurrentFlowVersion 取「正式版本」的定义，供正式入口执行。
//
// 返回的 models.Flow 是混合体：定义列（diagram / 变量 / 限额 / 名字 / 描述）来自
// is_current 那一行，身份与生命周期（id / status / endpoints / 时间戳）来自实体行 ——
// 运行期判可运行性必须看实体状态，快照里没有 status。
// 没有正式版本时返回 ErrNoReleasedVersion，调用方应当拒绝执行，不要退回草稿。
func (r *PostgresRepository) GetCurrentFlowVersion(authInfo *auth.AuthInfo, id string) (*models.Flow, error) {
	base, err := r.GetFlow(authInfo, id)
	if err != nil {
		return nil, err
	}

	var name, note string
	var desc xsql.String
	var diagram xsql.JSONB
	err = db.DB.QueryRow(`SELECT name, description, diagram, note
		FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND is_current`, id).Scan(&name, &desc, &diagram, &note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoReleasedVersion
	}
	if err != nil {
		return nil, err
	}

	base.Name = name
	base.Description = desc
	base.Diagram = diagram
	return base, nil
}

// flowVersionMeta 发布成功后补齐返回体里的展示字段（名字 / 描述 / 发布人姓名）。
func (r *PostgresRepository) flowVersionMeta(authInfo *auth.AuthInfo, flowID string, version int, createdAt xsql.Time, note string) *FlowVersion {
	v := &FlowVersion{
		FlowID:        flowID,
		Version:       version,
		IsCurrent:     true,
		Note:          note,
		CreatedBy:     xsql.UUID(authInfo.ID),
		CreatedByName: xsql.String(authInfo.Name),
		CreatedAt:     createdAt,
	}
	if err := db.DB.QueryRow(`SELECT name, description FROM `+authInfo.Schema+`.flows WHERE id = $1`, flowID).
		Scan(&v.Name, &v.Description); err != nil {
		return v // 补不到展示字段不影响发布结果本身
	}
	return v
}

// fillVersionState 给查询结果补上「当前版本号 + 是否有未发布改动」（两个非 DB 列）。
//
// 编辑器必须能区分三种状态，否则用户改完画布会以为线上已经生效：
//   - CurrentVersion = 0    尚未发布（正式调用会直接拒绝）
//   - Dirty = false         工作副本与当前版本一致
//   - Dirty = true          已保存的修改还没发布，线上仍跑旧版本
//
// 比对的是「定义列」：description / diagram / 变量 / 各项限额。
// 名字不比 —— 改名会改掉对外工具名（local__<name>），但那是实体的身份，
// 发布一个同名同内容的版本并不能「发布」一次改名，标成脏只会误导。
func (r *PostgresRepository) fillVersionState(authInfo *auth.AuthInfo, f *models.Flow) {
	if f == nil || f.ID == "" {
		return
	}
	var version int
	var dirty bool
	// jsonb 列必须归一化：编辑器保存时把空变量写成 '{}'，而发布快照里可能是 NULL，
	// 直接 IS DISTINCT FROM 会把「什么都没改」判成脏，提示立刻变成假的。
	err := db.DB.QueryRow(`SELECT v.version,
			(COALESCE(f.description, '')      IS DISTINCT FROM COALESCE(v.description, '')
			 OR COALESCE(f.diagram,   '{}'::jsonb) IS DISTINCT FROM COALESCE(v.diagram,   '{}'::jsonb)
			 OR COALESCE(f.arguments, '{}'::jsonb) IS DISTINCT FROM COALESCE(v.arguments, '{}'::jsonb)
			 OR COALESCE(f.outputs,   '{}'::jsonb) IS DISTINCT FROM COALESCE(v.outputs,   '{}'::jsonb)
			 OR COALESCE(f.env_vars,  '{}'::jsonb) IS DISTINCT FROM COALESCE(v.env_vars,  '{}'::jsonb)
			 OR COALESCE(f.flow_vars, '{}'::jsonb) IS DISTINCT FROM COALESCE(v.flow_vars, '{}'::jsonb)
			 OR f.timeout          IS DISTINCT FROM v.timeout
			 OR f.max_recursion_depth IS DISTINCT FROM v.max_recursion_depth
			 OR f.max_node_count   IS DISTINCT FROM v.max_node_count
			 OR f.max_exec_count   IS DISTINCT FROM v.max_exec_count
			 OR f.max_run_records  IS DISTINCT FROM v.max_run_records
			 OR f.max_session_records IS DISTINCT FROM v.max_session_records)
		FROM `+authInfo.Schema+`.flows f
		JOIN `+authInfo.Schema+`.flow_versions v
		  ON v.flow_id = f.id AND v.is_current
		WHERE f.id = $1`, f.ID).Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		f.CurrentVersion = 0
		f.Dirty = false
		return
	}
	if err != nil {
		// 版本信息拿不到不该让整个详情页失败：退回「未知」，前端显示未发布
		return
	}
	f.CurrentVersion = version
	f.Dirty = dirty
}

// releaseFlowVersionTx 在事务内冻结当前工作副本：
// 行锁 → 取下一版本号 → 旧版取消当前 → 插入新版。
func releaseFlowVersionTx(tx *sql.Tx, schema, flowID, note, userID string) (int, xsql.Time, error) {
	// 行锁：并发发布会在这里排队，保证 max(version)+1 不会撞主键
	var status int
	if err := tx.QueryRow(`SELECT status FROM `+schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&status); err != nil {
		return 0, xsql.Time{}, err
	}
	if !models.IsModifiable(status) {
		return 0, xsql.Time{}, fmt.Errorf("该工作流已废弃，不能发布版本")
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM `+schema+`.flow_versions
		WHERE flow_id = $1`, flowID).Scan(&next); err != nil {
		return 0, xsql.Time{}, err
	}

	if _, err := tx.Exec(`UPDATE `+schema+`.flow_versions SET is_current = false
		WHERE flow_id = $1 AND is_current`, flowID); err != nil {
		return 0, xsql.Time{}, err
	}

	var createdAt xsql.Time
	err := tx.QueryRow(`INSERT INTO `+schema+`.flow_versions
		(flow_id, version, name, description, diagram, arguments, outputs, env_vars, flow_vars,
		 timeout, max_recursion_depth, max_node_count, max_exec_count, max_run_records,
		 max_session_records, is_current, note, created_by)
		SELECT f.id, $2, f.name, f.description, f.diagram, f.arguments, f.outputs, f.env_vars,
		       f.flow_vars, f.timeout, f.max_recursion_depth, f.max_node_count, f.max_exec_count,
		       f.max_run_records, f.max_session_records, true, $3, NULLIF($4, '')::uuid
		  FROM `+schema+`.flows f
		 WHERE f.id = $1
		RETURNING created_at`, flowID, next, note, userID).Scan(&createdAt)
	if err != nil {
		return 0, xsql.Time{}, err
	}
	return next, createdAt, nil
}
