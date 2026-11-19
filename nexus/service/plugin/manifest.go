// Package plugin wires the Python plugins under {root_dir}/plugins into nexus: discovery (list),
// dispatch (invoke), and the manifest view used by the tool grant system.
//
// A plugin is code on disk that registers its own tool surface and implementations (see
// plugins/_sdk/flame_plugin):
//
//	plugins/<kind>/<tool>/tool.py            the tool surface (tool(...) is declared once)
//	plugins/<kind>/<tool>/<impl>/plugin.py   an implementation (@impl decorates invoke)
//
// The design is recorded in .codebuddy/plans/web-search-plugin-system_a7c41e02.md.
package plugin

// ConfigField is one implementation config item (the admin console renders a form from it and
// decides whether to mask it).
type ConfigField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // string / int / bool
	Help     string `json:"help"`
	Secret   bool   `json:"secret"`   // true: the admin console masks it on read, and a write merges per implementation (omitted = keep the old value)
	Required bool   `json:"required"` // true: an unconfigured call fails with code=config
	Default  any    `json:"default,omitempty"`
}

// Implementation is one implementation of a tool (tavily / bing_rss / searxng ...).
type Implementation struct {
	Name         string        `json:"name"`
	DisplayName  string        `json:"display_name"`
	ConfigFields []ConfigField `json:"config_fields"`
}

// Manifest is a tool's full declaration (tool surface plus implementation list).
type Manifest struct {
	Kind            string           `json:"kind"` // tools only, for now
	Name            string           `json:"name"` // LLM tool name, also the directory name and the ref subject
	DisplayName     string           `json:"display_name"`
	Description     string           `json:"description"`
	Version         string           `json:"version"`
	Parameters      map[string]any   `json:"parameters"` // a JSON Schema object: the arguments handed to the model
	Implementations []Implementation `json:"implementations"`
}

// ImplementationOf looks an implementation up by name.
func (m *Manifest) ImplementationOf(name string) (Implementation, bool) {
	for _, im := range m.Implementations {
		if im.Name == name {
			return im, true
		}
	}
	return Implementation{}, false
}

// ScanError means one tool or implementation failed to load during the scan. It only affects itself —
// a broken plugin must not drag the others down.
type ScanError struct {
	Path  string `json:"path"`  // relative to the plugin root
	Error string `json:"error"` // a one-line reason (import error, missing tool.py, no implementation, ...)
}

// ListResult is the output of pluginhost list.
type ListResult struct {
	Tools  []Manifest  `json:"tools"`
	Errors []ScanError `json:"errors"`
}

// InvokePayload is the stdin of pluginhost invoke.
type InvokePayload struct {
	Meta    map[string]any `json:"meta,omitempty"`    // for log troubleshooting only; a plugin must not authorise on it
	Config  map[string]any `json:"config,omitempty"`  // the config of the selected implementation (secrets arrive on stdin and are never written to disk)
	Args    map[string]any `json:"args,omitempty"`    // the arguments the model supplied
	Timeout int            `json:"timeout,omitempty"` // HTTP timeout in seconds, sharing its source with the nexus-side process timeout
}

// InvokeResponse is the stdout of pluginhost invoke (always one line of JSON).
type InvokeResponse struct {
	OK     bool         `json:"ok"`
	Result any          `json:"result"`
	Error  *InvokeError `json:"error"`
}

// InvokeError is an error a plugin reports itself (the code semantics are in errors.go).
type InvokeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
