package handler

import (
	"fmt"
	"sort"
	"strings"

	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/plugininfo"
	"flame/nexus/shared/pluginpolicy"
	pluginsrepo "flame/nexus/shared/repository/plugins"
	"flame/tools/xhttp"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// 插件授权：platform.tenant_plugins（租户行）+ platform.plugin_gates（全局闸）的读写。
//
// 三种值要分清楚：
//
//	row      本租户的原始行（null = 没设过 = 未开通）—— 编辑框绑它
//	blocked  全局闸是否拦下（缺省放行）—— 「还开不开」，压过租户的显式开启
//	enabled  租户侧认不认这个插件（{schema}.plugins.is_active）—— 只读展示
//
// 生效 = (租户行 ?? 未开通) AND NOT blocked。2026-10-21 移除了「系统默认行」
// （tenant_id = ''）：它只改缺省值、与急停是两种东西，混在一张表里会让管理员
// 分不清「我开的」和「本来就没有」。
//
// 表结构见 nexus/platform/sql/tenant_plugins.sql 与 plugin_gates.sql；解析逻辑见 shared/pluginpolicy。

// pluginCfgRepo 读租户侧插件配置（只取 name / is_active / impl，不碰 key）。
var pluginCfgRepo = pluginsrepo.NewRepository()

// policyJSON 策略的对外形状（与 shared/pluginpolicy.Policy 一一对应）。
type policyJSON struct {
	Allowed bool `json:"allowed"`
}

func toPolicyJSON(p pluginpolicy.Policy) policyJSON {
	return policyJSON{Allowed: p.Allowed}
}

// tenantPluginView 平台「租户 → 插件」列表的一行。
type tenantPluginView struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Version     string `json:"version"`

	// 生效值（本租户行 ?? 开放），已乘上全局闸
	policyJSON
	Blocked bool `json:"blocked"` // 全局闸是否拦下（false = 不拦，含「没有闸行」）

	Row *policyJSON `json:"row"` // 本租户原始行；null = 没设过（= 缺省开放）

	// 租户侧状态：开了没、用哪个实现（额度与实现无关，但并排看更有用）
	Enabled bool   `json:"enabled"`
	Impl    string `json:"impl"`
}

// ListTenantPlugins GET /platform/tenants/:schema/plugins
// 磁盘上发现的插件 × 本租户策略 × 租户侧启用状态。
func ListTenantPlugins(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}

	rows, err := pluginpolicy.Rows(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant plugin policy: "+err.Error(), nil, "")
		return
	}
	gates, err := pluginpolicy.Gates()
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load plugin gates: "+err.Error(), nil, "")
		return
	}

	// 租户侧配置读失败（schema 还没建、表不存在）不影响平台授权：按「未启用」展示。
	enabled := map[string]bool{}
	impl := map[string]string{}
	if list, lerr := pluginCfgRepo.List(&auth.AuthInfo{Schema: schema}); lerr == nil {
		for _, p := range list {
			if p.Kind != pluginsrepo.KindTools { // List 跨 kind，这里只要工具插件
				continue
			}
			enabled[p.Name] = p.IsActive
			impl[p.Name] = p.Impl
		}
	}

	tools := plugininfo.Tools()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	out := make([]tenantPluginView, 0, len(tools))
	for _, t := range tools {
		eff := pluginpolicy.Default()
		if p, ok := rows[t.Name]; ok {
			eff = p
		}
		v := tenantPluginView{
			Name:        t.Name,
			DisplayName: t.DisplayName,
			Description: t.Description,
			Version:     t.Version,
			Enabled:     enabled[t.Name],
			Impl:        impl[t.Name],
		}
		if p, ok := rows[t.Name]; ok {
			r := toPolicyJSON(p)
			v.Row = &r
		}
		if gates[t.Name] {
			eff.Allowed = false // 硬闸最后乘上去的 0；原始行原样保留
			v.Blocked = true
		}
		v.policyJSON = toPolicyJSON(eff)
		out = append(out, v)
	}

	xhttp.OkJSON(c, "Success", gin.H{"plugins": out}, "")
}

