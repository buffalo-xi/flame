package litellm

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"flame/nexus/service/llm/gateway"

	_ "github.com/lib/pq"
)

// ProxyGateway LiteLLM Proxy 网关实现
type ProxyGateway struct {
	URL       string
	MasterKey string
	client    *http.Client
	// db 连接 LiteLLM 自身的 Postgres 库（yaml 的 litellm.database），用于直接查注册表
	// dbMu 保护 db 指针的并发读写（后台重连 goroutine 可能在替换连接）
	// dbConnected 为 atomic，标记库连接状态，独立读取无需加锁
	db          *sql.DB
	dbConnected atomic.Bool
	dbMu        sync.RWMutex
}

// New 创建 LiteLLM Proxy 网关实例。
// db 可为 nil：此时表示 LiteLLM 库未连接（dbConnected=false），由调用方在重连 goroutine 中
// 通过 OpenDatabase 建连后调用 SetDB 注入连接（避免阻塞初始化）。
func New(url, masterKey string, db *sql.DB) *ProxyGateway {
	g := &ProxyGateway{
		URL:       strings.TrimRight(url, "/"),
		MasterKey: masterKey,
		client:    &http.Client{},
		db:        db,
	}
	if db != nil {
		g.dbConnected.Store(true)
	}
	return g
}

// IsConnected 返回 LiteLLM 库连接状态
func (g *ProxyGateway) IsConnected() bool { return g.dbConnected.Load() }

// SetDB 替换 LiteLLM 库连接并标记已连接（关闭旧连接）。
// 由调用方（main 的重连 goroutine）在成功 OpenDatabase 后调用。
func (g *ProxyGateway) SetDB(db *sql.DB) {
	g.dbMu.Lock()
	defer g.dbMu.Unlock()
	if g.db != nil {
		g.db.Close()
	}
	g.db = db
	g.dbConnected.Store(true)
}

// Ping 检查 LiteLLM 库连接并将结果同步到 dbConnected：
// db 为 nil 或 Ping 失败则置 false，成功则置 true。
func (g *ProxyGateway) Ping() error {
	g.dbMu.RLock()
	db := g.db
	g.dbMu.RUnlock()

	if db == nil {
		g.dbConnected.Store(false)
		return fmt.Errorf("litellm database not connected")
	}
	if err := db.Ping(); err != nil {
		g.dbConnected.Store(false)
		return err
	}
	g.dbConnected.Store(true)
	return nil
}

// liteLLMModelTable 是 LiteLLM 存储已注册模型的表名。
// 表名在 Postgres 中实际是大写混合（LiteLLM_ProxyModelTable），必须用双引号包裹，
// 否则未加引号的标识符会被 Postgres 折叠成小写而找不到该关系。
const liteLLMModelTable = `"LiteLLM_ProxyModelTable"`

// queryLiteLLMTable 按 model_id（UUID）查 LiteLLM 自身的注册表，判断模型是否已注册
func (g *ProxyGateway) queryLiteLLMTable(modelID string) (bool, error) {
	g.dbMu.RLock()
	db := g.db
	g.dbMu.RUnlock()
	if db == nil {
		return false, fmt.Errorf("litellm database connection not initialized")
	}
	var exists bool
	query := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE model_id = $1)", liteLLMModelTable)
	if err := db.QueryRow(query, modelID).Scan(&exists); err != nil {
		return false, fmt.Errorf("query litellm model table: %w", err)
	}
	return exists, nil
}

// ---- LiteLLM API 请求/响应结构 ----

// registerModelParams POST /model/new 请求体
type registerModelParams struct {
	ModelName     string         `json:"model_name"`
	LitellmParams litellmParams  `json:"litellm_params"`
	ModelInfo     map[string]any `json:"model_info,omitempty"`
}

type litellmParams struct {
	Model      string         `json:"model"`
	APIKey     string         `json:"api_key"`
	APIBase    string         `json:"api_base,omitempty"`
	APIVersion string         `json:"api_version,omitempty"`
	Extra      map[string]any `json:"-"`
}

