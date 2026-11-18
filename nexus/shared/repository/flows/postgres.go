package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/secureval"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository PostgreSQL实现的工作流实例存储
type PostgresRepository struct{}

// NewPostgresRepository 创建PostgreSQL存储实例
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// sealEnvVars 落库前加密 env_vars 里 type=secret 的值（见 secureval.SealEnvVars）。
//
// 只有两个写入口（CreateFlow / UpdateFlow）；版本快照与「载入版本」都是 SQL 内
// 原样搬运 jsonb，密文进密文出，不需要在 Go 里再走一遍。
//
// 不改入参：调用方（handler 出参、执行上下文）要的是明文。
func sealEnvVars(j xsql.JSONB) (xsql.JSONB, error) {
	raw := jsonbBytes(j)
	sealed, err := secureval.SealEnvVars(raw)
	if err != nil {
		return j, err
	}
	if len(sealed) == 0 {
		return j, nil
	}
	return xsql.JSONB{Data: json.RawMessage(sealed)}, nil
}

// openEnvVars 读出后把 env_vars 里的密文还原成明文。
func openEnvVars(j xsql.JSONB) (xsql.JSONB, error) {
	raw := jsonbBytes(j)
	opened, err := secureval.OpenEnvVars(raw)
	if err != nil {
		return j, err
	}
	if len(opened) == 0 {
		return j, nil
	}
	return xsql.JSONB{Data: json.RawMessage(opened)}, nil
}

// jsonbBytes 取 JSONB 的原始字节。xsql.JSONB.Data 可能是 []byte / RawMessage /
// string，也可能是 Scan 出来的 RawMessage，统一在这里归一。
func jsonbBytes(j xsql.JSONB) []byte {
	switch v := j.Data.(type) {
	case nil:
		return nil
	case []byte:
		return v
	case json.RawMessage:
		return []byte(v)
	case string:
		return []byte(v)
	default:
		b, err := json.Marshal(j.Data)
		if err != nil {
			return nil
		}
		return b
	}
}

// ListFlows 获取所有工作流实例
func (r *PostgresRepository) ListFlows(authInfo *auth.AuthInfo, req *ListFlowsRequest) ([]models.Flow, int, error) {
	// 构建查询条件
	whereClause := ""
	args := []interface{}{}
	argIndex := 1

	if req.Name != "" {
		whereClause += " WHERE name ILIKE $" + strconv.Itoa(argIndex) + ""
		args = append(args, "%"+req.Name+"%")
		argIndex++
	}

	// 计算总数
	countQuery := "SELECT COUNT(*) FROM " + authInfo.Schema + ".flows" + whereClause
	var total int
	err := db.DB.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// 处理分页
	page := req.Page
	if page < 1 {
		page = 1
	}

	// limit 语义：负数 = 取全部（管理界面用，不拼 LIMIT/OFFSET）；否则 <1 时用默认值
	all := req.Limit < 0
	limit := req.Limit
	if !all && limit < 1 {
		limit = 10
	}

	offset := (page - 1) * limit

	// 处理排序
	sortClauses := []string{}

	// 如果没有指定排序字段，默认按created_at降序
	if len(req.SortBy) == 0 {
		sortClauses = append(sortClauses, "created_at desc")
	} else {
		// 处理多列排序
		for i, sortBy := range req.SortBy {
			sortOrder := "asc"
			// 如果指定了对应列的排序顺序，使用指定的顺序
			if i < len(req.SortOrder) && (req.SortOrder[i] == "asc" || req.SortOrder[i] == "desc") {
				sortOrder = req.SortOrder[i]
			}
			sortClauses = append(sortClauses, sortBy+" "+sortOrder)
		}
	}

	// 构建排序子句
	sortClause := "ORDER BY " + strings.Join(sortClauses, ", ")

	// 构建查询语句（不查询diagram列，返回空值）
	query := "SELECT id, name, NULL as diagram, description, created_at, updated_at, arguments, outputs, NULL as env_vars, NULL as flow_vars, timeout, max_recursion_depth, max_node_count, max_exec_count, max_run_records, max_session_records, created_by, updated_by, (SELECT name FROM " + authInfo.Schema + ".users WHERE id = p.created_by) as created_by_name, (SELECT name FROM " + authInfo.Schema + ".users WHERE id = p.updated_by) as updated_by_name, endpoints, status FROM " + authInfo.Schema + ".flows p" + whereClause + " " + sortClause
	if !all {
		query += " LIMIT $" + strconv.Itoa(argIndex) + " OFFSET $" + strconv.Itoa(argIndex+1)
		args = append(args, limit, offset)
	}

	// 执行查询
	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []models.Flow
	for rows.Next() {
		var model models.Flow
		var endpointsJSON xsql.UUID
		if err := rows.Scan(&model.ID, &model.Name, &model.Diagram, &model.Description, &model.CreatedAt, &model.UpdatedAt, &model.Arguments, &model.Outputs, &model.EnvVars, &model.FlowVars, &model.Timeout, &model.MaxRecursionDepth, &model.MaxNodeCount, &model.MaxExecCount, &model.MaxRunRecords, &model.MaxSessionRecords, &model.CreatedBy, &model.UpdatedBy, &model.CreatedByName, &model.UpdatedByName, &endpointsJSON, &model.Status); err != nil {
			return nil, 0, err
		}
		if string(endpointsJSON) != "" {
			_ = json.Unmarshal([]byte(string(endpointsJSON)), &model.Endpoints)
		}
		results = append(results, model)
	}

	return results, total, nil
}

