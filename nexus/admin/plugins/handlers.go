// Package plugins holds the admin "plugins" endpoints: the discovered disk Python plugins plus
// tenant-level enablement and configuration.
//
// Semantics: **there is no "create plugin" endpoint**. A plugin is code placed on disk
// (self-registering); the admin side only "sees it / enables it / picks an implementation /
// fills in config / tries it once".
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

// secretMask is the masking prefix. The frontend sends it back verbatim - the backend reads it
// as "this secret was not changed" and keeps the old value (the real key is never echoed to the
// browser, and the masking string never wipes the key either).
const secretMask = "••••"

// PluginView is one admin row = disk manifest (tool surface + implementation list) + this
// tenant's state + platform policy.
type PluginView struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name"`
	Description string         `json:"description"`
	Version     string         `json:"version"`
	Kind        string         `json:"kind"`
	Parameters  map[string]any `json:"parameters"`

	Implementations []plugin.Implementation `json:"implementations"`

	Configured bool                      `json:"configured"` // whether this tenant has ever saved a config
	IsActive   bool                      `json:"is_active"`
	Impl       string                    `json:"impl"`
	Fallbacks  []string                  `json:"fallbacks"`
	Configs    map[string]map[string]any `json:"configs"` // secret fields are masked

	// Platform policy (read-only): configured by the platform admin under "tenant -> plugins".
	// This endpoint only echoes it and UpdatePlugin does not accept the field - availability is a
	// cost and safety guardrail, not something a tenant may switch on for itself.
	//
	// Blocked is the global gate: kept separate from "not enabled for this tenant" so the tenant
	// can tell who to ask.
	Allowed bool `json:"allowed"`
	Blocked bool `json:"blocked"`
}

// ListPlugins lists the plugins discovered on disk plus this tenant's state and the scan errors.
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the plugin config: "+err.Error(), "")
		return
	}
	byName := make(map[string]*models.Plugin, len(rows))
	for i := range rows {
		// This page lists tools only: restricting the index to tools rows keeps a future
		// same-named sources row from displacing the tool config (List fetches every kind).
		if rows[i].Kind != pluginsrepo.KindTools {
			continue
		}
		byName[rows[i].Name] = &rows[i]
	}

	// Platform policy is fetched in one go: resolving plugin by plugin would emit N queries, and
	// the number of plugins is unbounded.
	//
	// A read failure (usually platform.tenant_plugins not created yet) must **not** fall back to
	// the default: the default is open, while "cannot read" does not mean "nobody disabled it".
	// Degrade to "all disabled" with a policy_error note on the page - the tenant-side plugin
	// config still renders, but a missing platform table never turns everything on.
	policies, perr := pluginpolicy.List(authInfo.Schema)
	if perr != nil {
		xlog.Error("failed to read plugin policies schema=%s (showing everything as disabled): %v", authInfo.Schema, perr)
		policies = map[string]pluginpolicy.Policy{}
	}

	out := make([]PluginView, 0, len(snap.Tools))
	for _, m := range snap.Tools {
		pol := policyOf(policies, m.Name)
		if perr != nil {
			pol = pluginpolicy.Policy{Allowed: false} // conservative: unreadable means disabled
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

// updateReq updates a plugin config. configs is keyed per implementation; a secret field sent
// back as the masking string means "unchanged".
type updateReq struct {
	Impl      string                    `json:"impl"`
	Fallbacks []string                  `json:"fallbacks"`
	Configs   map[string]map[string]any `json:"configs"`
	IsActive  *bool                     `json:"is_active"`
}

// UpdatePlugin saves a plugin's enabled state / main implementation / fallback chain / config.
func UpdatePlugin(c *gin.Context) {
	authInfo, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	m, found := lookupManifest(name)
	if !found {
		xhttp.ErrCode(c, "plugin.not_found", gin.H{"name": name}, fmt.Sprintf("plugin %s is not in the disk manifest (put it in plugins/tools/%s/ and refresh)", name, name), "")
		return
	}

	var req updateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "malformed request body", "")
		return
	}
	if msg := validateSelection(m, req.Impl, req.Fallbacks, req.Configs); msg != "" {
		xhttp.ErrCode(c, "plugin.invalid_selection", nil, msg, "")
		return
	}

	old := &models.Plugin{Kind: m.Kind, Name: m.Name}
	if p, err := repo.Get(authInfo, m.Kind, m.Name); err == nil {
		old = p
	} else if !errors.Is(err, sql.ErrNoRows) {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the plugin config: "+err.Error(), "")
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
	// Enabling a plugin with no implementation picked would create a config that always fails; block it before saving
	if active && impl == "" {
		xhttp.ErrCode(c, "plugin.main_impl_required", nil, "pick a main implementation before enabling", "")
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to save: "+err.Error(), "")
		return
	}

	saved, err := repo.Get(authInfo, m.Kind, m.Name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read back after saving: "+err.Error(), "")
		return
	}
	// The save already succeeded, so a policy read failure must not report this write as failed;
	// but it must not echo as "open" either - same conservative rule as ListPlugins (unreadable
	// means disabled).
	pol, perr := pluginpolicy.Resolve(authInfo.Schema, m.Name)
	if perr != nil {
		pol = pluginpolicy.Policy{Allowed: false}
	}
	xhttp.OkJSON(c, "Success", buildView(m, saved, pol), "")
}

// testReq is "try it once": it may carry unsaved config (a freshly typed key need not be saved
// before testing).
type testReq struct {
	Impl    string         `json:"impl"`
	Query   string         `json:"query"`
	Configs map[string]any `json:"configs"`
}

// TestPlugin runs one real retrieval against the backend and returns the first 3 results or the
// raw error.
//
// It deliberately goes through Host rather than Runner: Runner requires "saved and enabled",
// while this exists precisely to validate a config before saving. Timeout and error
// classification still match production (same host process).
func TestPlugin(c *gin.Context) {
	authInfo, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	m, found := lookupManifest(name)
	if !found {
		xhttp.ErrCode(c, "plugin.not_found", gin.H{"name": name}, fmt.Sprintf("plugin %s is not in the disk manifest", name), "")
		return
	}

	var req testReq
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "malformed request body", "")
		return
	}
	impl := strings.TrimSpace(req.Impl)
	if _, ok := m.ImplementationOf(impl); !ok {
		xhttp.ErrCode(c, "plugin.invalid_impl", gin.H{"impl": impl}, fmt.Sprintf("implementation %q is not in the plugin manifest", impl), "")
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		query = "flame plugin test"
	}

	// "Try it once" also hits the upstream for real, so it obeys the platform policy too: not
	// enabled is rejected outright. is_active is not required here - the point of this endpoint is
	// to check reachability before saving.
	pol, err := pluginpolicy.Resolve(authInfo.Schema, m.Name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the plugin grant: "+err.Error(), "")
		return
	}
	if !pol.Allowed {
		if pol.Blocked {
			xhttp.ErrCode(c, "plugin.globally_disabled", gin.H{"name": m.Name}, fmt.Sprintf("plugin %s is globally disabled by the platform", m.Name), "")
			return
		}
		xhttp.ErrCode(c, "plugin.not_enabled_for_tenant", gin.H{"name": m.Name}, fmt.Sprintf("plugin %s is not enabled for this tenant (ask a platform admin to enable it)", m.Name), "")
		return
	}

	// Start from the saved config and override with the request (skipping masking strings)
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
		cfgInfo.DirOrDefault(config.Cfg.Storage.RootDir),
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

	// Return only the first 3: a connection test is for humans, not for the model
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