// registerModelResponse POST /model/new 响应（LiteLLM_ProxyModelTable）
// 字段 model_id 是 LiteLLM 生成的 UUID，作为 gatewayID 使用
type registerModelResponse struct {
	ModelID       string         `json:"model_id"`
	ModelName     string         `json:"model_name"`
	LitellmParams map[string]any `json:"litellm_params"`
	ModelInfo     map[string]any `json:"model_info"`
}

// modelInfoItem GET /model/info 返回的单个模型
// model_info.id 即 model_id（UUID）
type modelInfoItem struct {
	ModelName     string         `json:"model_name"`
	ModelInfo     map[string]any `json:"model_info"`
	LitellmParams map[string]any `json:"litellm_params"`
}

// modelInfoResponse GET /model/info 响应
type modelInfoResponse struct {
	Data []modelInfoItem `json:"data"`
}

// chatChoice POST /chat/completions 非流式响应中的 choice
type chatChoice struct {
	Index   int `json:"index"`
	Message struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content,omitempty"`
		ToolCalls        []chatToolCall  `json:"tool_calls,omitempty"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

type chatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

// ---- chat/completions SSE 流式响应 ----

type streamDelta struct {
	Content          string         `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
}

// ---- 接口实现 ----

// Provision 检查模型是否已在网关注册，未注册则注册；返回 gatewayID（LiteLLM 的 model_id UUID）
// 判断权威来源为 model.GatewayID（DB 中存的 UUID），直接查 LiteLLM 自身的注册表表：
//   - 查到：说明已注册好，直接返回该 id
//   - 查不到（含 GatewayID 为空、首次启用）：重新注册（覆盖原 gateway_id）
//
// 注：不再按 model_name 查重复用，因为 model_name 可重名，按名字复用会误命中/漏命中，
// 不可靠。唯一权威键是 gatewayID（UUID）。
func (g *ProxyGateway) Provision(schema string, model gateway.ModelInput, provider gateway.ProviderInput) (string, error) {
	// 以 DB 中存的 gateway_id（UUID）为权威依据，直接查 LiteLLM 注册表
	if model.GatewayID != "" {
		exists, err := g.queryLiteLLMTable(model.GatewayID)
		if err != nil {
			return "", fmt.Errorf("check gateway_id in litellm table: %w", err)
		}
		if exists {
			// 已注册好，直接返回，省去一次网络注册调用
			return model.GatewayID, nil
		}
		// DB 有 id 但网关已丢失，fallthrough 重新注册（覆盖原 gateway_id）
	}

	// 注册模型，返回 LiteLLM 生成的 model_id（UUID）
	gatewayID, err := g.registerModel(schema, model, provider)
	if err != nil {
		return "", fmt.Errorf("register model: %w", err)
	}
	return gatewayID, nil
}

// Deprovision 从网关删除模型，删除依据是 model.GatewayID（LiteLLM 的 model_id UUID）。
// 幂等：网关上本来就没有这条记录时直接返回成功 —— 否则「禁用」会因网关删不掉而永远
// 清空不了本地 gateway_id，模型会卡在「已启用」状态出不来。
func (g *ProxyGateway) Deprovision(schema string, model gateway.ModelInput, provider gateway.ProviderInput) error {
	if model.GatewayID == "" {
		return fmt.Errorf("model.GatewayID is empty, cannot deprovision")
	}
	exists, err := g.modelExists(model.GatewayID)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return g.deleteModel(model.GatewayID)
}

// modelExists 判断 gateway_id 是否仍在网关注册表中。
// 优先查 LiteLLM 自身注册表（一次查询），库未连接时退回 GET /model/info 比对 model_info.id。
func (g *ProxyGateway) modelExists(modelID string) (bool, error) {
	if modelID == "" {
		return false, nil
	}
	g.dbMu.RLock()
	hasDB := g.db != nil
	g.dbMu.RUnlock()
	if hasDB {
		return g.queryLiteLLMTable(modelID)
	}

	items, err := g.listModels()
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if id, _ := item.ModelInfo["id"].(string); id == modelID {
			return true, nil
		}
	}
	return false, nil
}

