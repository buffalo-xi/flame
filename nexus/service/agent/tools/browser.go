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
		Description: "在沙箱内的浏览器里操作网页（Chromium / CDP）。" +
			"每次操作返回页面标题、URL 和一份无障碍树快照：形如 `- e4 [button] \"登录\"`，" +
			"click / type 用快照里的 ref（如 e4），不要猜 CSS 选择器。" +
			"快照只包含可读文本与可操作元素；导航后旧 ref 全部失效，需重新 snapshot。" +
			"截图会存到工作区并返回路径（截图是给人看的，你自己读不到图像，判断页面用 snapshot / text）。" +
			"浏览器是常驻的重量级进程（Chromium）：只在确实需要渲染或交互（登录态、JS 页面、点击流程）时才开，" +
			"能用 exec（curl / requests）拿到内容就不要开；不再交互时用 action=close 收掉，否则进程随会话一直保留。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []any{
						"open", "goto", "snapshot", "click", "type", "press",
						"scroll", "wait", "text", "screenshot", "tabs", "switch", "back", "close",
					},
					"description": "open=打开页面（首次会拉起浏览器）；snapshot=获取当前页面快照；" +
						"click/type/press=按 ref 操作；wait=等文本或选择器出现；" +
						"text=取页面纯文本；tabs/switch=多标签页；back=后退；close=关闭整个浏览器会话",
				},
				"url":       map[string]any{"type": "string", "description": "open / goto 的目标地址（http/https）"},
				"ref":       map[string]any{"type": "string", "description": "snapshot 返回的元素引用（如 e4）"},
				"text":      map[string]any{"type": "string", "description": "type 要输入的内容；wait 要等待的文本片段"},
				"key":       map[string]any{"type": "string", "description": "press 的按键：Enter / Tab / Escape / ArrowDown 等"},
				"selector":  map[string]any{"type": "string", "description": "wait 的 CSS 选择器（与 text 二选一）"},
				"submit":    map[string]any{"type": "boolean", "description": "type 之后是否按 Enter"},
				"full_page": map[string]any{"type": "boolean", "description": "screenshot 是否截整页（默认只截视口）"},
				"new_tab":   map[string]any{"type": "boolean", "description": "open 是否新开标签页"},
				"path":      map[string]any{"type": "string", "description": "screenshot 的保存路径（相对工作区，默认 browser/shot-<时间>.png）"},
				"target":    map[string]any{"type": "string", "description": "switch 的目标标签页 id（来自 tabs）"},
				"index":     map[string]any{"type": "number", "description": "switch 的目标标签页序号（从 1 开始）"},
				"x":         map[string]any{"type": "number", "description": "scroll 的横向像素（正数向右）"},
				"y":         map[string]any{"type": "number", "description": "scroll 的纵向像素（正数向下）"},
				"max_bytes": map[string]any{"type": "number", "description": "snapshot / text 的输出上限（默认 snapshot 8KB、text 32KB）"},
				"max_nodes": map[string]any{"type": "number", "description": "snapshot 的节点数上限（默认 400）"},
				"timeout":   map[string]any{"type": "number", "description": "本次操作超时秒数（默认 30，仍受 Context.Timeout 上限约束）"},
				"headed":    map[string]any{"type": "boolean", "description": "open 时是否有头（可见窗口）。只有部署允许（mode=none + allow_headed）时可用；浏览器一旦创建不能再切换"},
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