// setPolicyReq 覆盖式写入本租户行；字段省略 = 保留当前值（先读后改，避免误清）。
type setPolicyReq struct {
	Allowed *bool `json:"allowed"`
}

// SetTenantPluginPolicy PUT /platform/tenants/:schema/plugins/:name
func SetTenantPluginPolicy(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	name, ok := mustPlugin(c)
	if !ok {
		return
	}
	writePluginPolicy(c, schema, name)
}

// DeleteTenantPluginPolicy DELETE /platform/tenants/:schema/plugins/:name
// 删掉本租户行 = 回到缺省（未开通）。系统默认行已移除，所以「清除」就是彻底关掉。
func DeleteTenantPluginPolicy(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	name, ok := mustPlugin(c)
	if !ok {
		return
	}
	if err := pluginpolicy.Delete(schema, name); err != nil {
		xhttp.ErrJSON(c, "Failed to delete tenant plugin policy: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"schema": schema, "plugin": name, "row": nil}, "")
}

// setPluginGateReq 闸写入；blocked 省略视为 false（= 不拦）。
//
// 不叫 setGateReq：本包已有一份属于能力闸的同名类型，重名会直接编译不过。
type setPluginGateReq struct {
	Blocked *bool `json:"blocked"`
}

// SetPluginGate PUT /platform/system/plugin-gates/:name
//
// 没有单独的「列出全部闸」接口：插件管理页的 /platform/plugins 已经在每个插件上
// 带 blocked，再开一个列表接口只会多一份需要对齐的口径。
//
// blocked=false 走删行而不是 SetGate(false)：闸表只保留「确实被拦」的记录，
// 否则排查时看到一行 false 会以为「有人动过这个闸」。
func SetPluginGate(c *gin.Context) {
	name, ok := mustPlugin(c)
	if !ok {
		return
	}
	var body setPluginGateReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	blocked := body.Blocked != nil && *body.Blocked
	var err error
	if blocked {
		err = pluginpolicy.SetGate(name, true)
	} else {
		err = pluginpolicy.DeleteGate(name)
	}
	if err != nil {
		xhttp.ErrJSON(c, "Failed to save plugin gate: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"plugin": name, "blocked": blocked}, "")
}

// DeletePluginGate DELETE /platform/system/plugin-gates/:name
func DeletePluginGate(c *gin.Context) {
	name, ok := mustPlugin(c)
	if !ok {
		return
	}
	if err := pluginpolicy.DeleteGate(name); err != nil {
		xhttp.ErrJSON(c, "Failed to delete plugin gate: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"plugin": name}, "")
}

// writePluginPolicy 读当前行 → 应用请求里的字段 → 落库 → 回显生效值。
func writePluginPolicy(c *gin.Context, tenantID, name string) {
	var req setPolicyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	cur, has, err := pluginpolicy.Get(tenantID, name)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load plugin policy: "+err.Error(), nil, "")
		return
	}
	if !has {
		cur = pluginpolicy.Default()
	}
	if req.Allowed != nil {
		cur.Allowed = *req.Allowed
	}

	if err := pluginpolicy.Set(tenantID, name, cur); err != nil {
		xhttp.ErrJSON(c, "Failed to save plugin policy: "+err.Error(), nil, "")
		return
	}

	eff, err := pluginpolicy.Resolve(tenantID, name)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to resolve plugin policy: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"plugin":    name,
		"row":       toPolicyJSON(cur),
		"effective": toPolicyJSON(eff),
	}, "")
}

// ===== 插件管理页（一次取全：清单 × 全局闸 × 各租户原始行 × 租户启用状态） =====

// pluginOverview 一个插件的全局视图。Blocked 是全局闸状态；
// 不做「生效值」计算 —— 生效值取决于看哪个租户，由前端按同一套规则现算。
type pluginOverview struct {
	Name            string                      `json:"name"`
	DisplayName     string                      `json:"display_name"`
	Description     string                      `json:"description"`
	Version         string                      `json:"version"`
	Implementations []plugininfo.Implementation `json:"implementations"`
	Blocked         bool                        `json:"blocked"`
}

// tenantBrief 矩阵的行头。
type tenantBrief struct {
	Schema      string `json:"schema"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

// ListPluginOverview GET /platform/plugins
//
// 回四块：
//
//	plugins   磁盘清单 + 全局闸状态（blocked）
//	tenants   租户列表（矩阵行头）
//	policies  各租户的**原始行**（缺 = 没设过 = 缺省开放）
//	enabled   各租户实际启用了哪些（{schema}.plugins.is_active）
//
// 一次取全的理由：矩阵要同时看「平台给没给」和「租户开没开」，
// 否则前端得按租户逐个请求，页面上会看到一堆先后到达的局部状态。
func ListPluginOverview(c *gin.Context) {
	tools := plugininfo.Tools()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	gates, err := pluginpolicy.Gates()
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load plugin gates: "+err.Error(), nil, "")
		return
	}

	plugins := make([]pluginOverview, 0, len(tools))
	for _, t := range tools {
		plugins = append(plugins, pluginOverview{
			Name:            t.Name,
			DisplayName:     t.DisplayName,
			Description:     t.Description,
			Version:         t.Version,
			Implementations: t.Implementations,
			Blocked:         gates[t.Name],
		})
	}

	tenants, err := tenantsrepo.List("", "", 0, 0)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenants: "+err.Error(), nil, "")
		return
	}

	briefs := make([]tenantBrief, 0, len(tenants))
	policies := make(map[string]map[string]policyJSON, len(tenants))
	enabled := make(map[string]map[string]bool, len(tenants))
	for _, t := range tenants {
		briefs = append(briefs, tenantBrief{Schema: t.Schema, DisplayName: t.DisplayName, Status: t.Status})

		if rows, rerr := pluginpolicy.Rows(t.Schema); rerr == nil {
			if len(rows) > 0 {
				m := make(map[string]policyJSON, len(rows))
				for name, p := range rows {
					m[name] = toPolicyJSON(p)
				}
				policies[t.Schema] = m
			}
		} else {
			// 单租户读失败不拖垮整页：那一列按「继承」显示，日志里留证据
			xlog.Error("读取租户插件策略失败 schema=%s: %v", t.Schema, rerr)
		}

		// 租户侧配置读失败（schema 还没建、表不存在）按「未启用」展示
		if list, lerr := pluginCfgRepo.List(&auth.AuthInfo{Schema: t.Schema}); lerr == nil {
			m := make(map[string]bool, len(list))
			for _, p := range list {
				if p.Kind != pluginsrepo.KindTools { // 矩阵只画工具插件
					continue
				}
				if p.IsActive {
					m[p.Name] = true
				}
			}
			enabled[t.Schema] = m
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"plugins":  plugins,
		"tenants":  briefs,
		"policies": policies,
		"enabled":  enabled,
	}, "")
}

// mustPlugin 取路径里的插件名，并确认它确实在磁盘清单里（清单里没有的插件配了也没用）。
func mustPlugin(c *gin.Context) (string, bool) {
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		xhttp.ErrCode(c, "plugin.name_required", nil, "插件名为空", "")
		return "", false
	}
	if _, ok := plugininfo.Find(name); !ok {
		xhttp.ErrCode(c, "plugin.not_found", gin.H{"name": name},
			fmt.Sprintf("插件 %s 不在磁盘清单里（先放进 plugins/tools/%s/ 再刷新）", name, name), "")
		return "", false
	}
	return name, true
}