// DeprovisionAll 禁用（删除）某 schema 下所有已注册到 LiteLLM 的模型。
// 优先查 LiteLLM 自身的注册表（一次查询拿全），库未连接时退回 GET /model/info。
// 逐行调用 deleteModel 从网关删除，某行失败立即返回错误。
func (g *ProxyGateway) DeprovisionAll(schema string) (int, error) {
	if schema == "" {
		return 0, fmt.Errorf("schema is empty")
	}

	var modelIDs []string

	g.dbMu.RLock()
	db := g.db
	g.dbMu.RUnlock()

	if db != nil {
		// 用 starts_with 而不是 LIKE 'schema:%' —— LIKE 里 "_" 是任意单字符通配，
		// 而 schema 允许含下划线，用 LIKE 有误删其他租户模型的风险。
		query := fmt.Sprintf("SELECT model_id FROM %s WHERE starts_with(model_name, $1)", liteLLMModelTable)
		rows, err := db.Query(query, schema+":")
		if err != nil {
			return 0, fmt.Errorf("query litellm model table by prefix: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var modelID string
			if err := rows.Scan(&modelID); err != nil {
				return 0, fmt.Errorf("scan litellm model row: %w", err)
			}
			modelIDs = append(modelIDs, modelID)
		}
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("iterate litellm model rows: %w", err)
		}
	} else {
		// 库连不上但 HTTP 可用时也要能注销，避免「全部注销」因 DB 未连接而完全不可用
		provisioned, err := g.ListProvisioned(schema)
		if err != nil {
			return 0, err
		}
		for _, p := range provisioned {
			modelIDs = append(modelIDs, p.GatewayID)
		}
	}

	var count int
	for _, id := range modelIDs {
		if err := g.deleteModel(id); err != nil {
			return count, fmt.Errorf("deprovision model %s: %w", id, err)
		}
		count++
	}
	return count, nil
}

// listModels 调用 GET /model/info 拉取网关上全部模型记录
func (g *ProxyGateway) listModels() ([]modelInfoItem, error) {
	req, err := http.NewRequest("GET", g.URL+"/model/info", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.MasterKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("model/info returned %d: %s", resp.StatusCode, string(body))
	}

	var result modelInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode model/info response: %w", err)
	}
	return result.Data, nil
}

// ListProvisioned 列出某 schema 下已注册到网关的模型。
// LiteLLM 侧注册名形如 "<schema>:<gateway_model>"，故按该前缀过滤；
// 没有 model_info.id 的行既无法定位也无法删除，直接跳过。
func (g *ProxyGateway) ListProvisioned(schema string) ([]gateway.ProvisionedModel, error) {
	if schema == "" {
		return nil, fmt.Errorf("schema is empty")
	}
	items, err := g.listModels()
	if err != nil {
		return nil, err
	}

	prefix := schema + ":"
	provisioned := make([]gateway.ProvisionedModel, 0, len(items))
	for _, item := range items {
		if !strings.HasPrefix(item.ModelName, prefix) {
			continue
		}
		id, _ := item.ModelInfo["id"].(string)
		if id == "" {
			continue
		}
		provisioned = append(provisioned, gateway.ProvisionedModel{
			GatewayID:     id,
			ModelName:     item.ModelName,
			LitellmParams: item.LitellmParams,
			ModelInfo:     item.ModelInfo,
		})
	}
	return provisioned, nil
}

