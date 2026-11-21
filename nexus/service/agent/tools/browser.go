package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"flame/nexus/shared/browserinfo"
	"flame/tools/sandbox"
)

// browserTool is the browser (CDP / Chromium), served by the standalone browser pool (browserd).
//
// Like shell it is a "session-level resident process": several calls within the same session / run reuse one browser instance
// (cookies, login state and tabs all survive). It is **not** part of the sandbox any more: browserd owns the process, its
// capacity (workers / per-tenant quota) and its isolation boundary, and this tool side only translates one action into one
// line of JSON.
//
// The observation channel is an **accessibility-tree snapshot**, not a screenshot: a tool result can only carry text back
// (see marshalToolResult in agent_impl.go), screenshots go to disk for humans, and for the model to "see" a page it must rely
// on the role / name / ref returned by snapshot.
type browserTool struct{}

const (
	browserToolID = "browser"
	// browserArgvPlaceholder only satisfies the protocol's "the first open must carry argv" -
	// for tool_id=browser sandboxd overrides it with the browserhost path it is configured with
	// (only sandboxd knows paths inside the sandbox, the same reason as langs / codehost).
	browserArgvPlaceholder = "browserhost"
)

// browserReqSeq is a monotonically increasing request id: responses are matched by id, so a reply that is late after the
// previous timeout can be discarded.
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
	if err := ensureBrowserReady(); err != nil {
		return nil, err
	}
	client := browserClientFor(c.Schema)
	if client == nil {
		return nil, errors.New("browser disabled: no browser pool configured (set browser.url)")
	}

	action := strings.TrimSpace(argString(args, "action"))
	if action == "" {
		return nil, errors.New("action is required")
	}

	// Session identity exactly as in shell: SessionID first, RunID as the fallback (a temporary process).
	sess := sandbox.InteractiveSession{Key: c.SessionID}
	if sess.Key == "" {
		sess.Key = c.RunID
		sess.Ephemeral = true
	}

	if action == "close" {
		// Explicit close: sandboxd kills browserhost (its Chrome child process goes with it).
		// Idempotent - it succeeds even when the browser was never started.
		if err := client.Kill(ctx, sess, browserToolID); err != nil {
			return nil, err
		}
		return map[string]any{"closed": true}, nil
	}

	// Headed is a property of "creating the browser" and cannot be switched at run time. It is blocked here up front, so
	// that the model does not believe a window is open while it stays headless (the reverse would be worse).
	if action == "open" && argBool(args, "headed") {
		if b := browserinfo.Get(); !b.HeadedAvailable {
			reason := "the browser pool did not report headed support"
			if b.Error != "" {
				reason = b.Error
			}
			return nil, fmt.Errorf("browser: headed is not available (%s); call open without headed", reason)
		}
	}

	req, err := buildBrowserRequest(action, args)
	if err != nil {
		return nil, err
	}

	// Mounts are meaningless to the pool (it is not in the sandbox), so nothing is mounted here. What it does need is the
	// workspace **path**: the pool resolves it under the directory shared with nexus and runs the browser with that cwd,
	// so a screenshot or a download lands where this tenant can open it afterwards.
	line, err := client.Do(ctx, sess, browserToolID,
		[]string{browserArgvPlaceholder}, req, nil, c.WorkKey)
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

// ensureBrowserReady is the browser tool's admission check: the browser pool must have said explicitly that it can run a
// browser.
//
// The same philosophy as ensureSandboxReady - no guessing. No configured pool, a pool that never answered, a failed
// self-test, a driver that cannot find Chromium: all four must show the model an explicit reason rather than let it
// believe "this site cannot be opened".
func ensureBrowserReady() error {
	if !BrowserConfigured() {
		return errors.New("browser disabled: no browser pool configured (set browser.url)")
	}
	if !browserinfo.Configured() {
		return errors.New("browser disabled: the browser pool has not answered yet (its capabilities are unknown)")
	}
	b := browserinfo.Get()
	if !b.Available {
		reason := b.Error
		if reason == "" {
			reason = "not available"
		}
		return fmt.Errorf("browser disabled: %s", reason)
	}
	return nil
}

// buildBrowserRequest translates the tool arguments into one browserhost request line.
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