// ExistsFlowName 判断同租户下是否已存在同名工作流（agent 重名校验用）。
func (r *PostgresRepository) ExistsFlowName(authInfo *auth.AuthInfo, name string) (bool, error) {
	var exists bool
	if err := db.DB.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM `+authInfo.Schema+`.flows WHERE LOWER(name) = LOWER($1))`, name,
	).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// FindByName 按名字取一行（重名判断与「同名覆盖安装」用）。不存在返回 (nil, nil)。
//
// 先取 id 再走 GetFlow：字段映射 / endpoints 反序列化 / 版本状态只有一处，
// 覆盖安装需要的 id 与 status 都在里面。
func (r *PostgresRepository) FindByName(authInfo *auth.AuthInfo, name string) (*models.Flow, error) {
	var id string
	err := db.DB.QueryRow(
		`SELECT id FROM `+authInfo.Schema+`.flows WHERE LOWER(name) = LOWER($1)`, name,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r.GetFlow(authInfo, id)
}

// GetFlow 根据ID获取工作流实例
func (r *PostgresRepository) GetFlow(authInfo *auth.AuthInfo, id string) (*models.Flow, error) {
	var model models.Flow
	var endpointsJSON xsql.UUID
	err := db.DB.QueryRow(`SELECT id, name, diagram, description, created_at, updated_at, arguments, outputs, env_vars, flow_vars, timeout, max_recursion_depth, max_node_count, max_exec_count, max_run_records, max_session_records, created_by, updated_by, (SELECT name FROM `+authInfo.Schema+`.users WHERE id = p.created_by) as created_by_name, (SELECT name FROM `+authInfo.Schema+`.users WHERE id = p.updated_by) as updated_by_name, endpoints, status FROM `+authInfo.Schema+`.flows p WHERE id = $1`, id).Scan(&model.ID, &model.Name, &model.Diagram, &model.Description, &model.CreatedAt, &model.UpdatedAt, &model.Arguments, &model.Outputs, &model.EnvVars, &model.FlowVars, &model.Timeout, &model.MaxRecursionDepth, &model.MaxNodeCount, &model.MaxExecCount, &model.MaxRunRecords, &model.MaxSessionRecords, &model.CreatedBy, &model.UpdatedBy, &model.CreatedByName, &model.UpdatedByName, &endpointsJSON, &model.Status)
	if err != nil {
		return nil, err
	}
	if string(endpointsJSON) != "" {
		_ = json.Unmarshal([]byte(string(endpointsJSON)), &model.Endpoints)
	}
	// env_vars 里的 secret 值是密文，读出来统一在这里还原成明文：
	// 执行器、管理端出参、资产包导出拿到的都该是明文，各自处理密文迟早漏一处。
	opened, oerr := openEnvVars(model.EnvVars)
	if oerr != nil {
		return nil, fmt.Errorf("工作流 %s 的环境变量解密失败: %w", model.Name, oerr)
	}
	model.EnvVars = opened
	// 补「当前版本 + 是否有未发布改动」：编辑器要据此告诉用户
	// 「你改的草稿还没发布，线上跑的仍是 vN」。查不到版本信息不影响主流程。
	r.fillVersionState(authInfo, &model)

	return &model, nil
}

// UpdateStatus 仅更新 status（1=active 启用 | 2=disabled 停用）。
func (r *PostgresRepository) UpdateStatus(authInfo *auth.AuthInfo, id string, status int) error {
	result, err := db.DB.Exec(`UPDATE `+authInfo.Schema+`.flows SET status = $1, updated_by = $2, updated_at = NOW() WHERE id = $3`,
		status, xsql.UUID(authInfo.ID), id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CreateFlow 创建工作流实例
func (r *PostgresRepository) CreateFlow(authInfo *auth.AuthInfo, Instance *models.Flow) (string, error) {
	// 开始事务
	tx, err := db.DB.Begin()
	if err != nil {
		return "", err
	}

	// 检查 agents 表中是否存在同名记录（大小写不敏感，与 agent 侧规则一致）
	var exists bool
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+authInfo.Schema+`.agents WHERE LOWER(name) = LOWER($1))`, Instance.Name).Scan(&exists)
	if err != nil {
		tx.Rollback()
		return "", err
	}

	if exists {
		tx.Rollback()
		return "", fmt.Errorf("agent instance with name '%s' already exists", Instance.Name)
	}

	// Go 后端生成 UUID 作为主键 id
	id := uuid.NewString()
	envVars, serr := sealEnvVars(Instance.EnvVars)
	if serr != nil {
		tx.Rollback()
		return "", fmt.Errorf("加密环境变量失败: %w", serr)
	}
	_, err = tx.Exec(`INSERT INTO `+authInfo.Schema+`.flows (id, name, diagram, description, arguments, outputs, env_vars, flow_vars, timeout, max_recursion_depth, max_node_count, max_exec_count, max_run_records, max_session_records, created_by, updated_by, endpoints, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, '[]'::jsonb, NOW(), NOW())`,
		id, Instance.Name, Instance.Diagram, Instance.Description, Instance.Arguments, Instance.Outputs, envVars, Instance.FlowVars, Instance.Timeout, Instance.MaxRecursionDepth, Instance.MaxNodeCount, Instance.MaxExecCount, Instance.MaxRunRecords, Instance.MaxSessionRecords, xsql.UUID(authInfo.ID), xsql.UUID(authInfo.ID))
	if err != nil {
		tx.Rollback()
		return "", err
	}

	// 全量覆盖断点（先删后插）
	if err := replaceBreakpoints(tx, authInfo.Schema, id, Instance.Breakpoints); err != nil {
		tx.Rollback()
		return "", err
	}

	// 自动补一条未对外的 RPC 端点：流程要被别的流程按端点名调用、要出现在工具目录里，
	// 都依赖这一行；不建的话流程是一个「谁也叫不到、也没有任何提示」的孤岛。
	// 建端点不授予任何权限 —— 对外开放仍是独立动作（exposed 默认 false）。
	if err := svcendpoints.CreateManagedRPC(tx, authInfo.Schema, "flow", id, Instance.Name, string(Instance.Description), Instance.Arguments); err != nil {
		tx.Rollback()
		return "", err
	}

	// 提交事务
	if err := tx.Commit(); err != nil {
		return "", err
	}

	return id, nil
}

// UpdateFlow 更新工作流实例
func (r *PostgresRepository) UpdateFlow(authInfo *auth.AuthInfo, id string, Instance *models.Flow) error {
	// 开始事务
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	// 只有「正常」允许改定义；废弃一律拦（锁定态已取消：线上由版本管理）。
	// 判定从"有无 exposed 端点"改为读状态：端点对外开放不改状态，所以 status 是唯一真源。
	var curStatus int
	var oldName string
	err = tx.QueryRow(`SELECT status, name FROM `+authInfo.Schema+`.flows WHERE id = $1`, id).Scan(&curStatus, &oldName)
	if err != nil {
		tx.Rollback()
		return err
	}
	if !models.IsModifiable(curStatus) {
		tx.Rollback()
		return fmt.Errorf("工作流已废弃，不能保存；请先切回「编辑」状态")
	}

	// 检查 agents 表中是否存在同名记录（排除当前记录；大小写不敏感）
	var exists bool
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+authInfo.Schema+`.agents WHERE LOWER(name) = LOWER($1))`, Instance.Name).Scan(&exists)
	if err != nil {
		tx.Rollback()
		return err
	}

	if exists {
		tx.Rollback()
		return fmt.Errorf("agent instance with name '%s' already exists", Instance.Name)
	}

	// 执行更新操作
	envVars, serr := sealEnvVars(Instance.EnvVars)
	if serr != nil {
		tx.Rollback()
		return fmt.Errorf("加密环境变量失败: %w", serr)
	}
	result, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flows SET name = $1, diagram = $2, description = $3, arguments = $4, outputs = $5, env_vars = $6, flow_vars = $7, timeout = $8, max_recursion_depth = $9, max_node_count = $10, max_exec_count = $11, max_run_records = $12, max_session_records = $13, updated_by = $14, updated_at = NOW() WHERE id = $15`,
		Instance.Name, Instance.Diagram, Instance.Description, Instance.Arguments, Instance.Outputs, envVars, Instance.FlowVars, Instance.Timeout, Instance.MaxRecursionDepth, Instance.MaxNodeCount, Instance.MaxExecCount, Instance.MaxRunRecords, Instance.MaxSessionRecords, xsql.UUID(authInfo.ID), id)
	if err != nil {
		tx.Rollback()
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return err
	}

	if rowsAffected == 0 {
		tx.Rollback()
		return sql.ErrNoRows
	}

	// 同步 flow 的名称/描述/arguments 到关联的 svc_endpoints（name、description、input_schema）。
	// 限定 target_type='flow'：端点名恒等于实体名，改名即改对外名。
	_, err = tx.Exec(
		`UPDATE `+authInfo.Schema+`.svc_endpoints SET name = $1, description = $2, input_schema = $3, updated_at = NOW() WHERE target_id = $4 AND target_type = 'flow'`,
		Instance.Name, Instance.Description, Instance.Arguments, id,
	)
	if err != nil {
		tx.Rollback()
		return err
	}

	// 改名要把授权引用一起搬走：agent 授权 / skill 依赖 / 工具组里存的是
	// local:rpc:<端点名> 字符串，端点名又恒等于工作流名。只改 flows 与 svc_endpoints
	// 会让这些引用静默指向旧名 —— 权限还在表里，运行时却匹配不到，排查极其费劲。
	//
	// 为什么在 flows 仓储里动手改授权表：这条迁移必须与改名、端点名同步在同一个事务里
	// （否则会留下「流程叫新名、授权指旧名」的中间态），而 UpdateFlow 的事务就在这里。
	// 只做等值替换：整组 ref 是 local:rpc（无 member），不受影响；mcp:/rpc: 是外部连接，
	// 与工作流名无关。
	if newName := strings.TrimSpace(Instance.Name); newName != "" && newName != oldName {
		oldRef := "local:rpc:" + oldName
		newRef := "local:rpc:" + newName
		for _, table := range []string{"agent_tool_grants", "skill_tool_grants", "tool_group_items"} {
			if _, err := tx.Exec(
				`UPDATE `+authInfo.Schema+`.`+table+` SET ref = $1 WHERE ref = $2`, newRef, oldRef,
			); err != nil {
				tx.Rollback()
				return fmt.Errorf("迁移 %s 里的工作流引用失败: %w", table, err)
			}
		}

		// diagram 里的 rpc.endpoint 是同一类问题：它存的是端点名（= 流程名），
		// 不改的话其它图仍写旧名，运行时才报「RPC 端点不存在」。
		if _, err := renameDiagramRefs(tx, authInfo.Schema, id, oldName, newName); err != nil {
			tx.Rollback()
			return fmt.Errorf("迁移流程 diagram 里的端点引用失败: %w", err)
		}
	}

	// 全量覆盖断点（先删后插）
	if err := replaceBreakpoints(tx, authInfo.Schema, id, Instance.Breakpoints); err != nil {
		tx.Rollback()
		return err
	}

	// 提交事务
	return tx.Commit()
}

