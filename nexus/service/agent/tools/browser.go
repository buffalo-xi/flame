package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"flame/nexus/shared/sandboxinfo"
	"flame/tools/sandbox"
)

// browserTool 沙箱内浏览器（CDP / Chromium）。
//
// 与 shell 同为「session 级常驻进程」：同一个 session / run 内的多次调用复用
// 同一个浏览器实例（cookie、登录态、标签页都保留），进程归 sandboxd 的
// 长交互注册表管，工具侧只负责把一次操作翻译成一行 JSON。
//
// 观测通道是**无障碍树快照**，不是截图：工具结果只能回填文本
// （见 agent_impl.go 的 marshalToolResult），截图落盘给人看，
// 模型"看"页面必须靠 snapshot 返回的 role / name / ref。
type browserTool struct{}

const (
	browserToolID = "browser"
	// browserArgvPlaceholder 只是满足协议「首次 open 必须带 argv」的要求 ——
	// sandboxd 对 tool_id=browser 会用自己配置的 browserhost 路径覆盖它
	// （沙箱内路径只有 sandboxd 知道，与 langs / codehost 同一个理由）。
	browserArgvPlaceholder = "browserhost"
)

// browserReqSeq 请求 id 单调递增：响应按 id 匹配，用来丢弃上次超时后迟到的回包。
var browserReqSeq atomic.Int64

func (t *browserTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "browser",
		Category: CategoryBrowser,
		Description: "Operate web pages in a browser inside the sandbox (Chromium / CDP). " +
			"Every action returns the page title, URL and an accessibility-tree snapshot such as `- e4 [button] \"Sign in\"`; " +
			"pass refs from that snapshot to click / type (e.g. e4) instead of guessing CSS selectors. " +
			"The snapshot only contains readable text and actionable elements; all refs go stale after navigation, so take a new snapshot. " +
			"Screenshots are written into the workspace and the path is returned (screenshots are for humans — you cannot read images, so inspect pages with snapshot / text). " +
			"The browser is a long-lived, heavyweight process (Chromium): start it only when rendering or interaction is genuinely required (logged-in state, JS-rendered pages, click flows); " +
			"if exec (curl / requests) can fetch the content, do not start it. When you are done, close it with action=close — otherwise the process lives on with the session.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []any{
						"open", "goto", "snapshot", "click", "type", "press",
						"scroll", "wait", "text", "screenshot", "tabs", "switch", "back", "close",
					},
					"description": "open=open a page (launches the browser on first use); snapshot=capture the current page; " +
						"click/type/press=act by ref; wait=wait for text or a selector to appear; " +
						"text=plain text of the page; tabs/switch=multiple tabs; back=go back; close=close the whole browser session",
				},
				"url":       map[string]any{"type": "string", "description": "Target address for open / goto (http/https)"},
				"ref":       map[string]any{"type": "string", "description": "Element ref returned by snapshot (e.g. e4)"},
				"text":      map[string]any{"type": "string", "description": "Text to type for type; text fragment to wait for on wait"},
				"key":       map[string]any{"type": "string", "description": "Key for press: Enter / Tab / Escape / ArrowDown, etc."},
				"selector":  map[string]any{"type": "string", "description": "CSS selector for wait (mutually exclusive with text)"},
				"submit":    map[string]any{"type": "boolean", "description": "Whether to press Enter after type"},
				"full_page": map[string]any{"type": "boolean", "description": "Whether screenshot captures the whole page (default: viewport only)"},
				"new_tab":   map[string]any{"type": "boolean", "description": "Whether open uses a new tab"},
				"path":      map[string]any{"type": "string", "description": "Save path for screenshot (relative to the workspace; default browser/shot-<time>.png)"},
				"target":    map[string]any{"type": "string", "description": "Target tab id for switch (from tabs)"},
				"index":     map[string]any{"type": "number", "description": "Target tab index for switch (1-based)"},
				"x":         map[string]any{"type": "number", "description": "Horizontal pixels for scroll (positive = right)"},
				"y":         map[string]any{"type": "number", "description": "Vertical pixels for scroll (positive = down)"},
				"max_bytes": map[string]any{"type": "number", "description": "Output cap for snapshot / text (default: 8KB for snapshot, 32KB for text)"},
				"max_nodes": map[string]any{"type": "number", "description": "Node cap for snapshot (default 400)"},
				"timeout":   map[string]any{"type": "number", "description": "Timeout in seconds for this action (default 30, still bounded by Context.Timeout)"},
				"headed":    map[string]any{"type": "boolean", "description": "Whether open shows a visible window. Only available when the deployment allows it (mode=none + allow_headed); cannot be changed once the browser exists"},
			},
			"required": []any{"action"},
		},
	}
}

