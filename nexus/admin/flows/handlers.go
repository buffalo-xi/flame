package flows

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/models"
	"flame/nexus/shared/quota"
	agentsrepo "flame/nexus/shared/repository/agents"
	"flame/nexus/shared/repository/flows"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/secureval"
	"flame/nexus/shared/versioning"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// 全局Repository实例
var repo = repository.NewRepository()

// rawOf 取 JSONB 的原始字节（xsql.JSONB.Data 可能是 []byte / RawMessage / string）。
func rawOf(j xsql.JSONB) []byte {
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
		b, _ := json.Marshal(j.Data)
		return b
	}
}

// maskedEnvVars 出参用：type=secret 的值不回落明文，只留 has_value。
//
// 掩不住就报错，绝不退回明文 —— 退回等于这个功能不存在。
func maskedEnvVars(j xsql.JSONB) (xsql.JSONB, error) {
	masked, err := secureval.MaskEnvVars(rawOf(j))
	if err != nil {
		return xsql.JSONB{}, err
	}
	if len(masked) == 0 {
		return j, nil
	}
	return xsql.JSONB{Data: json.RawMessage(masked)}, nil
}

// validateFlowNameUnique 校验工作流名可用：字符集与 agent/skill 一致（字母数字横线，≤64），
// 且与智能体共享命名空间，不得重名。
//
// 字符集不是抄来的规矩：工作流名会变成端点名（svc_endpoints.Create 强制相等），
// 也就是 MCP 的 tool.name、agent 侧的工具名 local__<name> 与授权 ref local:rpc:<name>。
// 后两者只认 ASCII，中文名会建得出、对外开放不了、授权不上 —— 中文用途写描述。
func validateFlowNameUnique(info *auth.AuthInfo, name string) error {
	name = strings.TrimSpace(name)
	if !tools.ValidName(name) {
		return fmt.Errorf("工作流名 %q 不合法：只能用字母、数字与横线（不能以横线开头，≤64）；"+
			"这个名字会作为对外工具名 local__<name>，中文说明请写进「描述」", name)
	}
	exist, err := agentsrepo.NewRepository().ExistsName(info, name, "")
	if err != nil {
		return err
	}
	if exist {
		return fmt.Errorf("工作流名 %q 已被同租户的 agent 占用：工作流与 agent 共享命名空间"+
			"（端点名恒等于实体名，撞名会导致端点创建失败），请换一个名字", name)
	}
	return nil
}