// renameDiagramRefs 把**别的**流程 diagram 里指向旧名的 rpc 端点引用改成新名，返回改动的流程数。
//
// 为什么必须搬：diagram 的 config.rpc.endpoint 存的是端点名（= 工作流名，见 rpc_target.go），
// 改名只同步 flows.name / svc_endpoints.name / local:rpc 授权 ref，会让旧图静默指向一个
// 不存在的端点 —— 存量的权限和端点都在，运行时才报「RPC 端点不存在」，很难往改名上想。
//
// 为什么在 Go 里解析再写回：SQL 的 replace(diagram::text, ...) 会把描述、prompt、参数默认值
// 里的同名文字一起改掉。这里只做等值匹配（config.rpc.endpoint == oldName）。
//
// 解析失败的图跳过而不是让改名整体失败：那是历史脏数据，不该阻塞用户改名。
// excludeID 是本次改名的流程自身 —— 它的 diagram 刚被用户提交的新内容覆盖，不再二次改写。
func renameDiagramRefs(tx *sql.Tx, schema, excludeID, oldName, newName string) (int, error) {
	rows, err := tx.Query(
		`SELECT id, diagram::text FROM `+schema+`.flows
		 WHERE id <> $2 AND diagram::text LIKE $1`, "%"+oldName+"%", excludeID)
	if err != nil {
		return 0, err
	}

	type hit struct{ id, raw string }
	hits := make([]hit, 0, 4)
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.raw); err != nil {
			_ = rows.Close()
			return 0, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	changed := 0
	for _, h := range hits {
		var doc map[string]interface{}
		if json.Unmarshal([]byte(h.raw), &doc) != nil {
			continue // 脏 diagram：跳过，不阻塞改名
		}
		nodes, _ := doc["nodes"].([]interface{})
		dirty := false
		for _, raw := range nodes {
			node, _ := raw.(map[string]interface{})
			data, _ := node["data"].(map[string]interface{})
			cfg, _ := data["config"].(map[string]interface{})
			rpcCfg, _ := cfg["rpc"].(map[string]interface{})
			if rpcCfg == nil {
				continue
			}
			if ep, _ := rpcCfg["endpoint"].(string); ep == oldName {
				rpcCfg["endpoint"] = newName
				dirty = true
			}
		}
		if !dirty {
			continue
		}
		out, err := json.Marshal(doc)
		if err != nil {
			continue
		}
		// updated_at 一起刷新：这是真实的定义改动，编辑器据此提示「有未发布改动」。
		// 注意：若被引用流程已废弃，这里绕过 UpdateFlowDiagram 的状态闸门是**有意**的
		// —— 引用修复必须完成，否则改名会留下必然跑不通的图；改动仍需重新发布才上线。
		if _, err := tx.Exec(
			`UPDATE `+schema+`.flows SET diagram = $1::jsonb, updated_at = NOW() WHERE id = $2`,
			string(out), h.id,
		); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// UpdateFlowDiagram 只替换 diagram（边工具用）：不碰名称/描述/限额/断点。
// 状态闸与 UpdateFlow 同口径：只有「正常」允许改（废弃拒绝）。
func (r *PostgresRepository) UpdateFlowDiagram(authInfo *auth.AuthInfo, id string, diagram xsql.JSONB) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	var curStatus int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1`, id).Scan(&curStatus); err != nil {
		tx.Rollback()
		return err
	}
	if !models.IsModifiable(curStatus) {
		tx.Rollback()
		return fmt.Errorf("工作流已废弃，不能保存；请先切回「编辑」状态")
	}

	result, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flows SET diagram = $1, updated_by = $2, updated_at = NOW() WHERE id = $3`,
		diagram, xsql.UUID(authInfo.ID), id)
	if err != nil {
		tx.Rollback()
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return err
	}
	if rowsAffected == 0 {
		tx.Rollback()
		return sql.ErrNoRows
	}

	return tx.Commit()
}