func (t *browserTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if err := ensureSandboxReady("browser"); err != nil {
		return nil, err
	}
	if err := ensureBrowserReady(); err != nil {
		return nil, err
	}
	client := CurrentInteractive()
	if client == nil {
		return nil, errors.New("browser disabled: interactive sandbox not initialized")
	}

	action := strings.TrimSpace(argString(args, "action"))
	if action == "" {
		return nil, errors.New("action is required")
	}

	// 会话身份与 shell 完全一致：SessionID 优先，RunID 兜底（临时进程）。
	sess := sandbox.InteractiveSession{Key: c.SessionID}
	if sess.Key == "" {
		sess.Key = c.RunID
		sess.Ephemeral = true
	}

	if action == "close" {
		// 显式关闭：sandboxd 会杀 browserhost（它的 Chrome 子进程随之收掉）。
		// 幂等 —— 浏览器没起过时也返回成功。
		if err := client.Kill(ctx, sess, browserToolID); err != nil {
			return nil, err
		}
		return map[string]any{"closed": true}, nil
	}

	// 有头是「创建浏览器时」的属性，不能在运行中切换。这里提前挡住，
	// 免得模型以为窗口开着、实际一直是无头（反过来更糟）。
	if action == "open" && argBool(args, "headed") {
		if b := sandboxinfo.Get().Browser; b == nil || !b.HeadedAvailable {
			reason := "this sandbox did not report headed support"
			if b != nil && b.Error != "" {
				reason = b.Error
			}
			return nil, fmt.Errorf("browser: headed is not available (%s); call open without headed", reason)
		}
	}

	req, err := buildBrowserRequest(action, args)
	if err != nil {
		return nil, err
	}

	// 挂载与 shell 一致：工作区（rw、cwd）+ 本 agent 定义域（ro）。
	// 浏览器进程自己的 profile 由 sandboxd 指到 /tmp，不需要额外挂载。
	hostRoot := c.Workspace
	if hostRoot == "" {
		hostRoot = "."
	}
	if err := os.MkdirAll(hostRoot, 0o755); err != nil {
		return nil, err
	}
	mounts := []sandbox.Mount{
		{Host: hostRoot, Guest: workspaceGuestDir, ReadOnly: false},
	}
	if c.AgentRoot != "" {
		mounts = append(mounts, sandbox.Mount{Host: c.AgentRoot, Guest: agentGuestDir, ReadOnly: true})
	}

	line, err := client.Do(ctx, sess, browserToolID,
		[]string{browserArgvPlaceholder}, req, mounts, workspaceGuestDir)
	if err != nil {
		return nil, err
	}

	var out map[string]any
	if err := json.Unmarshal(line, &out); err != nil {
		return nil, fmt.Errorf("browser: bad response: %w", err)
	}
	ok, _ := out["ok"].(bool)
	if !ok {
		msg, _ := out["error"].(string)
		code, _ := out["code"].(string)
		if msg == "" {
			msg = "operation failed"
		}
		if code != "" {
			return nil, fmt.Errorf("browser %s [%s]: %s", action, code, msg)
		}
		return nil, fmt.Errorf("browser %s: %s", action, msg)
	}
	delete(out, "id")
	return out, nil
}

// ensureBrowserReady 浏览器工具的准入：sandboxd 必须明确说过「浏览器可用」。
//
// 与 ensureSandboxReady 同一条哲学 —— 不猜。端口没报、探测失败、镜像里没装，
// 三种情况都必须让模型看到明确原因，而不是让它以为「这个网站打不开」。
func ensureBrowserReady() error {
	b := sandboxinfo.Get().Browser
	if b == nil {
		return errors.New("browser disabled: sandboxd did not report browser capability " +
			"(upgrade sandboxd, or this sandbox does not ship a browser)")
	}
	if !b.Enabled {
		reason := b.Error
		if reason == "" {
			reason = "not enabled"
		}
		return fmt.Errorf("browser disabled: %s", reason)
	}
	return nil
}

// buildBrowserRequest 把工具参数翻译成 browserhost 的一行请求。
func buildBrowserRequest(action string, args map[string]any) ([]byte, error) {
	req := map[string]any{
		"id": browserReqSeq.Add(1),
		"op": action,
	}
	setStr := func(key, arg string) {
		if v := strings.TrimSpace(argString(args, arg)); v != "" {
			req[key] = v
		}
	}
	setStr("url", "url")
	setStr("ref", "ref")
	setStr("text", "text")
	setStr("key", "key")
	setStr("selector", "selector")
	setStr("path", "path")
	setStr("target", "target")

	for _, key := range []string{"submit", "full_page", "new_tab", "headed"} {
		if argBool(args, key) {
			req[key] = true
		}
	}
	for _, key := range []string{"x", "y", "index", "max_bytes", "max_nodes"} {
		if n := argInt(args, key); n != 0 {
			req[key] = n
		}
	}
	if sec := argInt(args, "timeout"); sec > 0 {
		req["timeout_ms"] = sec * 1000
	}

	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func init() {
	Register(&browserTool{})
}
