// Package plugin 把 {root_dir}/plugins 下的 Python 插件接进 nexus：
// 发现（list）、调度（invoke），以及给工具授权体系用的 manifest 视图。
//
// 插件是磁盘上的代码，自己注册工具面与实现（见 plugins/_sdk/flame_plugin）：
//
//	plugins/<kind>/<tool>/tool.py            工具面（tool(...) 声明一次）
//	plugins/<kind>/<tool>/<impl>/plugin.py   实现（@impl 装饰 invoke）
//
// 设计定稿见 .codebuddy/plans/web-search-plugin-system_a7c41e02.md。
package plugin

// ConfigField 一项实现配置（管理端据此渲染表单、决定是否打码）。
type ConfigField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // string / int / bool
	Help     string `json:"help"`
	Secret   bool   `json:"secret"`   // true：管理端回显打码，写回时按实现合并（没传就保留旧值）
	Required bool   `json:"required"` // true：没配时调用直接报 code=config
	Default  any    `json:"default,omitempty"`
}

// Implementation 某个工具下的一种实现（tavily / bing_rss / searxng ...）。
type Implementation struct {
	Name         string        `json:"name"`
	DisplayName  string        `json:"display_name"`
	ConfigFields []ConfigField `json:"config_fields"`
}

// Manifest 一个工具的完整声明（工具面 + 实现清单）。
type Manifest struct {
	Kind            string           `json:"kind"` // 目前只有 tools
	Name            string           `json:"name"` // LLM 工具名，同时也是目录名与 ref subject
	DisplayName     string           `json:"display_name"`
	Description     string           `json:"description"`
	Version         string           `json:"version"`
	Parameters      map[string]any   `json:"parameters"` // JSON Schema object：交给模型的入参
	Implementations []Implementation `json:"implementations"`
}

// ImplementationOf 按名字查实现。
func (m *Manifest) ImplementationOf(name string) (Implementation, bool) {
	for _, im := range m.Implementations {
		if im.Name == name {
			return im, true
		}
	}
	return Implementation{}, false
}

// ScanError 扫描期某个工具/实现加载失败。只影响它自己 —— 坏插件不能拖垮别的插件。
type ScanError struct {
	Path  string `json:"path"`  // 相对插件根目录
	Error string `json:"error"` // 一句话原因（导入错误、缺 tool.py、没有实现……）
}

// ListResult 是 pluginhost list 的输出。
type ListResult struct {
	Tools  []Manifest  `json:"tools"`
	Errors []ScanError `json:"errors"`
}

// InvokePayload 是 pluginhost invoke 的 stdin。
type InvokePayload struct {
	Meta    map[string]any `json:"meta,omitempty"`    // 仅用于日志排障，插件不得据此做鉴权
	Config  map[string]any `json:"config,omitempty"`  // 选中实现那一份配置（密钥经 stdin 传入，不落磁盘）
	Args    map[string]any `json:"args,omitempty"`    // 模型给的入参
	Timeout int            `json:"timeout,omitempty"` // HTTP 超时（秒），与 nexus 侧进程超时同源
}

// InvokeResponse 是 pluginhost invoke 的 stdout（恒一行 JSON）。
type InvokeResponse struct {
	OK     bool         `json:"ok"`
	Result any          `json:"result"`
	Error  *InvokeError `json:"error"`
}

// InvokeError 插件主动报的错（错误码语义见 errors.go）。
type InvokeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