// UpdateFlowArguments 只替换 arguments（入口参数声明）：不碰名称 / 描述 / diagram / 限额 / 断点。
//
// 必须在同一事务里改 svc_endpoints.input_schema：端点对外的参数说明是 flows.arguments
// 的派生值（与 UpdateFlow 的同名同步段同口径），两处不一致时调用方会拿到过期的 schema。
// 状态闸与 UpdateFlowDiagram 同口径：只有「正常」允许改（废弃拒绝）。
func (r *PostgresRepository) UpdateFlowArguments(authInfo *auth.AuthInfo, id string, arguments xsql.JSONB) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	var curStatus int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1`, id).Scan(&curStatus); err != nil {
		tx.Rollback()
		return err
	}
	if !models.IsModifiable(curStatus) {
		tx.Rollback()
		return fmt.Errorf("工作流已废弃，不能保存；请先切回「编辑」状态")
	}

	result, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flows SET arguments = $1, updated_by = $2, updated_at = NOW() WHERE id = $3`,
		arguments, xsql.UUID(authInfo.ID), id)
	if err != nil {
		tx.Rollback()
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return err
	}
	if rowsAffected == 0 {
		tx.Rollback()
		return sql.ErrNoRows
	}

	if _, err := tx.Exec(
		`UPDATE `+authInfo.Schema+`.svc_endpoints SET input_schema = $1, updated_at = NOW() WHERE target_id = $2 AND target_type = 'flow'`,
		arguments, id,
	); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// DeleteFlow 删除工作流实例
