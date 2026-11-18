package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultPython  = "python"
	defaultTimeout = 30 * time.Second
	hostScriptRel  = "_host/pluginhost.py"

	// maxStdoutBytes 只防失控：结果的有效上限（12KB）由 runner 负责截断。
	maxStdoutBytes = 8 << 20
	// maxStderrBytes stderr 只用来记日志，截断即可。
	maxStderrBytes = 4 << 10
)

// Host 一个插件目录的宿主：起 Python 子进程跑 list / invoke。
//
// 无状态、不缓存 manifest —— 扫描与缓存归 Registry 管（见 registry.go）。
// 每次调用一个独立进程：崩溃天然隔离，超时按进程组整组杀（sysproc_*.go）。
type Host struct {
	Dir     string        // 插件根目录（{root_dir}/plugins）
	Python  string        // 解释器命令；空 = python
	Timeout time.Duration // 单次调用超时；0 = 30s
}

// NewHost 构造一个宿主。timeout <= 0 时用默认 30s。
func NewHost(dir, python string, timeout time.Duration) *Host {
	return &Host{Dir: dir, Python: python, Timeout: timeout}
}

// Script 返回宿主脚本路径。
func (h *Host) Script() string {
	return filepath.Join(h.Dir, filepath.FromSlash(hostScriptRel))
}

func (h *Host) python() string {
	if p := strings.TrimSpace(h.Python); p != "" {
		return p
	}
	return defaultPython
}

func (h *Host) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return defaultTimeout
}

// List 扫描目录，返回工具清单与逐条扫描错误。
func (h *Host) List(ctx context.Context) (*ListResult, error) {
	out, stderr, err := h.run(ctx, nil, "list", "--dir", h.Dir)
	if err != nil {
		return nil, err
	}
	var res ListResult
	if err := json.Unmarshal(jsonLine(out), &res); err != nil {
		return nil, &CallError{
			Code:    CodeOther,
			Message: fmt.Sprintf("解析插件清单失败: %v%s", err, stderrSuffix(stderr)),
		}
	}
	return &res, nil
}

// Invoke 执行一次工具调用，返回插件给的 result（未裁剪，裁剪在 runner）。
func (h *Host) Invoke(ctx context.Context, tool, impl string, payload *InvokePayload) (any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &CallError{Code: CodeConfig, Message: fmt.Sprintf("序列化调用参数失败: %v", err)}
	}
	out, stderr, err := h.run(ctx, body,
		"invoke", "--dir", h.Dir, "--tool", tool, "--impl", impl)
	if err != nil {
		// 进程没跑起来 / 被杀：把 stderr 尾巴带上，否则现场只剩一个 exit status
		var ce *CallError
		if errors.As(err, &ce) {
			ce.Message += stderrSuffix(stderr)
			return nil, ce
		}
		return nil, err
	}

	var res InvokeResponse
	if err := json.Unmarshal(jsonLine(out), &res); err != nil {
		return nil, &CallError{
			Code:    CodeOther,
			Message: fmt.Sprintf("解析插件返回失败: %v%s", err, stderrSuffix(stderr)),
		}
	}
	if res.Error != nil {
		return nil, &CallError{Code: normalizeCode(res.Error.Code), Message: res.Error.Message}
	}
	if !res.OK {
		return nil, &CallError{Code: CodeOther, Message: "插件返回 ok=false 但没带错误信息"}
	}
	return res.Result, nil
}

// run 起一次宿主进程。stdin 为 nil 表示不喂输入。
func (h *Host) run(ctx context.Context, stdin []byte, args ...string) ([]byte, string, error) {
	cctx, cancel := context.WithTimeout(ctx, h.timeout())
	defer cancel()

	full := append([]string{h.Script()}, args...)
	cmd := exec.CommandContext(cctx, h.python(), full...)
	cmd.Dir = h.Dir
	// 协议固定 UTF-8：Windows 上 Python 默认按 locale（cp936）编解码管道，
	// 中文查询词会在 stdin 被解成 surrogate、再在 stdout 编码时炸掉。
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	// 进程组：超时时整组杀，否则 python 拉起的子进程（pip、curl 之类）会残留
	setProcAttr(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 5 * time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	outW := newLimitedWriter(maxStdoutBytes)
	errW := newLimitedWriter(maxStderrBytes)
	cmd.Stdout = outW
	cmd.Stderr = errW

	err := cmd.Run()
	stderr := strings.TrimSpace(errW.String())

	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, stderr, &CallError{
				Code:    CodeNetwork,
				Message: fmt.Sprintf("插件调用超时（%s）", h.timeout()),
			}
		}
		if errors.Is(cctx.Err(), context.Canceled) {
			return nil, stderr, &CallError{Code: CodeNetwork, Message: "插件调用被取消"}
		}
		return nil, stderr, &CallError{
			Code:    CodeOther,
			Message: fmt.Sprintf("插件宿主执行失败: %v", err),
		}
	}
	return outW.Bytes(), stderr, nil
}

// jsonLine 取 stdout 里最后一行合法 JSON。
//
// 宿主已经保证 stdout 只有一行，但插件若在重定向生效前就 print（模块顶层代码），
// 或者解释器自己打了警告，这里要能容忍。
func jsonLine(out []byte) []byte {
	b := bytes.TrimSpace(out)
	if len(b) == 0 {
		return b
	}
	if b[0] == '{' && json.Valid(b) {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		ln := bytes.TrimSpace(lines[i])
		if len(ln) > 0 && ln[0] == '{' && json.Valid(ln) {
			return ln
		}
	}
	return b
}

func stderrSuffix(stderr string) string {
	if strings.TrimSpace(stderr) == "" {
		return ""
	}
	return "（stderr: " + truncate(stderr, 400) + "）"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// limitedWriter 带字节上限的缓冲：超限后丢弃多余内容但返回 len(p)，
// 避免子进程因短写而报错（与 tools 包的同名结构同一考虑）。
type limitedWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedWriter(limit int) *limitedWriter { return &limitedWriter{limit: limit} }

func (w *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.limit <= 0 { // <=0 表示不限
		return w.buf.Write(p)
	}
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return n, nil
	}
	if n > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return n, nil
	}
	w.buf.Write(p)
	return n, nil
}

func (w *limitedWriter) Bytes() []byte  { return w.buf.Bytes() }
func (w *limitedWriter) String() string { return w.buf.String() }
