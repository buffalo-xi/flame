package toolgrant

import (
	"database/sql"
	"strings"
	"testing"

	"flame/nexus/service/plugin"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/pluginpolicy"
)

// ===== 假实现：不依赖 DB、不起 python =====

type fakePluginSource struct{ tools []plugin.Manifest }

func (f fakePluginSource) Snapshot() plugin.Snapshot {
	return plugin.Snapshot{Tools: f.tools}
}

func (f fakePluginSource) Get(name string) (plugin.Manifest, bool) {
	for _, m := range f.tools {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return plugin.Manifest{}, false
}

// fakePluginsRepo 按 (kind, name) 存：与真实唯一键同口径，
// 顺带让「同名不同 kind 不互相覆盖」这条性质在单测里成立。
type fakePluginsRepo struct{ rows map[string]*models.Plugin }

func pluginRowKey(kind, name string) string { return kind + "/" + name }

func (f fakePluginsRepo) Get(_ *auth.AuthInfo, kind, name string) (*models.Plugin, error) {
	if p, ok := f.rows[pluginRowKey(kind, name)]; ok {
		return p, nil
	}
	return nil, sql.ErrNoRows
}

func (f fakePluginsRepo) List(_ *auth.AuthInfo) ([]models.Plugin, error) {
	out := make([]models.Plugin, 0, len(f.rows))
	for _, p := range f.rows {
		out = append(out, *p)
	}
	return out, nil
}

func (f fakePluginsRepo) Upsert(_ *auth.AuthInfo, p *models.Plugin) error {
	f.rows[pluginRowKey(p.Kind, p.Name)] = p
	return nil
}

func (f fakePluginsRepo) SetActive(_ *auth.AuthInfo, kind, name string, active bool) error {
	p, ok := f.rows[pluginRowKey(kind, name)]
	if !ok {
		return sql.ErrNoRows
	}
	p.IsActive = active
	return nil
}

func (f fakePluginsRepo) Delete(_ *auth.AuthInfo, kind, name string) error {
	key := pluginRowKey(kind, name)
	if _, ok := f.rows[key]; !ok {
		return sql.ErrNoRows
	}
	delete(f.rows, key)
	return nil
}

func (f fakePluginsRepo) TouchUsed(_ *auth.AuthInfo, _, _ string) error   { return nil }
func (f fakePluginsRepo) SetError(_ *auth.AuthInfo, _, _, _ string) error { return nil }

func webSearchManifest() plugin.Manifest {
	return plugin.Manifest{
		Kind:        "tools",
		Name:        "web_search",
		DisplayName: "网络搜索",
		Description: "搜索网络",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []any{"query"},
		},
		Implementations: []plugin.Implementation{
			{Name: "searxng", DisplayName: "SearXNG"},
			{Name: "tavily", DisplayName: "Tavily"},
		},
	}
}

func newPluginCatalog(active bool) *Catalog {
	plugin.SetSource(fakePluginSource{tools: []plugin.Manifest{webSearchManifest()}})
	c := NewCatalog("t0", nil, true)
	c.plugins = fakePluginsRepo{rows: map[string]*models.Plugin{
		pluginRowKey("tools", "web_search"): {Kind: "tools", Name: "web_search", Impl: "searxng", IsActive: active},
	}}
	// 平台策略替身：直接给「已开通 + 不限」，否则缺省拒绝会让列目录用例全挂
	c.policies = func(string) (map[string]pluginpolicy.Policy, error) {
		return map[string]pluginpolicy.Policy{"web_search": {Allowed: true}}, nil
	}
	return c
}

func TestPluginRefParsing(t *testing.T) {
	p, err := ParseRef("plugin:web_search")
	if err != nil {
		t.Fatalf("ParseRef = %v", err)
	}
	if p.Scope != ScopePlugin || p.Subject != "web_search" || p.Member != "" {
		t.Fatalf("解析结果不对：%+v", p)
	}
	if p.IsGroup(BuiltinCategories()) {
		t.Error("插件工具只有单工具 ref，不该被当成组")
	}
	if got := ToolNameOfRef("plugin:web_search"); got != "web_search" {
		t.Errorf("ToolNameOfRef = %q，期望 web_search（不加前缀）", got)
	}
	// 实现不进授权面：第三段一律拒绝
	if _, err := ParseRef("plugin:web_search:search"); err == nil {
		t.Error("plugin ref 不该接受第三段")
	}
}

func TestCatalogPluginToolEnabled(t *testing.T) {
	defer plugin.SetSource(nil)
	c := newPluginCatalog(true)

	list, err := c.ToolsOfRef("plugin:web_search")
	if err != nil {
		t.Fatalf("ToolsOfRef = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("工具数 = %d，期望 1", len(list))
	}
	info := list[0]
	if info.Name != "web_search" || info.Ref != "plugin:web_search" {
		t.Errorf("名字/ref 不对：%+v", info)
	}
	if !info.Available {
		t.Error("已启用却标记为不可用")
	}
	if info.SupervisorOnly {
		t.Error("插件工具不该是 超级智能体专属")
	}
	if info.Parameters == nil {
		t.Fatal("缺 parameters：外部工具没有 tools.List() 兜底，schema 必须自带")
	}
	if _, ok := info.Parameters["properties"]; !ok {
		t.Errorf("parameters 不是合法 schema：%+v", info.Parameters)
	}
	if info.Source != "plugin:web_search" {
		t.Errorf("Source = %q", info.Source)
	}
}

func TestCatalogPluginToolDisabled(t *testing.T) {
	defer plugin.SetSource(nil)
	c := newPluginCatalog(false)

	list, err := c.ToolsOfRef("plugin:web_search")
	if err != nil {
		t.Fatalf("ToolsOfRef = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("停用的插件也该出现在目录里（可用=false），实际 %d 条", len(list))
	}
	if list[0].Available {
		t.Error("未启用却标记为可用")
	}
}

func TestCatalogPluginToolUnknown(t *testing.T) {
	defer plugin.SetSource(nil)
	c := newPluginCatalog(true)

	list, err := c.ToolsOfRef("plugin:no_such_tool")
	if err != nil {
		t.Fatalf("未知插件不该报错，实际 %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("未知插件展开了 %d 个工具，期望 0", len(list))
	}
}

func TestCatalogWithoutPluginSource(t *testing.T) {
	plugin.SetSource(nil)
	c := NewCatalog("t0", nil, true)
	list, err := c.ToolsOfRef("plugin:web_search")
	if err != nil || len(list) != 0 {
		t.Fatalf("没有注册表时应当安静返回空，实际 %v / %d", err, len(list))
	}
}