func (r *PostgresRepository) DeleteFlow(authInfo *auth.AuthInfo, id string) error {
	// 开始事务
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	// 只有「仍对外开放」的端点才拦：对外入口可能正被租户外的调用方使用，不能随流程消失。
	// 未对外的端点（含建流程时自动补的 RPC）是实体自身的派生数据，随流程一起删 ——
	// 否则自动建之后每条流程都会因为「有端点」而永远删不掉。
	var exposedCount int
	err = tx.QueryRow(`SELECT COUNT(*) FROM `+authInfo.Schema+`.svc_endpoints WHERE target_id = $1 AND target_type = 'flow' AND exposed`, id).Scan(&exposedCount)
	if err != nil {
		tx.Rollback()
		return err
	}

	if exposedCount > 0 {
		tx.Rollback()
		return fmt.Errorf("工作流仍有对外开放的端点，请先「收回对外」再删除")
	}

	// 级联清掉派生端点（与 DeleteByTarget 同一语义，放在同一事务里）
	if _, delErr := tx.Exec(`DELETE FROM `+authInfo.Schema+`.svc_endpoints WHERE target_id = $1 AND target_type = 'flow'`, id); delErr != nil {
		tx.Rollback()
		return delErr
	}

	// 执行删除操作
	result, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.flows WHERE id = $1`, id)
	if err != nil {
		tx.Rollback()
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return err
	}

	if rowsAffected == 0 {
		tx.Rollback()
		return sql.ErrNoRows
	}

	// 提交事务
	return tx.Commit()
}

// replaceBreakpoints 在事务内全量覆盖 flow 的断点（先删后插）。
func replaceBreakpoints(tx *sql.Tx, schema string, flowId string, nodeIDs []string) error {
	if _, err := tx.Exec(`DELETE FROM `+schema+`.flow_breakpoints WHERE flow_id = $1`, flowId); err != nil {
		return err
	}
	for _, nodeID := range nodeIDs {
		if nodeID == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO `+schema+`.flow_breakpoints (flow_id, node_id, created_at) VALUES ($1, $2, NOW())`,
			flowId, nodeID,
		); err != nil {
			return err
		}
	}
	return nil
}

// ListBreakpoints 查询工作流断点 node_id 列表。
func (r *PostgresRepository) ListBreakpoints(authInfo *auth.AuthInfo, flowId string) ([]string, error) {
	rows, err := db.DB.Query(`SELECT node_id FROM `+authInfo.Schema+`.flow_breakpoints WHERE flow_id = $1`, flowId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		out = append(out, nodeID)
	}
	return out, rows.Err()
}

// SetBreakpoint 单条增删断点。
func (r *PostgresRepository) SetBreakpoint(authInfo *auth.AuthInfo, flowId string, nodeID string, on bool) error {
	if on {
		_, err := db.DB.Exec(
			`INSERT INTO `+authInfo.Schema+`.flow_breakpoints (flow_id, node_id, created_at) VALUES ($1, $2, NOW()) ON CONFLICT (flow_id, node_id) DO NOTHING`,
			flowId, nodeID,
		)
		return err
	}
	_, err := db.DB.Exec(
		`DELETE FROM `+authInfo.Schema+`.flow_breakpoints WHERE flow_id = $1 AND node_id = $2`,
		flowId, nodeID,
	)
	return err
}
