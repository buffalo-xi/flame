package llmmodels

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"flame/nexus/service/llm/gateway"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	repository "flame/nexus/shared/repository/llmmodels"
	providerRepo "flame/nexus/shared/repository/llmproviders"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// 全局Repository实例
var repo = repository.NewRepository()
var providerRepository = providerRepo.NewRepository()

// GetLLMModel 根据ID获取LLM模型
func GetLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	llmModel, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", llmModel, "")
}

// CreateLLMModel 创建LLM模型
func CreateLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	var req struct {
		Name          string     `json:"name"`
		ContextWindow xsql.Int   `json:"context_window"`
		BaseParams    xsql.JSONB `json:"base_params"`
		ProviderID    string     `json:"provider_id"`
		GatewayModel  string     `json:"gateway_model"`
		IsDefault     bool       `json:"is_default"`
		ExtraParams   xsql.JSONB `json:"extra_params"`
		Capabilities  xsql.JSONB `json:"capabilities"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	llmModel := models.LLMModel{
		Name:          req.Name,
		ContextWindow: req.ContextWindow,
		BaseParams:    req.BaseParams,
		ProviderID:    req.ProviderID,
		GatewayModel:  xsql.String(req.GatewayModel),
		IsDefault:     xsql.Bool(req.IsDefault),
		ExtraParams:   req.ExtraParams,
		Capabilities:  req.Capabilities,
	}

	id, err := repo.Create(authInfo.(*auth.AuthInfo), &llmModel)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	llmModel.ID = id
	xhttp.OkJSON(c, "Success", llmModel, "")
}

// UpdateLLMModel 更新LLM模型
func UpdateLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	var req struct {
		Name          string     `json:"name"`
		ContextWindow xsql.Int   `json:"context_window"`
		BaseParams    xsql.JSONB `json:"base_params"`
		ProviderID    string     `json:"provider_id"`
		GatewayModel  string     `json:"gateway_model"`
		IsDefault     bool       `json:"is_default"`
		ExtraParams   xsql.JSONB `json:"extra_params"`
		Capabilities  xsql.JSONB `json:"capabilities"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	// 已启用的模型：gateway_model 决定 LiteLLM 的 model_name，改动会导致路由失配，必须先禁用。
	// 只挡这一项 —— 名字 / 上下文 / 参数等改动允许，改完由「同步」推到网关。
	existing, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if string(existing.GatewayID) != "" && req.GatewayModel != string(existing.GatewayModel) {
		xhttp.ErrCode(c, "llm.model_enabled_locked", nil, "模型已启用，gateway_model 不可修改，请先禁用", "")
		return
	}

	llmModel := models.LLMModel{
		Name:          req.Name,
		ContextWindow: req.ContextWindow,
		BaseParams:    req.BaseParams,
		ProviderID:    req.ProviderID,
		GatewayModel:  xsql.String(req.GatewayModel),
		IsDefault:     xsql.Bool(req.IsDefault),
		ExtraParams:   req.ExtraParams,
		Capabilities:  req.Capabilities,
	}

	err = repo.Update(authInfo.(*auth.AuthInfo), id, &llmModel)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteLLMModel 删除LLM模型。
// 已启用（gateway_id 非空）的先到网关注销，避免 LiteLLM 里留下孤儿记录；
// 再在同一事务内解开 agent 引用并删除行。
func DeleteLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	m, err := repo.GetByID(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 1. 已启用则先网关注销（网络调用，放在事务外）。Deprovision 幂等，网关没了也算成功
	if gatewayID := string(m.GatewayID); gatewayID != "" {
		if gateway.Current == nil {
			xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
			return
		}
		if derr := gateway.Current.Deprovision(info.Schema,
			gateway.ModelInput{GatewayID: gatewayID}, gateway.ProviderInput{}); derr != nil {
			xhttp.ErrJSON(c, "Deprovision failed: "+derr.Error(), nil, "")
			return
		}
	}

	// 2. 解开 agent 引用 + 删除模型行
	if err := repo.DeleteCascade(info, id); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "LLM model deleted successfully", id, "")
}

