// Package plugins 管理端「插件」接口：磁盘 Python 插件的发现结果 + 租户级启用与配置。
//
// 语义要点：**没有「新建插件」接口**。插件是磁盘上放进去的代码（自注册），
// 管理端只做「看见它 / 启用它 / 选实现 / 填配置 / 试一次」。
package plugins

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"flame/nexus/service/plugin"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/models"
	"flame/nexus/shared/pluginpolicy"
	pluginsrepo "flame/nexus/shared/repository/plugins"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

var repo = pluginsrepo.NewRepository()

// secretMask 打码前缀。回传时前端原样带回 —— 后端据此判定「这个 secret 没改」，
// 从而保留旧值（不把真 key 回显给浏览器，也不会因为打码串把 key 洗掉）。
const secretMask = "••••"

// PluginView 管理端一行 = 磁盘清单（工具面 + 实现清单） + 本租户状态 + 平台策略。
type PluginView struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name"`
	Description string         `json:"description"`
	Version     string         `json:"version"`
	Kind        string         `json:"kind"`
	Parameters  map[string]any `json:"parameters"`

	Implementations []plugin.Implementation `json:"implementations"`

	Configured bool                      `json:"configured"` // 本租户是否已保存过配置
	IsActive   bool                      `json:"is_active"`
	Impl       string                    `json:"impl"`
	Fallbacks  []string                  `json:"fallbacks"`
	Configs    map[string]map[string]any `json:"configs"` // secret 字段已打码

	// 平台策略（只读）：由平台管理端在「租户 → 插件」里配，
	// 本接口只回显，UpdatePlugin 不接受该字段 —— 能不能用是成本与安全护栏，
	// 不能让租户自己开。
	//
	// Blocked 是全局闸：与「本租户没开通」分开，租户才能看出该找谁解决。
	Allowed bool `json:"allowed"`
	Blocked bool `json:"blocked"`
}

