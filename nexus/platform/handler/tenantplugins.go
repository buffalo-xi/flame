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

// Plugin grants: reading and writing platform.tenant_plugins (tenant rows) plus platform.plugin_gates (the global gate).
//
// Three values must be kept apart:
//
//	row      this tenant's raw row (null = never set = not enabled) - the edit form binds to it
//	blocked  whether the global gate stops it (default: let through) - "may it be on at all", overriding a tenant's
//	         explicit enable
//	enabled  whether the tenant side accepts this plugin ({schema}.plugins.is_active) - display only
//
// Effective = (tenant row ?? not enabled) AND NOT blocked. On 2026-10-21 the "system default row" (tenant_id = '') was
// removed: it only changed the default and is a different thing from the emergency stop, and keeping both in one table
// left administrators unable to tell "I turned it on" from "it was never there".
//
// The DDL lives in nexus/platform/sql/tenant_plugins.sql and plugin_gates.sql; the resolution logic in shared/pluginpolicy.

// pluginCfgRepo reads tenant-side plugin config (name / is_active / impl only, never the keys).
var pluginCfgRepo = pluginsrepo.NewRepository()

// policyJSON is the external shape of a policy (one-to-one with shared/pluginpolicy.Policy).
type policyJSON struct {
	Allowed bool `json:"allowed"`
}

func toPolicyJSON(p pluginpolicy.Policy) policyJSON {
	return policyJSON{Allowed: p.Allowed}
}

// tenantPluginView is one row of the platform "tenant -> plugins" list.
type tenantPluginView struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Version     string `json:"version"`

	// effective value (this tenant's row ?? open), already multiplied by the global gate
	policyJSON
	Blocked bool `json:"blocked"` // whether the global gate stops it (false = no, including "no gate row")

	Row *policyJSON `json:"row"` // this tenant's raw row; null = never set (= open by default)

	// Tenant-side state: is it on, and which implementation is used (quotas do not depend on the implementation, but
	// they are more useful side by side)
	Enabled bool   `json:"enabled"`
	Impl    string `json:"impl"`
}

// ListTenantPlugins GET /platform/tenants/:schema/plugins
// Plugins discovered on disk x this tenant's policy x the tenant-side enabled state.
func ListTenantPlugins(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}

	rows, err := pluginpolicy.Rows(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant plugin policy: "+err.Error(), "")
		return
	}
	gates, err := pluginpolicy.Gates()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load plugin gates: "+err.Error(), "")
		return
	}

	// A failed read of the tenant-side config (schema not built yet, table missing) must not affect platform grants: show
	// it as "not enabled".
	enabled := map[string]bool{}
	impl := map[string]string{}
	if list, lerr := pluginCfgRepo.List(&auth.AuthInfo{Schema: schema}); lerr == nil {
		for _, p := range list {
			if p.Kind != pluginsrepo.KindTools { // List spans kinds; only tool plugins belong here
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
			eff.Allowed = false // the hard gate multiplied in last; the raw row is kept as it is
			v.Blocked = true
		}
		v.policyJSON = toPolicyJSON(eff)
		out = append(out, v)
	}

	xhttp.OkJSON(c, "Success", gin.H{"plugins": out}, "")
}

// setPolicyReq overwrites this tenant's row; an omitted field keeps the current value (read first, then modify, so nothing
// is cleared by accident).
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
// Deleting this tenant's row returns to the default (not enabled). The system default row is gone, so "clear" really means
// "off".
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
		xhttp.ErrCode(c, "common.internal", nil, "Failed to delete tenant plugin policy: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"schema": schema, "plugin": name, "row": nil}, "")
}

// setPluginGateReq writes the gate; an omitted blocked counts as false (= do not stop).
//
// It is not called setGateReq: this package already has a same-named type for the capability gate, and the clash would not
// compile.
type setPluginGateReq struct {
	Blocked *bool `json:"blocked"`
}