// Update 按 GatewayID 覆盖更新网关上已注册模型的参数（POST /model/update）。
// 与 registerModel 复用同一套参数构造，保证「注册」与「对齐」写进去的内容一致。
func (g *ProxyGateway) Update(schema string, model gateway.ModelInput, provider gateway.ProviderInput) error {
	if model.GatewayID == "" {
		return fmt.Errorf("model.GatewayID is empty, cannot update")
	}

	bodyMap := map[string]any{
		"model_id":       model.GatewayID,
		"model_name":     schema + ":" + model.GatewayModel,
		"litellm_params": buildLitellmParamsMap(newLitellmParams(provider, model)),
		"model_info":     buildModelInfoMap(model),
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", g.URL+"/model/update", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.MasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("model/update returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// newLitellmParams 构造 LiteLLM 调用参数：
//   - model 用 "<protocol>/<model.Name>" 告诉 LiteLLM 实际调哪个上游
//   - api_key / api_base 来自 provider
//   - ExtraConfig 原样透传（api_version / vertex_project / timeout / max_retries 等）
//
// registerModel 与 Update 共用，避免两处写法漂移。
//
// 注意优先级：buildLitellmParamsMap 先写 model / api_key / api_base，再合并 ExtraConfig，
// 所以 extra_config 里若出现同名键（model / api_key / api_base）会覆盖上面的表单值。
func newLitellmParams(provider gateway.ProviderInput, model gateway.ModelInput) litellmParams {
	return litellmParams{
		Model:   provider.Protocol + "/" + model.Name,
		APIKey:  provider.APIKey,
		APIBase: provider.URL,
		Extra:   provider.ExtraConfig,
	}
}

// registerModel 通过 POST /model/new 注册模型，返回 LiteLLM 生成的 model_id（UUID）
func (g *ProxyGateway) registerModel(schema string, model gateway.ModelInput, provider gateway.ProviderInput) (string, error) {
	// model_name: 在 LiteLLM Proxy 中的唯一标识，用 schema + 模型在网关中的名称构造
	gatewayModelName := schema + ":" + model.GatewayModel

	params := registerModelParams{
		ModelName:     gatewayModelName,
		LitellmParams: newLitellmParams(provider, model),
	}

	// 使用自定义序列化合并 Extra
	bodyMap := map[string]any{
		"model_name":     params.ModelName,
		"litellm_params": buildLitellmParamsMap(params.LitellmParams),
		"model_info":     buildModelInfoMap(model),
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", g.URL+"/model/new", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.MasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("model/new returned %d: %s", resp.StatusCode, string(respBody))
	}

	// 解析 LiteLLM 生成的 model_id（UUID），作为 gatewayID
	var result registerModelResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode model/new response: %w", err)
	}
	if result.ModelID == "" {
		return "", fmt.Errorf("model/new response missing model_id")
	}
	return result.ModelID, nil
}

// deleteModel 通过 POST /model/delete 删除模型，参数为 LiteLLM 的 model_id（UUID）
func (g *ProxyGateway) deleteModel(modelID string) error {
	bodyMap := map[string]any{
		"id": modelID,
	}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", g.URL+"/model/delete", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.MasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("model/delete returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func buildLitellmParamsMap(p litellmParams) map[string]any {
	m := map[string]any{
		"model":   p.Model,
		"api_key": p.APIKey,
	}
	if p.APIBase != "" {
		m["api_base"] = p.APIBase
	}
	if p.APIVersion != "" {
		m["api_version"] = p.APIVersion
	}
	for k, v := range p.Extra {
		m[k] = v
	}
	return m
}

func buildModelInfoMap(model gateway.ModelInput) map[string]any {
	m := map[string]any{
		"mode": "chat",
	}
	if model.ContextWindow > 0 {
		m["context_window"] = model.ContextWindow
	}
	if model.MaxTokens > 0 {
		m["max_tokens"] = model.MaxTokens
	}
	for k, v := range model.ExtraParams {
		m[k] = v
	}
	return m
}

// Stream 流式调用 POST /chat/completions (stream=true)
func (g *ProxyGateway) Stream(ctx context.Context, model gateway.ModelInput, messages []gateway.Message, tools []gateway.ToolDef) (<-chan gateway.StreamChunk, error) {
	reqBody := g.buildChatRequest(model, messages, tools, true)
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", g.URL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.MasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("chat/completions returned %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan gateway.StreamChunk, 64)
	go g.readSSEStream(resp.Body, ch)
	return ch, nil
}

// ---- DeepSeek 原生工具调用标记（|DSML|）兜底解析 ----
//
// 某些上游（自建 vLLM / SGLang 等未启用 tool-call parser）不会把模型的原生工具调用
// 还原成 OpenAI 的 tool_calls 字段，而是把它当作普通正文写进 content，形如：
//
//	<|DSML|function_calls>
//	<|DSML|invoke name="read_skill">
//	<|DSML|parameter name="name" string="true">english-joke-writing</|DSML|parameter>
//	</|DSML|invoke>
//	</|DSML|function_calls>
//
// 这里在传输层做兜底：把标记还原成 gateway.ToolCall，并从正文里剥离，
// 避免「工具没被调用、标记泄漏给用户」。竖线兼容全角（｜）与半角（|）及单/双竖线 ——
// 不同推理框架输出的字符宽度不一致。
const (
	dsmlOpen  = `<\s*[|｜]{1,2}\s*DSML\s*[|｜]{1,2}\s*`
	dsmlClose = `</\s*[|｜]{1,2}\s*DSML\s*[|｜]{1,2}\s*`
)

// dsmlHoldback 流式下保留的尾部字节数：保证半截标记不会被提前推给前端。
// 取值需 ≥ 标记起始的最长字节数（`<｜｜DSML｜｜` ≈ 14 字节）。
const dsmlHoldback = 32

var (
	// dsmlStartRe 标记起始（判定正文是否已进入标记区，用于流式延迟推送）。
	dsmlStartRe = regexp.MustCompile(dsmlOpen + `(?:function_calls|invoke|parameter)`)
	// dsmlBlockRe 整个 function_calls 块，从正文中剥除。
	dsmlBlockRe = regexp.MustCompile(`(?s)` + dsmlOpen + `function_calls\s*>.*?` + dsmlClose + `function_calls\s*>`)
	// dsmlInvokeRe 单个 invoke（工具名 + 参数体）。
	dsmlInvokeRe = regexp.MustCompile(`(?s)` + dsmlOpen + `invoke\s+name="([^"]+)"\s*>(.*?)` + dsmlClose + `invoke\s*>`)
	// dsmlParamRe 单个 parameter（名字 + 属性 + 值）。
	dsmlParamRe = regexp.MustCompile(`(?s)` + dsmlOpen + `parameter\s+name="([^"]+)"([^>]*)>(.*?)` + dsmlClose + `parameter\s*>`)
)

// parseDSMLToolCalls 从正文解析 DeepSeek 原生工具调用标记，返回调用列表与剥离标记后的正文。
// 无标记时不改动正文（calls 为 nil），调用方可据此零成本放行。
func parseDSMLToolCalls(content string) ([]gateway.ToolCall, string) {
	if !strings.Contains(content, "DSML") {
		return nil, content
	}
	invs := dsmlInvokeRe.FindAllStringSubmatch(content, -1)
	if len(invs) == 0 {
		return nil, content
	}
	calls := make([]gateway.ToolCall, 0, len(invs))
	for i, m := range invs {
		tc := gateway.ToolCall{ID: fmt.Sprintf("dsml_%d", i), Name: m[1], Args: map[string]any{}, FromText: true}
		for _, pm := range dsmlParamRe.FindAllStringSubmatch(m[2], -1) {
			key, attrs, val := pm[1], pm[2], strings.TrimSpace(pm[3])
			// string="true" 时按纯文本取值；否则按 JSON 解（数字 / 布尔 / 对象）
			if !strings.Contains(attrs, `string="true"`) {
				var v any
				if err := json.Unmarshal([]byte(val), &v); err == nil {
					tc.Args[key] = v
					continue
				}
			}
			tc.Args[key] = val
		}
		calls = append(calls, tc)
	}
	// 剥离标记：先删整个 function_calls 块，再删可能未被外层包裹的裸 invoke
	cleaned := dsmlBlockRe.ReplaceAllString(content, "")
	cleaned = dsmlInvokeRe.ReplaceAllString(cleaned, "")
	return calls, strings.TrimSpace(cleaned)
}

// safeEmitLen 返回 pending 中「可安全推送」的字节数：不会把多字节字符切一半。
// want 为期望推送的字节数；结果向上收敛到最近的 rune 起始字节。
// 流式下按字节数切片会让汉字被拆开，下游 json.Marshal 会把非法字节渲染成 U+FFFD。
func safeEmitLen(pending string, want int) int {
	if want <= 0 {
		return 0
	}
	if want > len(pending) {
		want = len(pending)
	}
	// 串尾（want == len(pending)）必然是边界；否则回退到最近的 rune 起始字节
	for want > 0 && want < len(pending) && !utf8.RuneStart(pending[want]) {
		want--
	}
	return want
}

// readSSEStream 读取 SSE 流并输出到 channel
func (g *ProxyGateway) readSSEStream(body io.ReadCloser, ch chan<- gateway.StreamChunk) {
	defer body.Close()
	defer close(ch)

	scanner := bufio.NewScanner(body)
	// 增大 buffer 防止长行截断
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	// 累积工具调用（流式中 tool_calls 分片到达，需按 index 累积 arguments）
	var toolCallAccum map[int]*gateway.ToolCall

	// 累积正文并做 |DSML| 延迟推送：命中标记时停在标记前，剩余交给 flushTail 统一解析/剥离。
	var acc strings.Builder
	emitted := 0
	inDSML := false

	// pushContent 累积一段正文增量，并尽可能即时推送「确定不含标记」的部分。
	pushContent := func(delta string) {
		if delta == "" {
			return
		}
		acc.WriteString(delta)
		if inDSML {
			return // 已在标记区，等本轮结束统一处理
		}
		pending := acc.String()[emitted:]
		if loc := dsmlStartRe.FindStringIndex(pending); loc != nil {
			if loc[0] > 0 {
				ch <- gateway.StreamChunk{Content: pending[:loc[0]]}
				emitted += loc[0]
			}
			inDSML = true
			return
		}
		// 未出现完整标记：保留尾部 dsmlHoldback 字节（可能含半截标记），其余即时推出。
		// 注意按 rune 边界收敛，避免把多字节字符切一半（否则会产生 U+FFFD 乱码）。
		if n := safeEmitLen(pending, len(pending)-dsmlHoldback); n > 0 {
			ch <- gateway.StreamChunk{Content: pending[:n]}
			emitted += n
		}
	}

	// flushTail 本轮结束：解析 |DSML| 标记，补推剩余干净正文，返回解析出的工具调用。
	flushTail := func() []gateway.ToolCall {
		calls, clean := parseDSMLToolCalls(acc.String())
		if n := safeEmitLen(clean, len(clean)); n > emitted {
			ch <- gateway.StreamChunk{Content: clean[emitted:n]}
			emitted = n
		}
		return calls
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			if calls := flushTail(); len(calls) > 0 {
				ch <- gateway.StreamChunk{ToolCalls: calls}
			}
			ch <- gateway.StreamChunk{Done: true}
			return
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			ch <- gateway.StreamChunk{Error: fmt.Errorf("unmarshal stream chunk: %w", err)}
			return
		}

		for _, choice := range chunk.Choices {
			// 处理内容（含 |DSML| 标记的延迟推送）
			pushContent(choice.Delta.Content)
			// 处理 reasoning content
			if choice.Delta.ReasoningContent != "" {
				ch <- gateway.StreamChunk{ReasoningContent: choice.Delta.ReasoningContent}
			}
			// 处理工具调用（累积分片）
			if len(choice.Delta.ToolCalls) > 0 {
				if toolCallAccum == nil {
					toolCallAccum = make(map[int]*gateway.ToolCall)
				}
				for _, tc := range choice.Delta.ToolCalls {
					idx := tc.Index
					if toolCallAccum[idx] == nil {
						toolCallAccum[idx] = &gateway.ToolCall{
							ID:   tc.ID,
							Name: tc.Function.Name,
						}
					} else {
						if tc.ID != "" {
							toolCallAccum[idx].ID = tc.ID
						}
						if tc.Function.Name != "" {
							toolCallAccum[idx].Name = tc.Function.Name
						}
					}
					// 累积 arguments（JSON 字符串分片）
					if tc.Function.Arguments != "" {
						if toolCallAccum[idx].Args == nil {
							toolCallAccum[idx].Args = map[string]any{"_raw_arguments": tc.Function.Arguments}
						} else {
							if raw, ok := toolCallAccum[idx].Args["_raw_arguments"].(string); ok {
								toolCallAccum[idx].Args["_raw_arguments"] = raw + tc.Function.Arguments
							}
						}
					}
				}
			}
			// finish_reason 表示该 choice 完成
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				var calls []gateway.ToolCall
				// 标准 OpenAI tool_calls（如有），解析累积的 arguments JSON
				if toolCallAccum != nil {
					for i := 0; i < len(toolCallAccum); i++ {
						tc := toolCallAccum[i]
						if tc == nil {
							continue
						}
						if raw, ok := tc.Args["_raw_arguments"].(string); ok {
							var args map[string]any
							if err := json.Unmarshal([]byte(raw), &args); err == nil {
								tc.Args = args
							} else {
								tc.Args = map[string]any{"_raw": raw}
							}
						}
						calls = append(calls, *tc)
					}
					toolCallAccum = nil
				}
				// 合并正文里被当作纯文本输出的 |DSML| 工具调用
				calls = append(calls, flushTail()...)
				if len(calls) > 0 {
					ch <- gateway.StreamChunk{ToolCalls: calls}
				}
				ch <- gateway.StreamChunk{Done: true}
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- gateway.StreamChunk{Error: err}
		return
	}
	// 流正常结束但未收到 [DONE]
	if calls := flushTail(); len(calls) > 0 {
		ch <- gateway.StreamChunk{ToolCalls: calls}
	}
	ch <- gateway.StreamChunk{Done: true}
}

// Generate 非流式调用 POST /chat/completions (stream=false)
func (g *ProxyGateway) Generate(ctx context.Context, model gateway.ModelInput, messages []gateway.Message, tools []gateway.ToolDef) (gateway.GenerateResponse, error) {
	reqBody := g.buildChatRequest(model, messages, tools, false)
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return gateway.GenerateResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", g.URL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return gateway.GenerateResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+g.MasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return gateway.GenerateResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return gateway.GenerateResponse{}, fmt.Errorf("chat/completions returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return gateway.GenerateResponse{}, fmt.Errorf("decode chat response: %w", err)
	}

	var response gateway.GenerateResponse
	if len(result.Choices) > 0 {
		choice := result.Choices[0]
		// content 可能是 string 或 null
		if len(choice.Message.Content) > 0 && string(choice.Message.Content) != "null" {
			var contentStr string
			if err := json.Unmarshal(choice.Message.Content, &contentStr); err == nil {
				response.Content = contentStr
			} else {
				response.Content = string(choice.Message.Content)
			}
		}
		// 思考内容：与流式分支对齐，非流式模式不能把 reasoning 丢掉
		response.ReasoningContent = choice.Message.ReasoningContent
		// 解析 tool calls
		for _, tc := range choice.Message.ToolCalls {
			var args map[string]any
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = map[string]any{"_raw": tc.Function.Arguments}
				}
			}
			response.ToolCalls = append(response.ToolCalls, gateway.ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: args,
			})
		}
		// 正文里若混入了 |DSML| 原生工具调用标记（上游未解析），还原并剥离
		if dsmlCalls, cleaned := parseDSMLToolCalls(response.Content); len(dsmlCalls) > 0 {
			response.Content = cleaned
			response.ToolCalls = append(response.ToolCalls, dsmlCalls...)
		}
	}

	return response, nil
}