// ListPlugins 列出磁盘上发现的插件 + 本租户状态 + 扫描错误。
func ListPlugins(c *gin.Context) {
	authInfo, ok := mustAuth(c)
	if !ok {
		return
	}

	src := plugin.DefaultSource()
	if src == nil {
		xhttp.OkJSON(c, "Success", gin.H{
			"plugins": []PluginView{}, "errors": []plugin.ScanError{},
		}, "")
		return
	}

	snap := src.Snapshot()
	rows, err := repo.List(authInfo)
	if err != nil {
		xhttp.ErrJSON(c, "读取插件配置失败: "+err.Error(), nil, "")
		return
	}
	byName := make(map[string]*models.Plugin, len(rows))
	for i := range rows {
		// 本页只列 tools：把索引限制在 tools 的行上，否则将来同名的 sources 行
		// 会把工具配置挤掉（List 是跨 kind 全取）。
		if rows[i].Kind != pluginsrepo.KindTools {
			continue
		}
		byName[rows[i].Name] = &rows[i]
	}

	// 平台策略一次全取：逐个 Resolve 会打出 N 条查询（插件数量不受控）。
	//
	// 读失败（多半是 platform.tenant_plugins 还没建表）时**不能按缺省**：缺省是开放，
	// 而「读不出」不等于「没人停用过」。降级为全部「已停用」，页面上带 policy_error 提醒 ——
	// 租户侧的插件配置照常展示，但不能因为平台表没建就变成全开。
	policies, perr := pluginpolicy.List(authInfo.Schema)
	if perr != nil {
		xlog.Error("读取插件策略失败 schema=%s（按「已停用」展示）: %v", authInfo.Schema, perr)
		policies = map[string]pluginpolicy.Policy{}
	}

	out := make([]PluginView, 0, len(snap.Tools))
	for _, m := range snap.Tools {
		pol := policyOf(policies, m.Name)
		if perr != nil {
			pol = pluginpolicy.Policy{Allowed: false} // 保守方向：读不到就当被停用
		}
		out = append(out, buildView(m, byName[m.Name], pol))
	}

	body := gin.H{
		"plugins": out,
		"errors":  snap.Errors,
	}
	if !snap.At.IsZero() {
		body["refreshed_at"] = snap.At
	}
	if snap.Err != nil {
		body["refresh_error"] = snap.Err.Error()
	}
	if perr != nil {
		body["policy_error"] = perr.Error()
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// updateReq 更新插件配置。configs 按实现分键；secret 字段原样带回打码串表示「不改」。
type updateReq struct {
	Impl      string                    `json:"impl"`
	Fallbacks []string                  `json:"fallbacks"`
	Configs   map[string]map[string]any `json:"configs"`
	IsActive  *bool                     `json:"is_active"`
}

// UpdatePlugin 保存某插件的启用状态 / 主实现 / 兜底链 / 配置。
func UpdatePlugin(c *gin.Context) {
	authInfo, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	m, found := lookupManifest(name)
	if !found {
		xhttp.ErrJSON(c, fmt.Sprintf("插件 %s 不在磁盘清单里（先放进 plugins/tools/%s/ 再刷新）", name, name), nil, "")
		return
	}

	var req updateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "请求体格式不对", nil, "")
		return
	}
	if msg := validateSelection(m, req.Impl, req.Fallbacks, req.Configs); msg != "" {
		xhttp.ErrJSON(c, msg, nil, "")
		return
	}

	old := &models.Plugin{Kind: m.Kind, Name: m.Name}
	if p, err := repo.Get(authInfo, m.Kind, m.Name); err == nil {
		old = p
	} else if !errors.Is(err, sql.ErrNoRows) {
		xhttp.ErrJSON(c, "读取插件配置失败: "+err.Error(), nil, "")
		return
	}

	active := old.IsActive
	if req.IsActive != nil {
		active = *req.IsActive
	}
	impl := req.Impl
	if impl == "" {
		impl = old.Impl
	}
	// 启用一个没选实现的插件 = 建了一条调用必然失败的配置，直接挡在保存前
	if active && impl == "" {
		xhttp.ErrJSON(c, "启用前请先选择主实现", nil, "")
		return
	}

	row := &models.Plugin{
		ID:        old.ID,
		Kind:      m.Kind,
		Name:      m.Name,
		Impl:      impl,
		Fallbacks: toJSONB(normalizeFallbacks(req.Fallbacks)),
		Configs:   toJSONB(mergeConfigs(old.AllConfigs(), req.Configs)),
		IsActive:  active,
	}
	if err := repo.Upsert(authInfo, row); err != nil {
		xhttp.ErrJSON(c, "保存失败: "+err.Error(), nil, "")
		return
	}

	saved, err := repo.Get(authInfo, m.Kind, m.Name)
	if err != nil {
		xhttp.ErrJSON(c, "保存后读取失败: "+err.Error(), nil, "")
		return
	}
	// 保存已经成功，策略读失败不该把这次写入回报成失败；但也别回显成「开放」——
	// 这与 ListPlugins 的保守口径保持一致（读不到就当被停用）。
	pol, perr := pluginpolicy.Resolve(authInfo.Schema, m.Name)
	if perr != nil {
		pol = pluginpolicy.Policy{Allowed: false}
	}
	xhttp.OkJSON(c, "Success", buildView(m, saved, pol), "")
}

// testReq 「试一次」：可带未保存的配置（用户刚填的 key 不必先保存才能测）。
type testReq struct {
	Impl    string         `json:"impl"`
	Query   string         `json:"query"`
	Configs map[string]any `json:"configs"`
}

