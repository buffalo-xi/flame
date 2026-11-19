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

// ===== Fakes: no database, no python process =====

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

// fakePluginsRepo keys rows by (kind, name): the same criterion as the real unique key, which also makes "same name, different kind,
// neither overwrites the other" hold in unit tests.
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
	// Platform-policy stand-in: allow everything, otherwise default-deny makes every catalog case fail
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
		t.Fatalf("unexpected parse result: %+v", p)
	}
	if p.IsGroup(BuiltinCategories()) {
		t.Error("a plugin tool only has a single-tool ref and must not be treated as a group")
	}
	if got := ToolNameOfRef("plugin:web_search"); got != "web_search" {
		t.Errorf("ToolNameOfRef = %q, want web_search (no prefix)", got)
	}
	// The implementation stays out of the grant surface: a third segment is always refused
	if _, err := ParseRef("plugin:web_search:search"); err == nil {
		t.Error("a plugin ref must not accept a third segment")
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
		t.Fatalf("tool count = %d, want 1", len(list))
	}
	info := list[0]
	if info.Name != "web_search" || info.Ref != "plugin:web_search" {
		t.Errorf("unexpected name/ref: %+v", info)
	}
	if !info.Available {
		t.Error("it is enabled but marked unavailable")
	}
	if info.SupervisorOnly {
		t.Error("a plugin tool must not be super-agent only")
	}
	if info.Parameters == nil {
		t.Fatal("parameters missing: an external tool has no tools.List() fallback, so the schema must come with it")
	}
	if _, ok := info.Parameters["properties"]; !ok {
		t.Errorf("parameters is not a valid schema: %+v", info.Parameters)
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
		t.Fatalf("a disabled plugin should still appear in the catalog (available=false), got %d entries", len(list))
	}
	if list[0].Available {
		t.Error("it is not enabled but marked available")
	}
}

func TestCatalogPluginToolUnknown(t *testing.T) {
	defer plugin.SetSource(nil)
	c := newPluginCatalog(true)

	list, err := c.ToolsOfRef("plugin:no_such_tool")
	if err != nil {
		t.Fatalf("an unknown plugin must not error, got %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("an unknown plugin expanded to %d tools, want 0", len(list))
	}
}

func TestCatalogWithoutPluginSource(t *testing.T) {
	plugin.SetSource(nil)
	c := NewCatalog("t0", nil, true)
	list, err := c.ToolsOfRef("plugin:web_search")
	if err != nil || len(list) != 0 {
		t.Fatalf("with no registry it should return empty quietly, got %v / %d", err, len(list))
	}
}
