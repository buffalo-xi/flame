package browserhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Default viewport. The fixed size is deliberate: the model does not have to guess "how wide is the page really",
// and screenshots and snapshots stay comparable across sessions.
const (
	defaultWidth  = 1280
	defaultHeight = 800

	defaultOpTimeout = 30 * time.Second
	// tabInitTimeout caps creating a new tab (including the very first browser launch). It applies to a child
	// context, so a timeout only kills that tab and not the browser (chromedp treats a timeout on the first Run
	// as the end of the whole browser lifetime, which is why it is deliberately not shared with the tab's ctx).
	tabInitTimeout = 30 * time.Second

	defaultMaxNodes = 400
	defaultMaxBytes = 8192
	// The small snapshot attached after an action: it saves a snapshot round trip without blowing up the tokens.
	actionMaxNodes = 120
	actionMaxBytes = 3000
)

// Config holds browserhost's startup arguments (built by browserd for each session and passed in through argv).
type Config struct {
	// ExecPath is the browser executable; empty = auto-discover (chromium / chrome / msedge).
	ExecPath string
	// ProfileDir is Chrome's user-data-dir; empty = a temp directory, removed on exit.
	//
	// browserd always passes the session's own directory under browser.profile_root and deletes it when the session
	// ends: the profile carries cookies and local storage, so it must never be shared between sessions.
	ProfileDir string
	// DownloadsDir is the download directory; empty = <cwd>/downloads (that is, the workspace).
	DownloadsDir string
	// AllowHeaded says whether headed=true is allowed. The policy belongs to browserd (browser.allow_headed);
	// this is only the last gate at the execution layer — callers may omit it, but once it is set it must hold.
	AllowHeaded bool
	// DefaultHeaded is the display mode when headed is not explicitly requested.
	DefaultHeaded bool
	// Logf is the log sink (stderr). Dropped when nil.
	Logf func(format string, args ...any)
}

// Host is one browser instance plus its tabs.
//
// Lifetime: the browser is launched on the first open; everything is torn down when the process exits (stdin
// EOF). When the process is killed, Chrome is its child and goes down with the process tree — browserpool.killTree
// takes the whole tree down, never one pid.
type Host struct {
	cfg  Config
	logf func(string, ...any)

	allocCtx    context.Context
	allocCancel context.CancelFunc
	started     bool
	headed      bool

	tabs   map[target.ID]*tab
	active *tab

	// refs is the element reference table of the current snapshot. It is rebuilt on every snapshot — after a
	// navigation an old ref must become invalid, otherwise the model would use "e4 from the previous page" to
	// click something on the new one and hit a completely unrelated element.
	refs   map[string]cdp.BackendNodeID
	refSeq int

	mu sync.Mutex // serialises one operation (the protocol itself is single-threaded; this guards future entry points)
}

type tab struct {
	ctx    context.Context
	cancel context.CancelFunc
	id     target.ID
}

// New creates the host (it does not start the browser).
func New(cfg Config) *Host {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Host{cfg: cfg, logf: logf, tabs: map[target.ID]*tab{}}
}

// Handle processes one request.
func (h *Host) Handle(req Request) Response {
	h.mu.Lock()
	defer h.mu.Unlock()

	resp, err := h.dispatch(req)
	if err != nil {
		if oe, ok := err.(*opError); ok {
			return Response{ID: req.ID, Code: oe.code, Error: oe.msg}
		}
		return Response{ID: req.ID, Code: "failed", Error: err.Error()}
	}
	resp.ID = req.ID
	resp.OK = true
	return *resp
}

func (h *Host) dispatch(req Request) (*Response, error) {
	switch req.Op {
	case "ping":
		return &Response{}, nil
	case "open":
		return h.open(req)
	case "goto":
		return h.navigate(req)
	case "snapshot":
		return h.snapshot(req, 0, 0)
	case "text":
		return h.pageText(req)
	case "click":
		return h.click(req)
	case "type":
		return h.typeText(req)
	case "press":
		return h.press(req)
	case "scroll":
		return h.scroll(req)
	case "wait":
		return h.waitFor(req)
	case "screenshot":
		return h.screenshot(req)
	case "tabs":
		return h.listTabs()
	case "switch":
		return h.switchTab(req)
	case "back":
		return h.back(req)
	case "close", "shutdown":
		h.Close()
		return &Response{}, nil
	default:
		return nil, fail("unknown_op", "unknown op %q", req.Op)
	}
}