// TestPlugin 用真实后端跑一次检索，回显前 3 条结果或原始错误。
//
// 刻意走 Host 而不是 Runner：Runner 要求「已保存且已启用」，而这里恰恰是用来
// 在保存之前验证配置的。超时/错误分类仍与生产一致（同一个宿主进程）。
func TestPlugin(c *gin.Context) {
	authInfo, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	m, found := lookupManifest(name)
	if !found {
		xhttp.ErrJSON(c, fmt.Sprintf("插件 %s 不在磁盘清单里", name), nil, "")
		return
	}

	var req testReq
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "请求体格式不对", nil, "")
		return
	}
	impl := strings.TrimSpace(req.Impl)
	if _, ok := m.ImplementationOf(impl); !ok {
		xhttp.ErrJSON(c, fmt.Sprintf("实现 %q 不在插件清单里", impl), nil, "")
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		query = "flame plugin test"
	}

	// 「试一次」也真实打上游，所以同样受平台策略约束：没开通直接拒。
	// 这里不要求 is_active：该接口存在的意义就是「保存之前先验一下能不能通」。
	pol, err := pluginpolicy.Resolve(authInfo.Schema, m.Name)
	if err != nil {
		xhttp.ErrJSON(c, "读取插件授权失败: "+err.Error(), nil, "")
		return
	}
	if !pol.Allowed {
		if pol.Blocked {
			xhttp.ErrJSON(c, fmt.Sprintf("插件 %s 已被平台全局停用", m.Name), nil, "")
			return
		}
		xhttp.ErrJSON(c, fmt.Sprintf("插件 %s 未对本租户开放（请联系平台管理员开通）", m.Name), nil, "")
		return
	}

	// 已保存的配置打底，再用请求里的覆盖（跳过打码串）
	cfg := map[string]any{}
	if p, err := repo.Get(authInfo, m.Kind, m.Name); err == nil {
		for k, v := range p.ImplConfig(impl) {
			cfg[k] = v
		}
	}
	for k, v := range req.Configs {
		if s, ok := v.(string); ok && strings.HasPrefix(s, secretMask) {
			continue
		}
		cfg[k] = v
	}

	cfgInfo := config.Cfg.Plugin
	host := plugin.NewHost(
		cfgInfo.DirOrDefault(config.Cfg.Agent.RootDir),
		cfgInfo.Python,
		cfgInfo.TimeoutDuration(),
	)

	started := time.Now()
	raw, err := host.Invoke(c.Request.Context(), m.Name, impl, &plugin.InvokePayload{
		Meta:   map[string]any{"tenant": authInfo.Schema},
		Config: cfg,
		Args:   map[string]any{"query": query, "count": 3},
	})
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		xhttp.OkJSON(c, "Success", gin.H{
			"ok":         false,
			"error":      err.Error(),
			"error_code": plugin.Code(err),
			"elapsed_ms": elapsed,
		}, "")
		return
	}

	// 只回前 3 条：测试连接是给人看的，不是给模型用的
	res, _ := raw.(map[string]any)
	results := []any{}
	if items, ok := res["results"].([]any); ok {
		if len(items) > 3 {
			items = items[:3]
		}
		results = items
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"ok":         true,
		"provider":   res["provider"],
		"results":    results,
		"answer":     res["answer"],
		"notes":      res["notes"],
		"elapsed_ms": elapsed,
	}, "")
}

// RefreshPlugins 立即重扫插件目录（不等后台的刷新周期）。
func RefreshPlugins(c *gin.Context) {
	reg := plugin.Current()
	if reg == nil {
		xhttp.ErrJSON(c, "插件注册表未初始化（检查 plugin.dir / 启动日志）", nil, "")
		return
	}
	if err := reg.Refresh(c.Request.Context(), true); err != nil {
		xhttp.ErrJSON(c, "刷新失败: "+err.Error(), nil, "")
		return
	}
	snap := reg.Snapshot()
	xhttp.OkJSON(c, "Success", gin.H{
		"tools": len(snap.Tools), "errors": snap.Errors, "refreshed_at": snap.At,
	}, "")
}

// ===== 内部工具 =====

func mustAuth(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrJSON(c, "Invalid authentication information", nil, "")
		return nil, false
	}
	return info, true
}

func lookupManifest(name string) (plugin.Manifest, bool) {
	src := plugin.DefaultSource()
	if src == nil {
		return plugin.Manifest{}, false
	}
	return src.Get(name)
}

