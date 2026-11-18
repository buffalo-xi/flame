package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"
)

// BrowserInfo 浏览器能力快照（/v1/capabilities 的 browser 段）。
//
// 为什么要有这一项而不是让模型「调一次试试」：镜像里到底有没有 Chromium，
// 只有沙箱自己知道。不说，模型就会把「浏览器不可用」误判成「页面打不开」，
// 然后在错误的方向上重试 —— 与 langs / environment 是同一个理由。
type BrowserInfo struct {
	Enabled bool `json:"enabled"`
	// Engine / Version 来自 browserhost --selftest 的实测（不是配置声明）。
	Engine  string `json:"engine,omitempty"`
	Version string `json:"version,omitempty"`
	// HeadlessDefault 本部署缺省是否无头；HeadedAvailable 当前隔离方式下能否有头。
	HeadlessDefault bool `json:"headless_default"`
	HeadedAvailable bool `json:"headed_available"`
	// Error 非空表示不可用的原因（未启用 / 二进制缺失 / 镜像里没装浏览器）。
	Error string `json:"error,omitempty"`
}

// browserProbeTimeout 自检的上限。它只跑 `<browserhost> --selftest`
// （内部是 `chromium --version`），不拉起浏览器；docker 下多半花在建容器上。
const browserProbeTimeout = 30 * time.Second

const (
	// browserProbeRetryInterval 探测失败后的重试间隔。失败多半是环境性的
	// （浏览器正在更新、已有会话忙、容器刚起），快一点重试就能自愈。
	browserProbeRetryInterval = 30 * time.Second
	// browserProbeRefreshInterval 可用状态下的复核间隔。docker 下每次探测都要
	// 建一次容器，所以不跟着调用频率走，隔几分钟对一次即可。
	browserProbeRefreshInterval = 5 * time.Minute
)

// startBrowserProbe 启动异步能力探测（失败重试，成功定期复核）。
//
// 异步是刻意的：探测失败不该阻断 sandboxd 启动（浏览器只是可选能力），
// 但结果必须如实上报 —— 所以在完成前先落一个「探测中」的不可用态，
// 而不是先乐观地说「可用」。乐观默认会让模型在镜像还没装好时就开始点页面。
//
// 为什么不能只探一次：探测结果会由 /v1/capabilities 持续播报，而失败往往是
// 临时性的。只探一次意味着「一次失败 = 浏览器永久 disabled，直到重启」——
// 这正是 `bad output ""` 事件里真实的放大环节。
func (s *Server) startBrowserProbe() {
	if !s.cfg.Browser.Enabled {
		info := BrowserInfo{Enabled: false, Error: "disabled by config (browser.enabled: false)"}
		s.browser.Store(&info)
		return
	}
	// 非 none 下不可能为 true（validateBrowser 已拦），所以不再重复 `Mode == "none"`。
	headed := s.cfg.AllowHeadedFor(s.cfg.Mode)
	initial := BrowserInfo{
		Enabled:         false,
		HeadlessDefault: !s.cfg.Browser.Headed,
		HeadedAvailable: headed,
		Error:           "probing browser version",
	}
	s.browser.Store(&initial)

	go func() {
		prevReady := false
		lastErr := ""
		for {
			info, ok := s.probeBrowserOnce(headed)
			s.browser.Store(&info)

			switch {
			case ok && !prevReady:
				xlog.Info("sandboxd: browser ready (%s, headed_available=%v)", info.Version, info.HeadedAvailable)
			case !ok && info.Error != lastErr:
				// 同样的错误只在首次出现时记一条：重试间隔 30s，每次都打会把日志写成噪声。
				xlog.Error("sandboxd: browser unavailable: %s", info.Error)
			}

			prevReady = ok
			if ok {
				lastErr = ""
			} else {
				lastErr = info.Error
			}

			wait := browserProbeRetryInterval
			if ok {
				wait = browserProbeRefreshInterval
			}
			time.Sleep(wait)
		}
	}()
}

// probeBrowserOnce 跑一次 `<browserhost> --selftest`，返回能力快照与是否可用。
//
// 走 BrowserArgv 而不是自己拼：探测必须和真实调用看同一个 exec_path（按 mode 解析），
// 否则会出现「探测说可用、open 时找不到浏览器」。
func (s *Server) probeBrowserOnce(headed bool) (BrowserInfo, bool) {
	info := BrowserInfo{
		HeadlessDefault: !s.cfg.Browser.Headed,
		HeadedAvailable: headed,
	}

	argv, err := s.cfg.BrowserArgv(s.cfg.Mode)
	if err != nil {
		info.Error = err.Error()
		return info, false
	}
	argv = append(argv, "--selftest")

	ctx, cancel := context.WithTimeout(context.Background(), browserProbeTimeout)
	defer cancel()

	// stderr 必须收下来：失败原因几乎只出现在这里（二进制版本不对、缺库、
	// 镜像里没有浏览器）。之前填 io.Discard，症状就退化成一句 `bad output ""`。
	errBuf := &cappedWriter{n: 4096}

	spec := &sandbox.Spec{
		Argv:   argv,
		Stderr: errBuf,
		Limits: s.cfg.BrowserLimits(sandbox.Limits{}),
	}
	proc, err := s.exec.Start(ctx, spec)
	if err != nil {
		info.Error = fmt.Sprintf("start browserhost --selftest: %v", err)
		return info, false
	}
	out, _ := io.ReadAll(io.LimitReader(proc.Stdout(), 4096))
	_ = proc.Wait()

	var payload struct {
		Engine  string `json:"engine"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &payload); err != nil || payload.Version == "" {
		info.Error = fmt.Sprintf("browser selftest: bad output %q", truncateText(string(out), 200))
		if tail := strings.TrimSpace(errBuf.String()); tail != "" {
			info.Error += "; stderr: " + truncateText(tail, 300)
		}
		return info, false
	}

	info.Enabled = true
	info.Engine = payload.Engine
	info.Version = payload.Version
	return info, true
}

// cappedWriter 只保留前 n 字节（探测用的 stderr 缓冲）：
// 要的是「为什么起不来」的开头，而不是可能无限增长的日志。
type cappedWriter struct {
	buf []byte
	n   int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if remain := w.n - len(w.buf); remain > 0 {
		if len(p) > remain {
			w.buf = append(w.buf, p[:remain]...)
		} else {
			w.buf = append(w.buf, p...)
		}
	}
	return len(p), nil
}

func (w *cappedWriter) String() string { return string(w.buf) }

// BrowserSnapshot 取当前浏览器能力快照（未探测完时返回不可用态）。
func (s *Server) BrowserSnapshot() BrowserInfo {
	if p := s.browser.Load(); p != nil {
		return *p
	}
	return BrowserInfo{Error: "browser not probed"}
}

// truncateText 截断过长文本（只用于日志 / 错误信息）。
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