// RefreshPlugins rescans the plugin directory immediately (without waiting for the background
// refresh cycle).
func RefreshPlugins(c *gin.Context) {
	reg := plugin.Current()
	if reg == nil {
		xhttp.ErrCode(c, "plugin.registry_not_initialized", nil, "the plugin registry is not initialised (check plugin.dir and the startup log)", "")
		return
	}
	if err := reg.Refresh(c.Request.Context(), true); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "refresh failed: "+err.Error(), "")
		return
	}
	snap := reg.Snapshot()
	xhttp.OkJSON(c, "Success", gin.H{
		"tools": len(snap.Tools), "errors": snap.Errors, "refreshed_at": snap.At,
	}, "")
}

// ===== internal helpers =====

func mustAuth(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
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

// buildView merges the disk manifest with the tenant config, masks secrets on the way and echoes
// the platform policy.
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
	// The policy is echoed even when p is nil (this tenant never configured it): a plugin the
	// platform has not enabled should be visible at a glance in the UI
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

// policyOf picks one plugin's policy out of the "fetched in one go" result, defaulting to open.
func policyOf(all map[string]pluginpolicy.Policy, name string) pluginpolicy.Policy {
	if p, ok := all[name]; ok {
		return p
	}
	return pluginpolicy.Default()
}

// maskConfigs replaces every implementation's secret field with "mask plus the last 4 chars".
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

// validateSelection checks that the main implementation / fallback chain / config keys all exist
// in the manifest; an empty return means it passed.
func validateSelection(m plugin.Manifest, impl string, fallbacks []string, cfgs map[string]map[string]any) string {
	if impl != "" {
		if _, ok := m.ImplementationOf(impl); !ok {
			return fmt.Sprintf("implementation %q is not in the plugin manifest", impl)
		}
	}
	seen := map[string]bool{}
	for _, fb := range fallbacks {
		fb = strings.TrimSpace(fb)
		if fb == "" {
			continue
		}
		if _, ok := m.ImplementationOf(fb); !ok {
			return fmt.Sprintf("fallback implementation %q is not in the plugin manifest", fb)
		}
		if seen[fb] {
			return fmt.Sprintf("fallback implementation %q is duplicated", fb)
		}
		seen[fb] = true
	}
	for implName := range cfgs {
		if _, ok := m.ImplementationOf(implName); !ok {
			return fmt.Sprintf("the config references an unknown implementation %q", implName)
		}
	}
	return ""
}

// mergeConfigs merges per implementation: only keys present in the request are overwritten, and a
// masking string (unchanged) keeps the old value.
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
				continue // sent back masked = unchanged
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