// buildView 合并「磁盘清单」与「租户配置」，顺带把 secret 打码，并回显平台策略。
func buildView(m plugin.Manifest, p *models.Plugin, pol pluginpolicy.Policy) PluginView {
	v := PluginView{
		Name:            m.Name,
		DisplayName:     m.DisplayName,
		Description:     m.Description,
		Version:         m.Version,
		Kind:            m.Kind,
		Parameters:      m.Parameters,
		Implementations: m.Implementations,
		Fallbacks:       []string{},
		Configs:         map[string]map[string]any{},

		Allowed: pol.Allowed,
		Blocked: pol.Blocked,
	}
	// p 为 nil（本租户还没配过）也要回显策略：平台没开通的插件，界面上应该一眼看出来
	if p == nil {
		return v
	}
	v.Configured = true
	v.IsActive = p.IsActive
	v.Impl = p.Impl
	if fb := p.FallbackImpls(); fb != nil {
		v.Fallbacks = fb
	}
	v.Configs = maskConfigs(m, p.AllConfigs())
	return v
}

// policyOf 从「一次全取」的结果里取某插件的策略，缺省补成「开放」。
func policyOf(all map[string]pluginpolicy.Policy, name string) pluginpolicy.Policy {
	if p, ok := all[name]; ok {
		return p
	}
	return pluginpolicy.Default()
}

// maskConfigs 把每个实现里的 secret 字段替换成「••••尾4位」。
func maskConfigs(m plugin.Manifest, all map[string]map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(all))
	for impl, cfg := range all {
		masked := make(map[string]any, len(cfg))
		for k, val := range cfg {
			masked[k] = val
		}
		info, ok := m.ImplementationOf(impl)
		if ok {
			for _, f := range info.ConfigFields {
				if !f.Secret {
					continue
				}
				if s, ok := masked[f.Key].(string); ok && s != "" {
					masked[f.Key] = maskValue(s)
				}
			}
		}
		out[impl] = masked
	}
	return out
}

func maskValue(s string) string {
	if len(s) <= 4 {
		return secretMask
	}
	return secretMask + s[len(s)-4:]
}

// validateSelection 校验「主实现 / 兜底链 / 配置键」都落在清单里，返回空串表示通过。
func validateSelection(m plugin.Manifest, impl string, fallbacks []string, cfgs map[string]map[string]any) string {
	if impl != "" {
		if _, ok := m.ImplementationOf(impl); !ok {
			return fmt.Sprintf("实现 %q 不在插件清单里", impl)
		}
	}
	seen := map[string]bool{}
	for _, fb := range fallbacks {
		fb = strings.TrimSpace(fb)
		if fb == "" {
			continue
		}
		if _, ok := m.ImplementationOf(fb); !ok {
			return fmt.Sprintf("兜底实现 %q 不在插件清单里", fb)
		}
		if seen[fb] {
			return fmt.Sprintf("兜底实现 %q 重复", fb)
		}
		seen[fb] = true
	}
	for implName := range cfgs {
		if _, ok := m.ImplementationOf(implName); !ok {
			return fmt.Sprintf("配置里出现未知实现 %q", implName)
		}
	}
	return ""
}

// mergeConfigs 按实现合并：只覆盖请求里出现的键；打码串（未改）保留旧值。
func mergeConfigs(old, incoming map[string]map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(old)+len(incoming))
	for impl, cfg := range old {
		cp := make(map[string]any, len(cfg))
		for k, v := range cfg {
			cp[k] = v
		}
		out[impl] = cp
	}
	for impl, cfg := range incoming {
		cur := out[impl]
		if cur == nil {
			cur = map[string]any{}
			out[impl] = cur
		}
		for k, v := range cfg {
			if s, ok := v.(string); ok && strings.HasPrefix(s, secretMask) {
				continue // 打码回传 = 没改
			}
			cur[k] = v
		}
	}
	return out
}

func normalizeFallbacks(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func toJSONB(v any) xsql.JSONB {
	b, err := json.Marshal(v)
	if err != nil {
		return xsql.JSONB{Data: json.RawMessage("{}")}
	}
	return xsql.JSONB{Data: json.RawMessage(b)}
}