// SetFlowStatus 切换工作流状态（1=正常 | 3=废弃，见 models.StatusXxx；不设「锁定」，线上由版本管理）。
//
// 切「废弃」时会自动收回该 flow 全部端点的对外（exposed=false，记录保留）：
// 废弃态不该再对外服务。切「正常」不动端点（端点对外与定义状态无关）。
func SetFlowStatus(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		Status int `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	switch body.Status {
	case models.StatusNormal, models.StatusDeprecated:
	default:
		xhttp.ErrJSON(c, "status must be 1(正常) / 3(废弃)；工作流不设「锁定」，线上由版本管理", nil, "")
		return
	}

	if err := repo.UpdateStatus(ai, id, body.Status); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrJSON(c, "flow instance not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 只有切「废弃」才收回端点对外：废弃态不该再对外服务。
	// 切回「正常」不收回 —— 端点对外与定义生命周期是两件事，恢复编辑不该把线上服务打掉。
	// 状态已改，收回失败不回滚，只记日志（下次切换或手工收回可恢复）。
	if body.Status == models.StatusDeprecated {
		if uerr := svcendpoints.NewRepository().RetractByTarget(ai, "flow", id); uerr != nil {
			xlog.Error("下架 flow 端点失败 id=%s: %v", id, uerr)
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id, "status": body.Status}, "")
}

// ListFlows 获取工作流实例列表
func ListFlows(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	// 解析请求参数
	var req repository.ListFlowsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request parameters", nil, "")
		return
	}

	// 查询数据
	models, total, err := repo.ListFlows(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 当前版本号：一次批量查（列表卡片要显示 vN），读失败不影响列表本身。
	// 只填 CurrentVersion；Dirty 要逐条比对定义，留给详情接口（fillVersionState）。
	curVers, _ := versioning.CurrentVersions(authInfo.(*auth.AuthInfo).Schema, "flow_versions")
	for i := range models {
		models[i].CurrentVersion = curVers[models[i].ID]
	}

	// 构建响应
	response := gin.H{
		"items": models,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// validateDiagramLabels 节点 label 必须全图唯一。
//
// label 是 config.inputs.*.source_ref / call.target / wait.fork_node_id 的可写形式
// （工具与执行期都会按 label 换成节点 id），重名会让这些引用指向错误节点，
// 所以在保存口上直接拦住 —— 画布再怎么改也存不进去。
func validateDiagramLabels(diagram json.RawMessage) error {
	raw := bytes.TrimSpace(diagram)
	if len(raw) == 0 {
		return nil
	}
	// 画布提交的是 JSON 字符串（前端先 JSON.stringify 了一次），工具写库的是对象，两种都要认
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil
		}
		raw = []byte(s)
	}

	var doc struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Data struct {
				Label string `json:"label"`
			} `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil // 形状问题不归这里管（空图 / 非对象由执行期兜底）
	}

	seen := make(map[string]string, len(doc.Nodes))
	var dups []string
	for _, n := range doc.Nodes {
		label := strings.TrimSpace(n.Data.Label)
		if label == "" {
			continue
		}
		if first, ok := seen[label]; ok {
			dups = append(dups, fmt.Sprintf("「%s」（节点 %s 与 %s）", label, first, n.ID))
			continue
		}
		seen[label] = n.ID
	}
	if len(dups) == 0 {
		return nil
	}
	return fmt.Errorf("节点名称不能重复：%s。名称可以用于 source_ref / call.target 引用，重名会指错节点，请改名后再保存",
		strings.Join(dups, "、"))
}

