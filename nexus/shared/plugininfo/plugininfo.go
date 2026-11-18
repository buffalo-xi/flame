// Package plugininfo 磁盘插件清单的只读视图（进程级单例）。
//
// 为什么要有这一层：插件清单的真实来源是 service/plugin（Registry 扫描磁盘），
// 但平台管理台（nexus/platform）的边界规则禁止 import nexus/service/**。
// 这里只放「名字 + 展示名 + 描述 + 版本」这种最小结构，由 service/plugin 在启动时注入；
// 平台侧据此列出「可以给租户开通哪些插件」，不碰实现细节、不碰 Python。
package plugininfo

import "sync/atomic"

// Implementation 插件的一种实现（tavily / bing_rss / searxng ...）。
// 平台页面只需展示「有哪几种」，配置项与 key 属于租户侧，不进这里。
type Implementation struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// Tool 一个已发现的插件工具（与 tool.py 的 manifest 一一对应，不含实现配置与 key）。
type Tool struct {
	Name            string           `json:"name"`
	DisplayName     string           `json:"display_name"`
	Description     string           `json:"description"`
	Version         string           `json:"version"`
	Kind            string           `json:"kind"`
	Implementations []Implementation `json:"implementations"`
}

// Source 插件清单来源（生产是 service/plugin 的适配器；单测可注入假实现）。
type Source interface {
	Tools() []Tool
}

// box 包一层：atomic.Value 要求存入的具体类型始终一致。
type box struct{ s Source }

var defaultSource atomic.Value

// SetSource 注入清单来源（服务启动时调用；传 nil 表示清空）。
func SetSource(s Source) { defaultSource.Store(box{s: s}) }

// Current 取清单来源；未注入时为 nil。
func Current() Source {
	if v, ok := defaultSource.Load().(box); ok {
		return v.s
	}
	return nil
}

// Tools 当前已发现的插件工具（未注入时返回空切片，不是 nil，前端可直接遍历）。
func Tools() []Tool {
	s := Current()
	if s == nil {
		return []Tool{}
	}
	return s.Tools()
}

// Find 按名字查一个插件（大小写敏感，与工具名口径一致）。
func Find(name string) (Tool, bool) {
	for _, t := range Tools() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
