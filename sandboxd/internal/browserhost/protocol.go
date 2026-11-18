// Package browserhost 是跑在隔离单元内部的浏览器宿主：替 sandboxd 长驻一个
// Chromium，并按「一行一个 JSON」的协议接受操作。
//
// 为什么是独立二进制而不是让 sandboxd 直接连 CDP：
// Chrome 跑在隔离单元（容器 / nsjail / 宿主进程）自己的 network namespace 里，
// 宿主上的 sandboxd 够不到它的调试端口；docker exec 也不支持传额外 fd，
// --remote-debugging-pipe 同样用不了。于是把「拉浏览器 + 说 CDP」这段整体放进
// 单元内部，sandboxd 只负责把字节搬进搬出（复用 /v1/interactive）。
//
// 为什么 stdout 只走协议、日志一律走 stderr：
// sandboxd 侧对长交互进程的 stdout 是一根纯字节管道（io.Copy → 当前连接），
// 任何非协议内容混进去都会让上层解析错位。
package browserhost

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sync"
)

// Request 一次操作请求。按 Op 使用不同字段。
type Request struct {
	ID int    `json:"id"`
	Op string `json:"op"`

	// 导航 / 打开
	URL    string `json:"url,omitempty"`
	NewTab bool   `json:"new_tab,omitempty"`
	Headed bool   `json:"headed,omitempty"` // 仅首次 open 生效

	// 元素操作（ref 来自 snapshot）
	Ref      string `json:"ref,omitempty"`
	Text     string `json:"text,omitempty"`
	Key      string `json:"key,omitempty"`
	Selector string `json:"selector,omitempty"`
	Submit   bool   `json:"submit,omitempty"`

	// 滚动
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`

	// 截图
	Path     string `json:"path,omitempty"`
	FullPage bool   `json:"full_page,omitempty"`

	// 标签页
	Target string `json:"target,omitempty"`
	Index  int    `json:"index,omitempty"`

	// 快照预算与超时
	MaxNodes  int `json:"max_nodes,omitempty"`
	MaxBytes  int `json:"max_bytes,omitempty"`
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// TabInfo 一个标签页的概要（tabs 操作用）。
type TabInfo struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Active bool  `json:"active,omitempty"`
}

// Response 一次操作的结果。
type Response struct {
	ID  int  `json:"id"`
	OK  bool `json:"ok"`
	// Code 机器可读的错误码（invalid_ref / not_open / timeout / headed_not_allowed ...），
	// Message 给人（与模型）看。分开是为了让上层能按码做处理而不必匹配文案。
	Code    string `json:"code,omitempty"`
	Error   string `json:"error,omitempty"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
	Nodes    int    `json:"nodes,omitempty"`
	Truncated bool  `json:"truncated,omitempty"`
	Text      string `json:"text,omitempty"`
	Path      string `json:"path,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	Tabs      []TabInfo `json:"tabs,omitempty"`
	// Changed 动作是否可能改变了页面。false 时 snapshot 往往可以跳过。
	Changed bool `json:"changed,omitempty"`
	// Headed 浏览器实际的显示方式。只在 open 时回填 —— 模型必须知道
	// 「我请求的有头到底生效了没有」，猜错的代价是它以为有人在看。
	Headed *bool `json:"headed,omitempty"`
}

// opError 带错误码的操作失败。
type opError struct {
	code string
	msg  string
}

func (e *opError) Error() string { return e.msg }

func fail(code, format string, args ...any) *opError {
	return &opError{code: code, msg: fmt.Sprintf(format, args...)}
}

// lineWriter 串行化写 stdout：一行一个 JSON。浏览器宿主是单线程处理的，
// 这里加锁只是为了将来引入异步事件时不出错位。
type lineWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func (l *lineWriter) write(r *Response) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(b); err != nil {
		return err
	}
	if err := l.w.WriteByte('\n'); err != nil {
		return err
	}
	return l.w.Flush()
}