// buildChatRequest 构建 chat/completions 请求体（map 承载，extra_params 直接透传顶层）
func (g *ProxyGateway) buildChatRequest(model gateway.ModelInput, messages []gateway.Message, tools []gateway.ToolDef, stream bool) map[string]any {
	req := map[string]any{
		"model":    model.GatewayModel,
		"messages": messages,
		"stream":   stream,
	}

	// 设置可选参数
	if model.Temperature > 0 {
		req["temperature"] = model.Temperature
	}
	if model.TopP > 0 {
		req["top_p"] = model.TopP
	}
	if model.MaxTokens > 0 {
		req["max_tokens"] = model.MaxTokens
	}

	// 构建工具定义
	if len(tools) > 0 {
		toolsList := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			toolsList = append(toolsList, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		req["tools"] = toolsList
	}

	// extra_params 统一放进 extra_body：LiteLLM 不校验，展开后透传给上游，不报 UnsupportedParamsError
	if len(model.ExtraParams) > 0 {
		eb, _ := req["extra_body"].(map[string]any)
		if eb == nil {
			eb = make(map[string]any)
		}
		for k, v := range model.ExtraParams {
			if _, exists := eb[k]; !exists {
				eb[k] = v
			}
		}
		req["extra_body"] = eb
	}

	return req
}