// GetFlow 获取单个工作流实例
func GetFlow(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	id := c.Param("id")

	s, err := repo.GetFlow(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrJSON(c, "flow instance not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 出参脱敏：env_vars 里 type=secret 的值不回明文。
	// 遮住的只是「顺手能看见」（截图 / devtools / 录屏），不是「能写流程的人」——
	// 前者造成的无意识泄漏远多于后者。
	if s != nil {
		masked, merr := maskedEnvVars(s.EnvVars)
		if merr != nil {
			xhttp.ErrJSON(c, "环境变量脱敏失败: "+merr.Error(), nil, "")
			return
		}
		s.EnvVars = masked
	}
	xhttp.OkJSON(c, "Success", s, "")
}

// CreateFlow 创建工作流实例
func CreateFlow(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	// 配额检查：租户内工作流数量上限
	schema := authInfo.(*auth.AuthInfo).Schema
	cnt, err := quota.CountFlows(schema)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := models.CheckQuota("flow", cnt, quota.Resolve(schema).MaxFlows); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 定义请求结构
	var req struct {
		Name              string          `json:"name"`
		Diagram           json.RawMessage `json:"diagram"`
		Description       string          `json:"description"`
		Arguments         json.RawMessage `json:"arguments"`
		Outputs           json.RawMessage `json:"outputs"`
		EnvVars           json.RawMessage `json:"env_vars"`
		FlowVars          json.RawMessage `json:"flow_vars"`
		Timeout           int             `json:"timeout"`
		MaxRecursionDepth int             `json:"max_recursion_depth"`
		MaxNodeCount      int             `json:"max_node_count"`
		MaxExecCount      int             `json:"max_exec_count"`
		MaxRunRecords     int             `json:"max_run_records"`
		MaxSessionRecords int             `json:"max_session_records"`
		Breakpoints       []string        `json:"breakpoints"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	// 应用工作流配置默认值并校验上限
	if err := validateFlowLimits(authInfo.(*auth.AuthInfo), &req.Timeout, &req.MaxRecursionDepth, &req.MaxNodeCount, &req.MaxExecCount, &req.MaxRunRecords, &req.MaxSessionRecords); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 校验工作流描述
	if err := validateDescription(req.Description); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 转换为Instance结构
	Instance := models.Flow{
		Name:              req.Name,
		Diagram:           xsql.JSONB{Data: req.Diagram},
		Description:       xsql.String(req.Description),
		Arguments:         xsql.JSONB{Data: req.Arguments},
		Outputs:           xsql.JSONB{Data: req.Outputs},
		EnvVars:           xsql.JSONB{Data: req.EnvVars},
		FlowVars:          xsql.JSONB{Data: req.FlowVars},
		Timeout:           xsql.Int(req.Timeout),
		MaxRecursionDepth: xsql.Int(req.MaxRecursionDepth),
		MaxNodeCount:      xsql.Int(req.MaxNodeCount),
		MaxExecCount:      xsql.Int(req.MaxExecCount),
		MaxRunRecords:     xsql.Int(req.MaxRunRecords),
		MaxSessionRecords: xsql.Int(req.MaxSessionRecords),
		Breakpoints:       req.Breakpoints,
	}

	// 名称校验：合法（字母数字横线）+ 不得与智能体重名（写库之前）
	req.Name = strings.TrimSpace(req.Name)
	if err := validateFlowNameUnique(authInfo.(*auth.AuthInfo), req.Name); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 节点 label 唯一性：重名会让 source_ref / call.target / fork_node_id 指错节点
	if err := validateDiagramLabels(req.Diagram); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 插入数据
	id, err := repo.CreateFlow(authInfo.(*auth.AuthInfo), &Instance)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 出参脱敏：secret 的值不回给前端（写库用的是上面的明文，这里只改响应体）
	maskedEnv, merr := maskedEnvVars(Instance.EnvVars)
	if merr != nil {
		xhttp.ErrJSON(c, "环境变量脱敏失败: "+merr.Error(), nil, "")
		return
	}
	Instance.EnvVars = maskedEnv

	// 返回创建的工作流实例
	data := gin.H{
		"id":                  id,
		"created_by":          authInfo.(*auth.AuthInfo).ID,
		"updated_by":          authInfo.(*auth.AuthInfo).ID,
		"created_by_name":     authInfo.(*auth.AuthInfo).Name,
		"updated_by_name":     authInfo.(*auth.AuthInfo).Name,
		"name":                Instance.Name,
		"diagram":             Instance.Diagram,
		"description":         Instance.Description,
		"arguments":           Instance.Arguments,
		"outputs":             Instance.Outputs,
		"env_vars":            Instance.EnvVars,
		"flow_vars":           Instance.FlowVars,
		"timeout":             Instance.Timeout,
		"max_recursion_depth": Instance.MaxRecursionDepth,
		"max_node_count":      Instance.MaxNodeCount,
		"max_exec_count":      Instance.MaxExecCount,
		"max_run_records":     Instance.MaxRunRecords,
		"max_session_records": Instance.MaxSessionRecords,
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateFlow 更新工作流实例
func UpdateFlow(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	id := c.Param("id")

	// 定义请求结构
	var req struct {
		Name              string          `json:"name"`
		Diagram           json.RawMessage `json:"diagram"`
		Description       string          `json:"description"`
		Arguments         json.RawMessage `json:"arguments"`
		Outputs           json.RawMessage `json:"outputs"`
		EnvVars           json.RawMessage `json:"env_vars"`
		FlowVars          json.RawMessage `json:"flow_vars"`
		Timeout           int             `json:"timeout"`
		MaxRecursionDepth int             `json:"max_recursion_depth"`
		MaxNodeCount      int             `json:"max_node_count"`
		MaxExecCount      int             `json:"max_exec_count"`
		MaxRunRecords     int             `json:"max_run_records"`
		MaxSessionRecords int             `json:"max_session_records"`
		Breakpoints       []string        `json:"breakpoints"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	// 应用工作流配置默认值并校验上限
	if err := validateFlowLimits(authInfo.(*auth.AuthInfo), &req.Timeout, &req.MaxRecursionDepth, &req.MaxNodeCount, &req.MaxExecCount, &req.MaxRunRecords, &req.MaxSessionRecords); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 校验工作流描述
	if err := validateDescription(req.Description); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 转换为Instance结构
	Instance := models.Flow{
		Name:              req.Name,
		Diagram:           xsql.JSONB{Data: req.Diagram},
		Description:       xsql.String(req.Description),
		Arguments:         xsql.JSONB{Data: req.Arguments},
		Outputs:           xsql.JSONB{Data: req.Outputs},
		EnvVars:           xsql.JSONB{Data: req.EnvVars},
		FlowVars:          xsql.JSONB{Data: req.FlowVars},
		Timeout:           xsql.Int(req.Timeout),
		MaxRecursionDepth: xsql.Int(req.MaxRecursionDepth),
		MaxNodeCount:      xsql.Int(req.MaxNodeCount),
		MaxExecCount:      xsql.Int(req.MaxExecCount),
		MaxRunRecords:     xsql.Int(req.MaxRunRecords),
		MaxSessionRecords: xsql.Int(req.MaxSessionRecords),
		Breakpoints:       req.Breakpoints,
	}

	// 名称处理：未传则沿用旧名；有变化则先校验（合法 + 不与智能体重名），再写库
	req.Name = strings.TrimSpace(req.Name)
	old, oldErr := repo.GetFlow(authInfo.(*auth.AuthInfo), id)
	if oldErr == nil && old != nil {
		if req.Name == "" {
			req.Name = old.Name
		} else if old.Name != req.Name {
			if err := validateFlowNameUnique(authInfo.(*auth.AuthInfo), req.Name); err != nil {
				xhttp.ErrJSON(c, err.Error(), nil, "")
				return
			}
		}
	} else if req.Name != "" {
		// 读不到旧行（并发删除等）也要校验：名字最终会写进 flows.name 与端点名，
		// 不能因为一次读失败就绕过字符集与重名检查。
		if err := validateFlowNameUnique(authInfo.(*auth.AuthInfo), req.Name); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
	}
	// 上面可能把空名补成了旧名，也可能来自未 trim 的入参：一律以解析后的 req.Name 落库，
	// 否则会出现「库里名字带空格、端点名却按 trim 后的值迁移」的错位。
	Instance.Name = req.Name

	// 节点 label 唯一性：重名会让 source_ref / call.target / fork_node_id 指错节点
	if err := validateDiagramLabels(req.Diagram); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// secret 留空 = 不改原值：出参不回明文，前端提交的必然是空。
	// 必须在这里把旧值补回去 —— 否则用户只改了描述、保存一次，密钥就被清空了。
	if len(req.EnvVars) > 0 && oldErr == nil && old != nil {
		merged, merr := secureval.MergeEnvVarSecrets(rawOf(old.EnvVars), req.EnvVars)
		if merr != nil {
			xhttp.ErrJSON(c, "合并环境变量失败: "+merr.Error(), nil, "")
			return
		}
		Instance.EnvVars = xsql.JSONB{Data: json.RawMessage(merged)}
	}

	// 更新数据
	err := repo.UpdateFlow(authInfo.(*auth.AuthInfo), id, &Instance)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrJSON(c, "flow instance not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 出参脱敏：secret 的值不回给前端（写库用的是上面合并后的明文）
	maskedEnv, merr := maskedEnvVars(Instance.EnvVars)
	if merr != nil {
		xhttp.ErrJSON(c, "环境变量脱敏失败: "+merr.Error(), nil, "")
		return
	}
	Instance.EnvVars = maskedEnv

	data := gin.H{
		"id":                  id,
		"created_by":          authInfo.(*auth.AuthInfo).ID,
		"updated_by":          authInfo.(*auth.AuthInfo).ID,
		"created_by_name":     authInfo.(*auth.AuthInfo).Name,
		"updated_by_name":     authInfo.(*auth.AuthInfo).Name,
		"name":                Instance.Name,
		"diagram":             Instance.Diagram,
		"description":         Instance.Description,
		"arguments":           Instance.Arguments,
		"outputs":             Instance.Outputs,
		"env_vars":            Instance.EnvVars,
		"flow_vars":           Instance.FlowVars,
		"timeout":             Instance.Timeout,
		"max_recursion_depth": Instance.MaxRecursionDepth,
		"max_node_count":      Instance.MaxNodeCount,
		"max_exec_count":      Instance.MaxExecCount,
		"max_run_records":     Instance.MaxRunRecords,
		"max_session_records": Instance.MaxSessionRecords,
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// DeleteFlow 删除工作流实例
func DeleteFlow(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	id := c.Param("id")

	// 先删除该工作流关联的所有协议端点（target_type=flow）
	if err := svcendpoints.NewRepository().DeleteByTarget(authInfo.(*auth.AuthInfo), "flow", id); err != nil {
		xhttp.ErrJSON(c, "failed to delete endpoints: "+err.Error(), nil, "")
		return
	}

	// 删除数据
	err := repo.DeleteFlow(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrJSON(c, "flow instance not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "flow instance deleted successfully", id, "")
}

// validateDescription 校验工作流描述：为空或仅含空白字符时拒绝保存
func validateDescription(description string) error {
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("工作流描述(description)不能为空")
	}
	return nil
}

// validateFlowLimits 应用默认值并校验不超过系统上限
// -1 表示不限制；0 表示使用系统默认值
func validateFlowLimits(authInfo *auth.AuthInfo, timeout, maxRecursionDepth, maxNodeCount, maxExecCount, maxRunRecords, maxSessionRecords *int) error {
	limits := flowconfig.Resolve(authInfo.Schema)

	// timeout: 超过 max 拒绝（-1 表示不限制）
	if *timeout > 0 && limits.MaxTimeout > 0 && *timeout > limits.MaxTimeout {
		return fmt.Errorf("timeout exceeds system limit: %d > %d", *timeout, limits.MaxTimeout)
	}

	// max_recursion_depth
	if *maxRecursionDepth > 0 && limits.MaxRecursionDepth > 0 && *maxRecursionDepth > limits.MaxRecursionDepth {
		return fmt.Errorf("max_recursion_depth exceeds system limit: %d > %d", *maxRecursionDepth, limits.MaxRecursionDepth)
	}

	// max_node_count
	if *maxNodeCount > 0 && limits.MaxNodeCount > 0 && *maxNodeCount > limits.MaxNodeCount {
		return fmt.Errorf("max_node_count exceeds system limit: %d > %d", *maxNodeCount, limits.MaxNodeCount)
	}

	// max_exec_count
	if *maxExecCount > 0 && limits.MaxExecCount > 0 && *maxExecCount > limits.MaxExecCount {
		return fmt.Errorf("max_exec_count exceeds system limit: %d > %d", *maxExecCount, limits.MaxExecCount)
	}

	// max_run_records：0 保持未设置（运行时由系统级兜底）；超过系统上限拒绝（-1 表示不限制）
	if *maxRunRecords > 0 && limits.MaxRunRecords > 0 && *maxRunRecords > limits.MaxRunRecords {
		return fmt.Errorf("max_run_records exceeds system limit: %d > %d", *maxRunRecords, limits.MaxRunRecords)
	}

	// max_session_records：-1/0 表示不做会话记忆（允许）；一旦设置必须 >=1；超过系统上限拒绝
	if *maxSessionRecords == 0 {
		return fmt.Errorf("max_session_records must be >= 1 when set, or -1 to disable")
	}
	if *maxSessionRecords > 0 && limits.MaxSessionRecords > 0 && *maxSessionRecords > limits.MaxSessionRecords {
		return fmt.Errorf("max_session_records exceeds system limit: %d > %d", *maxSessionRecords, limits.MaxSessionRecords)
	}

	return nil
}

// GetBreakpoints 获取工作流断点 node_id 列表
func GetBreakpoints(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	id := c.Param("id")
	list, err := repo.ListBreakpoints(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"breakpoints": list}, "")
}

// SetBreakpoint 单条增删断点（运行中即时落库，防刷新丢失）
func SetBreakpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	id := c.Param("id")
	var req struct {
		NodeID string `json:"node_id"`
		On     bool   `json:"on"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	if req.NodeID == "" {
		xhttp.ErrJSON(c, "node_id is required", nil, "")
		return
	}
	if err := repo.SetBreakpoint(authInfo.(*auth.AuthInfo), id, req.NodeID, req.On); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", nil, "")
}