// ---------------------------------------------------------------------------
// Lifetime
// ---------------------------------------------------------------------------

// start launches the browser for the first time. headed only matters here — Chrome cannot switch at runtime.
func (h *Host) start(headed bool) error {
	if h.started {
		return nil
	}
	if headed && !h.cfg.AllowHeaded {
		return fail("headed_not_allowed",
			"headed browsing is not allowed by this deployment (browser.allow_headed=false); "+
				"call open without headed, or enable it in browserd config")
	}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.NoSandbox, // the container is the isolation boundary; inside docker the userns is usually unavailable
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		// Chrome started through CDP sets navigator.webdriver to true, and many sites simply drop a captcha based
		// on that. This is not "bypassing verification", it only stops the browser from advertising "I am
		// automation" — the actual check is still up to the site.
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-download-notification", true),
		// Cache cap: under docker the profile lands in --tmpfs /tmp (which counts against the memory limit), so an
		// uncapped cache would let one heavy site blow up the cgroup.
		// The value must be a string: chromedp's flagArgs only understands string / bool.
		chromedp.Flag("disk-cache-size", "33554432"),
		chromedp.WindowSize(defaultWidth, defaultHeight),
	}
	if h.cfg.ExecPath != "" {
		opts = append(opts, chromedp.ExecPath(h.cfg.ExecPath))
	}
	profile := h.cfg.ProfileDir
	if profile != "" {
		opts = append(opts, chromedp.UserDataDir(profile))
	} else {
		dir, err := os.MkdirTemp("", "browserhost-profile-")
		if err != nil {
			return err
		}
		h.cfg.ProfileDir = dir
		opts = append(opts, chromedp.UserDataDir(dir))
	}
	if !headed {
		// The new headless mode (Chrome 112+): the old headless differs a lot in fonts and viewport behaviour.
		opts = append(opts, chromedp.Flag("headless", "new"))
		// Muting is for headless only: headless has no audio output path, and the flag is kept to stop Chrome from
		// repeatedly logging audio backend errors. Headed (only possible under mode=none) must be audible — this
		// used to be passed unconditionally, which left the headed window mute as well.
		opts = append(opts, chromedp.Flag("mute-audio", true))
	}

	h.allocCtx, h.allocCancel = chromedp.NewExecAllocator(context.Background(), opts...)
	if _, err := h.newTab(""); err != nil {
		h.allocCancel()
		h.allocCtx = nil
		return err
	}
	h.started = true
	h.headed = headed
	h.logf("browser started: headed=%v profile=%s exec=%s", headed, h.cfg.ProfileDir, h.cfg.ExecPath)
	h.setupDownloads()
	return nil
}

// newTab creates a tab and makes it current. A non-empty existing attaches to an existing target.
func (h *Host) newTab(existing target.ID) (*tab, error) {
	var opts []chromedp.ContextOption
	if existing != "" {
		opts = append(opts, chromedp.WithTargetID(existing))
	}
	ctx, cancel := chromedp.NewContext(h.allocCtx, opts...)

	// The first Run triggers "launch the browser / create the tab".
	//
	// ⚠️ The tab ctx must be used directly: chromedp binds the browser's lifetime to the context of the **first
	// Run**, so wrapping it in WithTimeout and cancelling it shuts the whole browser down (every later operation
	// then reports context canceled). Timing must be enforced by an external watchdog instead — a timeout means
	// the browser really cannot start, and killing it along with the tab is exactly what we want then.
	watchdog := time.AfterFunc(tabInitTimeout, cancel)
	_, err := chromedp.Run(ctx, chromedp.Title())
	watchdog.Stop()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("browser start: %w", err)
	}

	id := chromedp.FromContext(ctx).Target.TargetID
	t := &tab{ctx: ctx, cancel: cancel, id: id}
	h.tabs[id] = t
	h.active = t
	return t, nil
}