// SetPluginGate PUT /platform/system/plugin-gates/:name
//
// There is no separate "list all gates" endpoint: /platform/plugins already carries blocked on every plugin, and another
// list endpoint would only add a second shape that has to be kept in sync.
//
// blocked=false deletes the row instead of SetGate(false): the gate table keeps only "actually blocked" records, otherwise
// someone troubleshooting would see a false row and assume "somebody touched this gate".
func SetPluginGate(c *gin.Context) {
	name, ok := mustPlugin(c)
	if !ok {
		return
	}
	var body setPluginGateReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
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
		xhttp.ErrCode(c, "common.internal", nil, "Failed to save plugin gate: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "Failed to delete plugin gate: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"plugin": name}, "")
}

// writePluginPolicy reads the current row -> applies the request fields -> writes -> echoes the effective value.
func writePluginPolicy(c *gin.Context, tenantID, name string) {
	var req setPolicyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	cur, has, err := pluginpolicy.Get(tenantID, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load plugin policy: "+err.Error(), "")
		return
	}
	if !has {
		cur = pluginpolicy.Default()
	}
	if req.Allowed != nil {
		cur.Allowed = *req.Allowed
	}

	if err := pluginpolicy.Set(tenantID, name, cur); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to save plugin policy: "+err.Error(), "")
		return
	}

	eff, err := pluginpolicy.Resolve(tenantID, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to resolve plugin policy: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"plugin":    name,
		"row":       toPolicyJSON(cur),
		"effective": toPolicyJSON(eff),
	}, "")
}

// ===== Plugin management page (one fetch: manifest x global gates x each tenant's raw row x tenant enabled state) =====

// pluginOverview is the global view of one plugin. Blocked is the global gate state;
// no "effective value" is computed - that depends on which tenant you look at, and the frontend derives it from the same
// rules.
type pluginOverview struct {
	Name            string                      `json:"name"`
	DisplayName     string                      `json:"display_name"`
	Description     string                      `json:"description"`
	Version         string                      `json:"version"`
	Implementations []plugininfo.Implementation `json:"implementations"`
	Blocked         bool                        `json:"blocked"`
}

// tenantBrief is the row header of the matrix.
type tenantBrief struct {
	Schema      string `json:"schema"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

// ListPluginOverview GET /platform/plugins
//
// Returns four blocks:
//
//	plugins   disk manifest + global gate state (blocked)
//	tenants   tenant list (matrix row header)
//	policies  each tenant's **raw row** (absent = never set = open by default)
//	enabled   what each tenant actually enabled ({schema}.plugins.is_active)
//
// Everything in one fetch because the matrix shows "did the platform grant it" and "did the tenant turn it on" side by
// side; otherwise the frontend would have to request tenant by tenant and the page would fill with partial states arriving
// at different times.
func ListPluginOverview(c *gin.Context) {
	tools := plugininfo.Tools()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	gates, err := pluginpolicy.Gates()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load plugin gates: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenants: "+err.Error(), "")
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
			// One tenant's failed read must not bring down the page: that column shows "inherited" and the log keeps the evidence
			xlog.Error("failed to read tenant plugin policy schema=%s: %v", t.Schema, rerr)
		}

		// A failed read of the tenant-side config (schema not built yet, table missing) shows as "not enabled"
		if list, lerr := pluginCfgRepo.List(&auth.AuthInfo{Schema: t.Schema}); lerr == nil {
			m := make(map[string]bool, len(list))
			for _, p := range list {
				if p.Kind != pluginsrepo.KindTools { // the matrix only draws tool plugins
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

// mustPlugin reads the plugin name from the path and confirms it really is in the disk manifest (configuring a plugin that
// is not there does nothing).
func mustPlugin(c *gin.Context) (string, bool) {
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		xhttp.ErrCode(c, "plugin.name_required", nil, "the plugin name cannot be empty", "")
		return "", false
	}
	if _, ok := plugininfo.Find(name); !ok {
		xhttp.ErrCode(c, "plugin.not_found", gin.H{"name": name},
			fmt.Sprintf("plugin %s is not in the disk manifest (put it in plugins/tools/%s/ and refresh)", name, name), "")
		return "", false
	}
	return name, true
}