// ListLLMModels 获取LLM模型列表
func ListLLMModels(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// 解析请求参数
	var req repository.ListLLMModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request parameters", nil, "")
		return
	}

	llmModels, total, err := repo.List(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 构建响应
	response := gin.H{
		"items": llmModels,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// ListLLMModelsByProviderID 根据ProviderID获取LLM模型列表
func ListLLMModelsByProviderID(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	providerID := c.Param("provider_id")

	// 解析请求参数
	var req repository.ListLLMModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request parameters", nil, "")
		return
	}

	llmModels, total, err := repo.ListByProviderID(authInfo.(*auth.AuthInfo), providerID, &req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 构建响应
	response := gin.H{
		"items": llmModels,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// GetDefaultLLMModel 获取默认模型
func GetDefaultLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	llmModel, err := repo.GetDefault(authInfo.(*auth.AuthInfo))
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "Default LLM model not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", llmModel, "")
}

// ListSelectableLLMModels 返回可在 LLM 节点下拉中选择的模型列表：
// provider.enabled=true 且 gateway_id 非空，DISTINCT 去重
func ListSelectableLLMModels(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	llmModels, err := repo.ListSelectable(authInfo.(*auth.AuthInfo))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	response := gin.H{
		"items": llmModels,
		"total": len(llmModels),
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// ProvisionLLMModel 启用 LLM 模型：向 LiteLLM 网关注册模型并将 gateway_id 回写 DB
func ProvisionLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	if gateway.Current == nil {
		xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
		return
	}

	// 1. 查模型
	m, err := repo.GetByID(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 已有 gateway_id 则幂等返回
	if string(m.GatewayID) != "" {
		xhttp.OkJSON(c, "already provisioned", m, "")
		return
	}

	// 2. 查 provider 构造 ProviderInput
	provider, err := providerRepository.GetByID(info, m.ProviderID)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to get LLM provider: "+err.Error(), nil, "")
		return
	}

	// 2.1 服务商必须处于启用状态 —— 否则会在网关上注册一个在下拉里选不到的模型
	if !bool(provider.Enabled) {
		xhttp.ErrCode(c, "llm.provider_disabled", nil, "服务商已停用，请先在「LLM 服务商」里启用后再启用模型", "")
		return
	}

	// 2.2 gateway_model 决定 LiteLLM 的 model_name，已启用的模型之间不允许重名。
	// 该模型此刻 gateway_id 必为空，所以这条统计天然排除了自己。
	gatewayModel := string(m.GatewayModel)
	if gatewayModel == "" {
		xhttp.ErrCode(c, "llm.gateway_model_required", nil, "网关模型名(gateway_model)不能为空，请先填写后再启用", "")
		return
	}
	dup, err := repo.CountEnabledByGatewayModel(info, gatewayModel)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to check gateway_model uniqueness: "+err.Error(), nil, "")
		return
	}
	if dup > 0 {
		xhttp.ErrCode(c, "llm.gateway_model_taken", gin.H{"name": gatewayModel}, "网关模型名 "+gatewayModel+" 已被其他启用中的模型使用，会导致网关路由歧义，请改用不同的 gateway_model", "")
		return
	}

	// 3. 构造 ModelInput / ProviderInput（锁外调 LiteLLM 注册）
	modelInput, providerInput, err := buildGatewayInputs(m, provider)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	gatewayID, err := gateway.Current.Provision(info.Schema, modelInput, providerInput)
	if err != nil {
		xhttp.ErrJSON(c, "Provision failed: "+err.Error(), nil, "")
		return
	}

	// 4. 锁行防并发：若已被别人 provision，退订自己刚注册的
	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	existing, err := repo.LockAndGetGatewayID(tx, info, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if existing != "" {
		// 已被并发请求 provision：退订自己刚注册的，返回已有 id
		tx.Rollback()
		tx = nil
		_ = gateway.Current.Deprovision(info.Schema, gateway.ModelInput{GatewayID: gatewayID}, gateway.ProviderInput{})
		xhttp.OkJSON(c, "already provisioned by concurrent request", gin.H{"gateway_id": existing}, "")
		return
	}

	if err := repo.SetGatewayIDInTx(tx, info, id, gatewayID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	tx = nil

	xhttp.OkJSON(c, "Success", gin.H{"gateway_id": gatewayID}, "")
}

// DeprovisionLLMModel 禁用 LLM 模型：从 LiteLLM 网关删除模型并条件清空 gateway_id
func DeprovisionLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	if gateway.Current == nil {
		xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
		return
	}

	// 1. 查模型
	m, err := repo.GetByID(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	gatewayID := string(m.GatewayID)
	if gatewayID == "" {
		// 幂等返回
		xhttp.OkJSON(c, "already deprovisioned", m, "")
		return
	}

	// 2. 先 LiteLLM 删除（锁外做网络调用）
	if err := gateway.Current.Deprovision(info.Schema,
		gateway.ModelInput{GatewayID: gatewayID}, gateway.ProviderInput{}); err != nil {
		xhttp.ErrJSON(c, "Deprovision failed: "+err.Error(), nil, "")
		return
	}

	// 3. 锁行 + 条件清空（仅当当前 gateway_id 仍等于 gatewayID 才置空）
	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	affected, err := repo.ClearGatewayIDInTx(tx, info, id, gatewayID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	tx = nil

	// affected == 0：说明在 LiteLLM 删除与 DB 清空之间被重新 provision，新 id 不应被清
	if affected == 0 {
		xlog.Info("DeprovisionLLMModel: gateway_id changed concurrently for model %s, skip clearing", id)
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeprovisionAllLLMModels 批量注销当前租户下所有已注册到网关的模型：
// 1. 先调用网关 DeprovisionAll 删除 LiteLLM 中 schema 前缀的所有模型（失败立即返回，不动 DB）
// 2. 成功后批量将该租户下所有 gateway_id 非空的模型置空
func DeprovisionAllLLMModels(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	if gateway.Current == nil {
		xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
		return
	}

	// 1. 从网关批量删除，返回实际删除的模型数量（某行失败立即返回，DB 保持不变以保证两端一致）
	count, err := gateway.Current.DeprovisionAll(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, "Deprovision all failed: "+err.Error(), nil, "")
		return
	}

	// 2. 批量清空 gateway_id
	if err := repo.ClearAllGatewayIDs(info); err != nil {
		xhttp.ErrJSON(c, "Failed to clear gateway ids: "+err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"count": count}, "")
}

// bpFloat 从 base_params map 中取出 float 类型参数值。
// 键不存在或类型不符时返回 (0, false)，调用方据此判断是否使用该参数。
func bpFloat(bp map[string]any, key string) (float64, bool) {
	v, ok := bp[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// ---- 网关同步 ----

// syncSkip 被跳过的模型（只同步已启用的，其余列出来让操作者知情）
type syncSkip struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}

// syncFailure 同步失败的模型（单个失败不中断整体流程）
type syncFailure struct {
	Model string `json:"model"`
	Error string `json:"error"`
}

// SyncLLMModels 把本地 LLM 模型配置逐个同步到 LiteLLM 网关。
//
// 逐条判定（判据是 gateway_id，即 LiteLLM 的 model_id UUID —— model_name 可重名，不可靠）：
//
//  1. 已启用（gateway_id 非空）但网关上查不到 → 重新注册并回写 gateway_id（created）；
//  2. 未启用（gateway_id 为空）→ 不注册，记入 skipped；
//  3. 网关上已存在且托管字段有差异 → Update 覆盖（updated），一致则 unchanged；
//  4. 网关上属于本 schema 前缀、但同步结束后没有任何本地 gateway_id 引用的记录 → 删除（deleted）；
//  5. 单条失败不中断，收集到 errors 一并返回。
//
// 删除范围严格限定在 model_name 前缀 "<当前schema>:" 之内 —— LiteLLM 是多租户共用实例，
// 不限制前缀会删掉其他租户的模型。
func SyncLLMModels(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	if gateway.Current == nil {
		xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
		return
	}

	locals, err := repo.ListAll(info)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to list llm models: "+err.Error(), nil, "")
		return
	}

	remote, err := gateway.Current.ListProvisioned(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to list gateway models: "+err.Error(), nil, "")
		return
	}
	remoteByID := make(map[string]gateway.ProvisionedModel, len(remote))
	for _, r := range remote {
		remoteByID[r.GatewayID] = r
	}

	providerCache := make(map[string]*models.LLMProvider)

	var (
		created   int
		updated   int
		unchanged int
		deleted   int
		skipped   = []syncSkip{}
		failures  = []syncFailure{}
		// keepIDs 记录同步结束后本地仍引用的网关 id，用于判定「多余」
		keepIDs = make(map[string]bool, len(locals))
	)

	for i := range locals {
		m := &locals[i]
		name := m.Name

		// 规则 2：未启用 —— 不注册、不处理，只记一笔让操作者知情
		gatewayID := string(m.GatewayID)
		if gatewayID == "" {
			skipped = append(skipped, syncSkip{Model: name, Reason: "未启用，未注册"})
			continue
		}

		// 先登记：即使后续步骤失败，也不能把它在网关上的记录当成「多余」删掉
		keepIDs[gatewayID] = true

		provider, perr := getProviderCached(providerCache, info, m.ProviderID)
		if perr != nil {
			failures = append(failures, syncFailure{Model: name, Error: "读取服务商失败: " + perr.Error()})
			continue
		}
		if provider == nil {
			failures = append(failures, syncFailure{Model: name, Error: "服务商不存在（provider_id=" + m.ProviderID + "）"})
			continue
		}

		modelInput, providerInput, ierr := buildGatewayInputs(m, provider)
		if ierr != nil {
			failures = append(failures, syncFailure{Model: name, Error: ierr.Error()})
			continue
		}

		remoteModel, onGateway := remoteByID[gatewayID]
		if !onGateway {
			// 规则 1：已启用但网关查不到 → 清掉旧 id 强制新建，成功后回写 gateway_id
			modelInput.GatewayID = ""
			newID, rerr := gateway.Current.Provision(info.Schema, modelInput, providerInput)
			if rerr != nil {
				failures = append(failures, syncFailure{Model: name, Error: "注册失败: " + rerr.Error()})
				continue
			}
			if uerr := repo.UpdateGatewayID(info, m.ID, newID); uerr != nil {
				failures = append(failures, syncFailure{Model: name, Error: "网关已注册但回写 gateway_id 失败: " + uerr.Error()})
				continue
			}
			delete(keepIDs, gatewayID)
			keepIDs[newID] = true
			created++
			continue
		}

		// 规则 3：已存在 —— 只比托管字段，有差异才覆盖
		if gatewayModelMatches(remoteModel, info.Schema, modelInput, providerInput) {
			unchanged++
			continue
		}
		modelInput.GatewayID = gatewayID
		if uerr := gateway.Current.Update(info.Schema, modelInput, providerInput); uerr != nil {
			failures = append(failures, syncFailure{Model: name, Error: "更新失败: " + uerr.Error()})
			continue
		}
		updated++
	}

	// 规则 4：清理网关多余（仅限本 schema 前缀 —— remote 已由 ListProvisioned 过滤）。
	// remote 是循环前的快照，本次新建的记录不在其中，不会被自己删掉。
	for _, r := range remote {
		if keepIDs[r.GatewayID] {
			continue
		}
		if derr := gateway.Current.Deprovision(info.Schema,
			gateway.ModelInput{GatewayID: r.GatewayID}, gateway.ProviderInput{}); derr != nil {
			failures = append(failures, syncFailure{Model: r.ModelName, Error: "删除多余记录失败: " + derr.Error()})
			continue
		}
		deleted++
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"created":   created,
		"updated":   updated,
		"unchanged": unchanged,
		"deleted":   deleted,
		"skipped":   skipped,
		"errors":    failures,
	}, "")
}

// getProviderCached 按 provider_id 取服务商（nil 表示不存在）。
// 同步会遍历全部模型，同一 provider 只查一次库。
func getProviderCached(cache map[string]*models.LLMProvider, info *auth.AuthInfo, providerID string) (*models.LLMProvider, error) {
	if p, ok := cache[providerID]; ok {
		return p, nil
	}
	p, err := providerRepository.GetByID(info, providerID)
	if err != nil {
		if err == sql.ErrNoRows {
			cache[providerID] = nil
			return nil, nil
		}
		return nil, err
	}
	cache[providerID] = p
	return p, nil
}

// buildGatewayInputs 把 DB 的模型 + 服务商转成网关入参。
// Provision / Update / 同步三处共用，保证写进网关的内容完全一致。
//
// 注意 GatewayModel 的语义：它是 chat 请求里的 model 字段，必须是 "<schema>:<gateway_model>"。
// 本函数只做 DB → 入参的映射，前缀由调用方补齐。
func buildGatewayInputs(m *models.LLMModel, provider *models.LLMProvider) (gateway.ModelInput, gateway.ProviderInput, error) {
	// 网关模型名必须用 DB 的 gateway_model，不允许为空
	gatewayModel := string(m.GatewayModel)
	if gatewayModel == "" {
		return gateway.ModelInput{}, gateway.ProviderInput{}, fmt.Errorf("网关模型名(gateway_model)不能为空，请先填写后再启用")
	}

	var extraParams map[string]any
	if !m.ExtraParams.IsNil() {
		if err := m.ExtraParams.Unmarshal(&extraParams); err != nil {
			return gateway.ModelInput{}, gateway.ProviderInput{}, fmt.Errorf("Failed to parse extra_params: %w", err)
		}
	}

	// 从 base_params 解包生成参数（键存在=使用，键不存在=未使用）
	var bp map[string]any
	if !m.BaseParams.IsNil() {
		if err := m.BaseParams.Unmarshal(&bp); err != nil {
			return gateway.ModelInput{}, gateway.ProviderInput{}, fmt.Errorf("Failed to parse base_params: %w", err)
		}
	}
	var maxTokens int
	if v, ok := bpFloat(bp, "max_tokens"); ok {
		maxTokens = int(v)
	}
	var temperature float64
	if v, ok := bpFloat(bp, "temperature"); ok {
		temperature = v
	}
	var topP float64
	if v, ok := bpFloat(bp, "top_p"); ok {
		topP = v
	}

	// 服务商的额外配置原样透传给 LiteLLM，用于 api_version（Azure）、
	// vertex_project / vertex_location / vertex_credentials（Vertex AI）、timeout / max_retries 等
	var providerExtra map[string]any
	if !provider.ExtraConfig.IsNil() {
		if err := provider.ExtraConfig.Unmarshal(&providerExtra); err != nil {
			return gateway.ModelInput{}, gateway.ProviderInput{}, fmt.Errorf("Failed to parse provider extra_config: %w", err)
		}
	}

	return gateway.ModelInput{
		ID:            m.ID,
		Name:          m.Name,
		GatewayModel:  gatewayModel,
		GatewayID:     string(m.GatewayID),
		ContextWindow: int(m.ContextWindow),
		MaxTokens:     maxTokens,
		Temperature:   temperature,
		TopP:          topP,
		ExtraParams:   extraParams,
	}, gateway.ProviderInput{
		ID:          provider.ID,
		Name:        provider.Name,
		URL:         string(provider.URL),
		APIKey:      string(provider.APIKey),
		Protocol:    string(provider.Protocol),
		ExtraConfig: providerExtra,
	}, nil
}

// gatewayModelMatches 判断网关上已有记录是否与本地配置一致。
// 只比对「我们托管写入」的字段：model_name、litellm_params.model/api_base/api_key、
// provider extra_config 展开到 litellm_params 的键、
// model_info.context_window/max_tokens + 模型 extra_params 展开的键。
// LiteLLM 会自行补充 tpm / rpm / use_in_pass_through 等键，全量比 JSON 会把它们误当差异，
// 导致每次同步都无谓地 Update，因此只比托管子集。
func gatewayModelMatches(r gateway.ProvisionedModel, schema string, model gateway.ModelInput, provider gateway.ProviderInput) bool {
	if r.ModelName != schema+":"+model.GatewayModel {
		return false
	}

	// litellm_params.model 必须是 "<protocol>/<model.Name>"
	if want := provider.Protocol + "/" + model.Name; r.LitellmParams["model"] != want {
		return false
	}
	// api_base 与 provider.URL 对齐
	if provider.URL != "" {
		if got, _ := r.LitellmParams["api_base"].(string); got != provider.URL {
			return false
		}
	}
	// api_key 对齐。网关若以 os.environ/ 引用或返回打码值（含 *）则无法直接比对，跳过，
	// 避免把每次同步都变成一次覆盖写。
	if provider.APIKey != "" {
		if got, _ := r.LitellmParams["api_key"].(string); got != "" &&
			!strings.HasPrefix(got, "os.environ/") && !strings.Contains(got, "*") {
			if got != provider.APIKey {
				return false
			}
		}
	}

	// provider extra_config 是逐键并进 litellm_params 的，只比对我们写入的键。
	// 漏掉这里的话，改了 extra_config（如 api_version）同步不会判为差异，也就不会推送。
	for k, want := range provider.ExtraConfig {
		if !jsonValueEqual(r.LitellmParams[k], want) {
			return false
		}
	}

	// model_info：未使用的参数按 0 处理（buildModelInfoMap 只在 >0 时写入该键）
	if !infoNumEqual(r.ModelInfo, "context_window", float64(model.ContextWindow)) {
		return false
	}
	if !infoNumEqual(r.ModelInfo, "max_tokens", float64(model.MaxTokens)) {
		return false
	}
	// extra_params 是逐键合并进 model_info 的，只比对我们写入的键
	for k, want := range model.ExtraParams {
		if !jsonValueEqual(r.ModelInfo[k], want) {
			return false
		}
	}
	return true
}

// infoNumEqual 比较 model_info 中的数值键：键缺失时，期望值为 0 视为一致
func infoNumEqual(info map[string]any, key string, want float64) bool {
	v, ok := info[key]
	if !ok || v == nil {
		return want == 0
	}
	f, ok := toFloat(v)
	if !ok {
		return false
	}
	return f == want
}

// jsonValueEqual 比较两个来自 JSON 的值；数字统一按 float64 比，避免 int/float 类型差异误判
func jsonValueEqual(a, b any) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		return af == bf
	}
	return reflect.DeepEqual(a, b)
}

// toFloat 把 JSON 反序列化出的数值统一转成 float64
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