// setupDownloads points the download directory at the workspace (default <cwd>/downloads).
func (h *Host) setupDownloads() {
	dir := h.cfg.DownloadsDir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return
		}
		dir = filepath.Join(cwd, "downloads")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.logf("downloads dir: %v", err)
		return
	}
	_, err := chromedp.Call(h.active.ctx, browser.SetDownloadBehavior, browser.SetDownloadBehaviorParams{
		Behavior:     browser.SetDownloadBehaviorBehaviorAllow,
		DownloadPath: dir,
	})
	if err != nil {
		h.logf("set download behavior: %v", err)
	}
}

// Close tears down the browser and every tab.
func (h *Host) Close() {
	for _, t := range h.tabs {
		t.cancel()
	}
	h.tabs = map[target.ID]*tab{}
	h.active = nil
	if h.allocCancel != nil {
		h.allocCancel()
		h.allocCancel = nil
	}
	if h.cfg.ProfileDir != "" && strings.Contains(filepath.Base(h.cfg.ProfileDir), "browserhost-profile-") {
		_ = os.RemoveAll(h.cfg.ProfileDir)
	}
	h.started = false
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

func (h *Host) open(req Request) (*Response, error) {
	headed := h.cfg.DefaultHeaded
	if req.Headed {
		headed = true
	}
	if err := h.start(headed); err != nil {
		return nil, err
	}

	if req.NewTab {
		if _, err := h.newTab(""); err != nil {
			return nil, fail("new_tab_failed", "%v", err)
		}
	}
	if req.URL != "" {
		if err := h.navigateTo(req); err != nil {
			return nil, err
		}
	}
	resp, err := h.snapshot(req, actionMaxNodes, actionMaxBytes)
	if err != nil {
		return nil, err
	}
	headedCopy := h.headed
	resp.Headed = &headedCopy
	return resp, nil
}

func (h *Host) navigate(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	if req.URL == "" {
		return nil, fail("bad_request", "url is required")
	}
	if err := h.navigateTo(req); err != nil {
		return nil, err
	}
	return h.snapshot(req, actionMaxNodes, actionMaxBytes)
}

func (h *Host) navigateTo(req Request) error {
	ctx, cancel := h.opCtx(req)
	defer cancel()
	if err := chromedp.Do(ctx, chromedp.Navigate(req.URL)); err != nil {
		return fail("nav_failed", "navigate %s: %v", req.URL, err)
	}
	return nil
}

func (h *Host) back(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()
	if err := chromedp.Do(ctx, chromedp.NavigateBack()); err != nil {
		return nil, fail("nav_failed", "back: %v", err)
	}
	return h.snapshot(req, actionMaxNodes, actionMaxBytes)
}

func (h *Host) snapshot(req Request, defNodes, defBytes int) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()

	res, err := chromedp.Call(ctx, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
	if err != nil {
		return nil, fail("snapshot_failed", "accessibility tree: %v", err)
	}

	maxNodes := req.MaxNodes
	if maxNodes <= 0 {
		maxNodes = defNodes
	}
	if maxNodes <= 0 {
		maxNodes = defaultMaxNodes
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defBytes
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}

	text, refs, count, truncated := renderAX(res.Nodes, maxNodes, maxBytes)
	h.refs = refs
	h.refSeq = len(refs)

	resp := &Response{Snapshot: text, Nodes: count, Truncated: truncated}
	h.fillPageInfo(ctx, resp, req)
	return resp, nil
}

func (h *Host) pageText(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()
	s, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.body ? document.body.innerText : ''`))
	if err != nil {
		return nil, fail("eval_failed", "innerText: %v", err)
	}
	limit := req.MaxBytes
	if limit <= 0 {
		limit = 32 * 1024
	}
	truncated := false
	if len(s) > limit {
		s = s[:limit]
		truncated = true
	}
	resp := &Response{Text: s, Truncated: truncated}
	h.fillPageInfo(ctx, resp, req)
	return resp, nil
}

func (h *Host) click(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	backend, err := h.ref(req.Ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()

	if err := callOnNode(ctx, backend, `function(){ this.scrollIntoView({block:'center'}); this.click(); return true; }`); err != nil {
		return nil, fail("click_failed", "%v", err)
	}
	return h.afterAction(ctx, req)
}

func (h *Host) typeText(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	backend, err := h.ref(req.Ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()

	// focus + select rather than assigning value directly: React/Vue controlled components do not notice a plain
	// assignment.
	if err := callOnNode(ctx, backend, `function(){ this.focus(); if (this.select) this.select(); return true; }`); err != nil {
		return nil, fail("type_failed", "%v", err)
	}
	if req.Text != "" {
		// Input.insertText inserts "the way an IME does", so it does fire input events.
		if _, err := chromedp.Call(ctx, input.InsertText, input.InsertTextParams{Text: req.Text}); err != nil {
			return nil, fail("type_failed", "%v", err)
		}
	}
	if req.Submit {
		if _, err := chromedp.Call(ctx, input.DispatchKeyEvent, keyParams("Enter", input.DispatchKeyEventTypeKeyDown)); err != nil {
			return nil, fail("type_failed", "%v", err)
		}
		if _, err := chromedp.Call(ctx, input.DispatchKeyEvent, keyParams("Enter", input.DispatchKeyEventTypeKeyUp)); err != nil {
			return nil, fail("type_failed", "%v", err)
		}
	}
	return h.afterAction(ctx, req)
}

func (h *Host) press(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	if req.Key == "" {
		return nil, fail("bad_request", "key is required")
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()

	if _, err := chromedp.Call(ctx, input.DispatchKeyEvent, keyParams(req.Key, input.DispatchKeyEventTypeKeyDown)); err != nil {
		return nil, fail("press_failed", "%v", err)
	}
	if _, err := chromedp.Call(ctx, input.DispatchKeyEvent, keyParams(req.Key, input.DispatchKeyEventTypeKeyUp)); err != nil {
		return nil, fail("press_failed", "%v", err)
	}
	return h.afterAction(ctx, req)
}

func (h *Host) scroll(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()
	if req.Ref != "" {
		backend, err := h.ref(req.Ref)
		if err != nil {
			return nil, err
		}
		if err := callOnNode(ctx, backend, `function(){ this.scrollIntoView({block:'center'}); return true; }`); err != nil {
			return nil, fail("scroll_failed", "%v", err)
		}
	} else {
		expr := fmt.Sprintf(`window.scrollBy(%d, %d)`, int(req.X), int(req.Y))
		if _, err := chromedp.Run(ctx, chromedp.Evaluate[any](expr)); err != nil {
			return nil, fail("scroll_failed", "%v", err)
		}
	}
	return h.afterAction(ctx, req)
}

func (h *Host) waitFor(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	if req.Text == "" && req.Selector == "" {
		return nil, fail("bad_request", "wait needs text or selector")
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()

	var expr string
	if req.Selector != "" {
		q, _ := json.Marshal(req.Selector)
		expr = fmt.Sprintf(`!!document.querySelector(%s)`, q)
	} else {
		q, _ := json.Marshal(req.Text)
		expr = fmt.Sprintf(`(() => (document.body ? document.body.innerText : '').includes(%s))()`, q)
	}

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](expr))
		if err != nil {
			return nil, fail("wait_failed", "%v", err)
		}
		if ok {
			return h.afterAction(ctx, req)
		}
		select {
		case <-ctx.Done():
			return nil, fail("timeout", "wait: condition not met before timeout")
		case <-ticker.C:
		}
	}
}

func (h *Host) screenshot(req Request) (*Response, error) {
	if err := h.requireTab(); err != nil {
		return nil, err
	}
	ctx, cancel := h.opCtx(req)
	defer cancel()

	var (
		data []byte
		err  error
	)
	if req.FullPage {
		data, err = chromedp.Run(ctx, chromedp.FullScreenshot(90))
	} else {
		data, err = chromedp.Run(ctx, chromedp.CaptureScreenshot())
	}
	if err != nil {
		return nil, fail("screenshot_failed", "%v", err)
	}

	rel := req.Path
	if rel == "" {
		rel = filepath.Join("browser", fmt.Sprintf("shot-%d.png", time.Now().UnixNano()))
	}
	// Relative paths only: screenshots land in the workspace, so the browser cannot write files anywhere it likes
	// on the host.
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return nil, fail("bad_request", "screenshot path must be relative and inside the workspace")
	}
	if err := os.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return nil, fail("write_failed", "%v", err)
	}
	if err := os.WriteFile(rel, data, 0o644); err != nil {
		return nil, fail("write_failed", "%v", err)
	}
	return &Response{Path: filepath.ToSlash(rel), Bytes: len(data)}, nil
}

func (h *Host) listTabs() (*Response, error) {
	if h.allocCtx == nil {
		return nil, fail("not_open", "browser is not started; call open first")
	}
	list, err := chromedp.Targets(h.allocCtx)
	if err != nil {
		return nil, fail("tabs_failed", "%v", err)
	}
	out := make([]TabInfo, 0, len(list))
	for _, t := range list {
		if t.Type != "page" {
			continue
		}
		info := TabInfo{ID: string(t.TargetID), URL: t.URL, Title: t.Title}
		if h.active != nil && t.TargetID == h.active.id {
			info.Active = true
		}
		out = append(out, info)
	}
	return &Response{Tabs: out}, nil
}

func (h *Host) switchTab(req Request) (*Response, error) {
	if h.allocCtx == nil {
		return nil, fail("not_open", "browser is not started; call open first")
	}
	list, err := chromedp.Targets(h.allocCtx)
	if err != nil {
		return nil, fail("tabs_failed", "%v", err)
	}
	var pages []*target.Info
	for _, t := range list {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	var pick *target.Info
	if req.Target != "" {
		for _, t := range pages {
			if string(t.TargetID) == req.Target {
				pick = t
				break
			}
		}
	} else if req.Index > 0 && req.Index <= len(pages) {
		pick = pages[req.Index-1]
	}
	if pick == nil {
		return nil, fail("bad_request", "switch: target not found (call tabs to list)")
	}

	if t, ok := h.tabs[pick.TargetID]; ok {
		h.active = t
	} else {
		t, err := h.newTab(pick.TargetID)
		if err != nil {
			return nil, fail("switch_failed", "%v", err)
		}
		h.active = t
	}
	return h.snapshot(req, actionMaxNodes, actionMaxBytes)
}

// afterAction is the wrap-up after an action: a small snapshot plus page info. It saves the model an extra
// snapshot round trip, but with a smaller budget than an explicit snapshot — clicking repeatedly would pile the
// tokens up fast.
func (h *Host) afterAction(ctx context.Context, req Request) (*Response, error) {
	resp := &Response{Changed: true}
	if err := h.fillSnapshot(ctx, resp, actionMaxNodes, actionMaxBytes); err != nil {
		return nil, err
	}
	h.fillPageInfo(ctx, resp, req)
	return resp, nil
}

func (h *Host) fillSnapshot(ctx context.Context, resp *Response, maxNodes, maxBytes int) error {
	res, err := chromedp.Call(ctx, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
	if err != nil {
		// A failed snapshot must not turn "the click already succeeded" into an overall failure.
		h.logf("post-action snapshot: %v", err)
		return nil
	}
	text, refs, count, truncated := renderAX(res.Nodes, maxNodes, maxBytes)
	h.refs = refs
	h.refSeq = len(refs)
	resp.Snapshot, resp.Nodes, resp.Truncated = text, count, truncated
	return nil
}

func (h *Host) fillPageInfo(ctx context.Context, resp *Response, req Request) {
	if loc, err := chromedp.Run(ctx, chromedp.Location()); err == nil {
		resp.URL = loc
	}
	if title, err := chromedp.Run(ctx, chromedp.Title()); err == nil {
		resp.Title = title
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (h *Host) requireTab() error {
	if !h.started || h.active == nil {
		return fail("not_open", "browser is not started; call open first")
	}
	return nil
}

func (h *Host) opCtx(req Request) (context.Context, context.CancelFunc) {
	d := defaultOpTimeout
	if req.TimeoutMS > 0 {
		d = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	return context.WithTimeout(h.active.ctx, d)
}

func (h *Host) ref(ref string) (cdp.BackendNodeID, error) {
	if ref == "" {
		return 0, fail("bad_request", "ref is required (take a snapshot first)")
	}
	id, ok := h.refs[ref]
	if !ok {
		return 0, fail("invalid_ref",
			"ref %q is not in the current snapshot — the page may have changed; take a new snapshot", ref)
	}
	return id, nil
}

// callOnNode runs a piece of JS on a DOM node.
//
// It uses el.click() rather than dispatching mouse events at coordinates: coordinate clicks go wrong in scroll
// containers, with fixed positioning and on covered elements, while a DOM click is semantically equivalent to
// "the user really clicked it".
func callOnNode(ctx context.Context, backend cdp.BackendNodeID, fn string) error {
	res, err := chromedp.Call(ctx, dom.ResolveNode, dom.ResolveNodeParams{BackendNodeID: backend})
	if err != nil {
		return fmt.Errorf("resolve node: %w (element may be detached)", err)
	}
	if res.Object == nil || res.Object.ObjectID == "" {
		return fmt.Errorf("resolve node: empty object (element may be detached)")
	}
	out, err := chromedp.Call(ctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		FunctionDeclaration: fn,
		ObjectID:            res.Object.ObjectID,
	})
	if err != nil {
		return fmt.Errorf("call on node: %w", err)
	}
	if out.ExceptionDetails != nil {
		return fmt.Errorf("js exception: %s", out.ExceptionDetails.Text)
	}
	return nil
}

// keySpec is the code / windowsVirtualKeyCode of a common key.
type keySpec struct {
	code string
	vk   int64
	text string
}

var keys = map[string]keySpec{
	"Enter":      {"Enter", 13, "\r"},
	"Tab":        {"Tab", 9, ""},
	"Escape":     {"Escape", 27, ""},
	"Backspace":  {"Backspace", 8, ""},
	"Delete":     {"Delete", 46, ""},
	"ArrowUp":    {"ArrowUp", 38, ""},
	"ArrowDown":  {"ArrowDown", 40, ""},
	"ArrowLeft":  {"ArrowLeft", 37, ""},
	"ArrowRight": {"ArrowRight", 39, ""},
	"Home":       {"Home", 36, ""},
	"End":        {"End", 35, ""},
	"PageUp":     {"PageUp", 33, ""},
	"PageDown":   {"PageDown", 34, ""},
	"Space":      {"Space", 32, " "},
}

func keyParams(key string, typ input.DispatchKeyEventType) input.DispatchKeyEventParams {
	p := input.DispatchKeyEventParams{
		Type: typ,
		Key:  key,
	}
	if spec, ok := keys[key]; ok {
		p.Code = spec.code
		p.WindowsVirtualKeyCode = spec.vk
		p.NativeVirtualKeyCode = spec.vk
		if typ == input.DispatchKeyEventTypeKeyDown && spec.text != "" {
			p.Text = spec.text
			p.UnmodifiedText = spec.text
		}
		return p
	}
	if len(key) == 1 {
		// Single character: follow the usual code and vk conventions (the common case of an uppercase letter /
		// digit / symbol).
		r := []rune(strings.ToUpper(key))[0]
		p.Code = "Key" + string(r)
		p.WindowsVirtualKeyCode = int64(r)
		p.NativeVirtualKeyCode = int64(r)
		if typ == input.DispatchKeyEventTypeKeyDown {
			p.Text = key
			p.UnmodifiedText = key
		}
	}
	return p
}
