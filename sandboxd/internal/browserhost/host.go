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

// 默认视口。固定尺寸是刻意的：让模型不必猜「页面到底有多宽」，
// 也让截图与快照在不同会话间可比。
const (
	defaultWidth  = 1280
	defaultHeight = 800

	defaultOpTimeout = 30 * time.Second
	// tabInitTimeout 新标签页（含首次拉起浏览器）的上限。它作用在子 context 上，
	// 超时只废掉这个标签页、不杀浏览器（chromedp 的首次 Run 会把超时当成整个
	// 浏览器生命周期的尽头，所以这里刻意不与 tab 的 ctx 共用）。
	tabInitTimeout = 30 * time.Second

	defaultMaxNodes = 400
	defaultMaxBytes = 8192
	// 动作后附带的小快照：省掉一轮 snapshot，但别把 token 打爆。
	actionMaxNodes = 120
	actionMaxBytes = 3000
)

// Config browserhost 的启动参数（由 sandboxd 按 mode 解析后经 argv 传入）。
type Config struct {
	// ExecPath 浏览器可执行文件；空 = 自动探测（chromium / chrome / msedge）。
	ExecPath string
	// ProfileDir Chrome 的 user-data-dir；空 = 临时目录，退出即删。
	//
	// docker 下由 sandboxd 指到 /tmp（容器带 --tmpfs /tmp），随容器销毁；
	// none 下留空即可 —— 宿主 /tmp 里的临时目录在 Close 时清掉。
	ProfileDir string
	// DownloadsDir 下载目录；空 = <cwd>/downloads（即工作区）。
	DownloadsDir string
	// AllowHeaded 是否允许 headed=true。策略归 sandboxd（配置 + mode 校验），
	// 这里只做执行层的最后一道闸 —— 上层可以不传，但传了就必须守住。
	AllowHeaded bool
	// DefaultHeaded 未显式请求时的缺省显示方式。
	DefaultHeaded bool
	// Logf 日志出口（stderr）。nil 时丢弃。
	Logf func(format string, args ...any)
}

// Host 一个浏览器实例 + 它的标签页。
//
// 生命周期：第一次 open 时拉起浏览器；进程退出（stdin EOF）时整体销毁。
// 进程被杀时 Chrome 是它的子进程，会随进程树一起收掉（见 sandboxd 的杀干净约定）。
type Host struct {
	cfg  Config
	logf func(string, ...any)

	allocCtx    context.Context
	allocCancel context.CancelFunc
	started     bool
	headed      bool

	tabs   map[target.ID]*tab
	active *tab

	// refs 当前快照里的元素引用表。每次 snapshot 重建 —— 导航后旧 ref 必须失效，
	// 否则模型会拿一个「上一次页面里的 e4」去点新页面，点到完全不相干的东西。
	refs   map[string]cdp.BackendNodeID
	refSeq int

	mu sync.Mutex // 串行化一次操作（协议本身是单线程的，这里防未来的并发入口）
}

type tab struct {
	ctx    context.Context
	cancel context.CancelFunc
	id     target.ID
}

// New 创建宿主（不启动浏览器）。
func New(cfg Config) *Host {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Host{cfg: cfg, logf: logf, tabs: map[target.ID]*tab{}}
}

// Handle 处理一次请求。
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
// 生命周期
// ---------------------------------------------------------------------------

// start 首次拉起浏览器。headed 只在这里有意义 —— Chrome 不能在运行中切换。
func (h *Host) start(headed bool) error {
	if h.started {
		return nil
	}
	if headed && !h.cfg.AllowHeaded {
		return fail("headed_not_allowed",
			"headed browsing is not allowed by this deployment (browser.allow_headed=false, or mode is not none); "+
				"call open without headed, or enable it in sandboxd config")
	}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.NoSandbox, // 容器本身就是隔离边界；inside docker userns 通常不可用
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		// CDP 启动的 Chrome 会把 navigator.webdriver 置为 true，很多站点据此
		// 直接丢一张验证码。这不是「绕过验证」，只是别让浏览器自报「我是自动化」——
		// 真正的验证仍然由站点决定。
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-download-notification", true),
		// 缓存上限：docker 下 profile 落在 --tmpfs /tmp（计进内存上限），
		// 不设限会被一个大站点的资源撑爆 cgroup。
		// 值必须是字符串：chromedp 的 flagArgs 只认 string / bool。
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
		// 新 headless（Chrome 112+）：旧 headless 的字体与视口行为差异很大。
		opts = append(opts, chromedp.Flag("headless", "new"))
		// 静音只给无头：无头没有音频输出路径，留着这条是为了免得 Chrome 反复刷
		// 音频后端错误。有头（只能在 mode=none 下开启）必须能出声 —— 这条以前
		// 是无条件下发的，导致有头窗口也是哑的。
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

// newTab 建一个标签页并设为当前。existing 非空时附着到已有 target。
func (h *Host) newTab(existing target.ID) (*tab, error) {
	var opts []chromedp.ContextOption
	if existing != "" {
		opts = append(opts, chromedp.WithTargetID(existing))
	}
	ctx, cancel := chromedp.NewContext(h.allocCtx, opts...)

	// 第一次 Run 触发「拉浏览器 / 建标签页」。
	//
	// ⚠️ 必须直接用 tab ctx：chromedp 把浏览器的生命周期绑在**首次 Run 的
	// context** 上，给它套一层 WithTimeout 再 cancel，会把整个浏览器一起关掉
	// （表现为后续每个操作都是 context canceled）。要限时只能用外部看门狗 ——
	// 超时意味着浏览器真的起不来，那时连浏览器一起杀掉正是我们想要的。
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

// setupDownloads 把下载目录指到工作区（默认 <cwd>/downloads）。
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

// Close 销毁浏览器与所有标签页。
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
// 操作
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

	// focus + select：不直接改 value —— React/Vue 的受控组件感知不到直接赋值。
	if err := callOnNode(ctx, backend, `function(){ this.focus(); if (this.select) this.select(); return true; }`); err != nil {
		return nil, fail("type_failed", "%v", err)
	}
	if req.Text != "" {
		// Input.insertText 走的是「像输入法一样插入」，能触发 input 事件。
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
	// 只接受相对路径：截图落在工作区，不让浏览器往宿主任意位置写文件。
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

// afterAction 动作后的收尾：小快照 + 页面信息。省掉模型额外一轮 snapshot，
// 但预算压得比显式 snapshot 小 —— 连续点击时 token 会很快堆起来。
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
		// 快照失败不该把「点击已经成功」这件事变成整体失败。
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
// 工具方法
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

// callOnNode 在 DOM 节点上执行一段 JS。
//
// 用 el.click() 而不是按坐标派发鼠标事件：坐标点击在滚动容器、固定定位、
// 被遮挡的元素上全会点错，而 DOM 点击与「用户真的点了它」在业务语义上等价。
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

// keySpec 常用按键的 code / windowsVirtualKeyCode。
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
		// 单字符：用 code 与 vk 的常规约定（大写字母 / 数字 / 符号的常见情形）。
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
