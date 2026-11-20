// Package plugininfo is the read-only view of the on-disk plugin manifests (a process-level singleton).
//
// Why this layer exists: the real source of the plugin manifests is service/plugin (the Registry scans the disk), but the
// platform console (nexus/platform) may not import nexus/service/** under the boundary rules.
// Only the minimal shape "name + display name + description + version" lives here, injected by service/plugin at startup;
// the platform side uses it to list "which plugins can be opened for a tenant", without touching implementation details
// or Python.
package plugininfo

import "sync/atomic"

// Implementation is one implementation of a plugin (tavily / bing_rss / searxng ...).
// The platform page only needs to show "which ones exist" — the configuration keys belong to the tenant side and do not
// come here.
type Implementation struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// Tool is one discovered plugin tool (one-to-one with the manifest in tool.py; it carries no implementation
// configuration and no keys).
type Tool struct {
	Name            string           `json:"name"`
	DisplayName     string           `json:"display_name"`
	Description     string           `json:"description"`
	Version         string           `json:"version"`
	Kind            string           `json:"kind"`
	Implementations []Implementation `json:"implementations"`
}

// Source is the plugin manifest source (production uses service/plugin's adapter; a unit test may inject a fake).
type Source interface {
	Tools() []Tool
}

// box wraps the value: atomic.Value requires every stored value to have the same concrete type.
type box struct{ s Source }

var defaultSource atomic.Value

// SetSource injects the manifest source (called at service startup; nil clears it).
func SetSource(s Source) { defaultSource.Store(box{s: s}) }

// Current returns the manifest source; nil when nothing was injected.
func Current() Source {
	if v, ok := defaultSource.Load().(box); ok {
		return v.s
	}
	return nil
}

// Tools returns the currently discovered plugin tools (an empty slice when nothing was injected — not nil, so the
// frontend can iterate it directly).
func Tools() []Tool {
	s := Current()
	if s == nil {
		return []Tool{}
	}
	return s.Tools()
}

// Find looks one plugin up by name (case sensitive, the same rule as tool names).
func Find(name string) (Tool, bool) {
	for _, t := range Tools() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
