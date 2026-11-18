// Package codehost 实现 sandboxd 的脚本执行宿主：一个语言无关的常驻进程，
// 跑在隔离单元内部（nsjail / none），替 sandboxd 逐次 fork 全新解释器进程执行脚本。
//
// 与旧 worker.lua/py/js 的区别：
//   - 一份 Go 代码 fork 三种解释器（lua / python / node），不再有三份语言 worker；
//   - 每次 execute 都 fork 全新解释器，进程级干净，天然没有跨请求状态残留；
//   - 协议退化成单向 execute / done / error（砍掉 callback / callback_resp 交互），
//     脚本的 print / log 一律走 stderr，stdout 只留给结构化结果。
package codehost

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// maxMsgSize 单条协议帧上限，与 daemon/protocol.go 保持一致。
	maxMsgSize = 10 * 1024 * 1024
	// maxStderrCapture 回传 logs 时留存解释器 stderr 的尾部上限（转发不受此限）。
	// 也用作 reply 的错误回退（lastNonEmptyLine）来源。
	maxStderrCapture = 64 * 1024
	// killWaitDelay 杀进程后等待 I/O 管道收尾的上限：解释器的 stderr 可能被
	// 逃逸的孙进程攥着，Wait 最多等这么久，避免永久卡住。
	killWaitDelay = 5 * time.Second
)

// request 是 host → codehost 的一帧。
type request struct {
	Action    string          `json:"action"`
	Lang      string          `json:"lang"`
	Command   string          `json:"command"`
	Script    string          `json:"script"`
	Inputs    json.RawMessage `json:"inputs"`
	TimeoutMS int64           `json:"timeout_ms"`
}

// Host 一个 codehost 实例：持有 stdin/stdout（协议通道）与 stderr（解释器 stderr 的
// 转发目的地）。stdin/stdout/stderr 全部注入，便于测试。
//
// 注意 stderr 的去向：sandboxd 侧接的是 io.Discard（见 stderrCollector 的说明），
// 脚本 print 是靠协议帧里的 logs 返回给 nexus 的，不是靠这条路。
type Host struct {
	in     io.Reader
	out    io.Writer
	stderr io.Writer
}

// Run 以默认宿主环境跑 codehost 主循环。
func Run(in io.Reader, out, errW io.Writer) error {
	return (&Host{in: in, out: out, stderr: errW}).run()
}

// run 主循环：读一帧、执行一次、回一帧。stdin 读到 EOF（sandboxd 关闭连接）即退出。
//
// 协议里的帧只有 execute（进）/ done、error（出），外加两个控制帧：
//   - ping / pong —— liveness 探针，供 sandboxd 的池做借出/归还/巡检探活。
//     它不是 callback 那种「双向交互」，只是确认宿主进程还活着。
//   - shutdown —— 优雅收尾。
func (h *Host) run() error {
	for {
		req, err := h.readRequest()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		switch req.Action {
		case "execute":
			h.execute(req)
		case "ping":
			_ = h.writeJSON(map[string]string{"action": "pong"})
		case "shutdown":
			return nil
		default:
			h.sendError("unknown action: "+req.Action, "", false)
		}
	}
}

// execute 执行一次：按 lang 选中模板 → 织成完整脚本 → 写盘 → fork 解释器 →
// 收集结果（stdout）与 print 输出（stderr 转发）→ 回 done / error。
//
// 僵尸回收：解释器是唯一被 Wait 的子进程，无论正常退出还是超时强杀都会 Wait 一次，
// 所以不会积累僵尸。killTree 顺带覆盖它自己 fork 的后代（见 proc_*.go）。
func (h *Host) execute(req *request) {
	if req.Command == "" {
		h.sendError("execute: empty command", "", false)
		return
	}
	tmpl, ok := templates[req.Lang]
	if !ok {
		h.sendError("unsupported language: "+req.Lang, "", false)
		return
	}

	src := tmpl.build(req.Script, req.Inputs)

	// 脚本临时文件写在 cwd（"."），而不是 os.TempDir()：sandboxd 会把 codehost 的
	// cwd 设成本次会话独占的可写目录，脚本与其临时产物都落在这里，随会话结束一并清掉，
	// 不会泄漏到宿主 /tmp。
	f, err := os.CreateTemp(".", "codehost-*"+tmpl.ext)
	if err != nil {
		h.sendError("codehost: create temp script: "+err.Error(), "", false)
		return
	}
	fname := f.Name()
	_, werr := f.WriteString(src)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(fname)
		if werr != nil {
			h.sendError("codehost: write temp script: "+werr.Error(), "", false)
		} else {
			h.sendError("codehost: close temp script: "+cerr.Error(), "", false)
		}
		return
	}
	defer os.Remove(fname)

	// 超时：0 / 未写 = 不限时（standalone 调试）；接入后由 sandboxd 逐次下发。
	ctx := context.Background()
	cancel := context.CancelFunc(nil)
	if req.TimeoutMS > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, req.Command, fname)

	var stdout cappedBuffer
	stdout.max = maxMsgSize
	cmd.Stdout = &stdout

	col := &stderrCollector{dst: h.stderr, max: maxStderrCapture}
	cmd.Stderr = col

	setProcessGroup(cmd)
	// 内存上限由隔离层（nsjail cgroup v2 的 memory.max）统一施加，解释器不再单独设
	// RLIMIT_AS —— VSZ 上限会误杀 node/V8 这类预留大量虚拟地址空间的运行时。
	// 关键：ctx 超时 / 取消时走 killTree（整组），而不是默认的只杀单 pid ——
	// 脚本可能 fork 后代，只杀解释器会留下逃逸进程。
	cmd.Cancel = func() error { return killTree(cmd) }
	cmd.WaitDelay = killWaitDelay

	if err := cmd.Start(); err != nil {
		h.sendError("codehost: start "+req.Command+": "+err.Error(), "", false)
		return
	}

	waitErr := cmd.Wait()

	// 超时优先于其它判定：ctx 被 DeadlineExceeded 触发，脚本一定没跑完。
	// 超时也带 logs：脚本可能已经 print 了一部分，带回对排障有用。
	if ctx.Err() == context.DeadlineExceeded {
		h.sendError(fmt.Sprintf("script execution timed out after %dms", req.TimeoutMS), col.String(), col.truncated)
		return
	}

	h.reply(stdout.Bytes(), waitErr, req.Command, col)
}

// reply 解析解释器 stdout（{"result": X} / {"error": msg} / {}）并回帧。
//
// stdout 兜底：解析不出 JSON 时，用退出码 + stderr 尾部拼一条错误，覆盖「脚本语法错误
// 让整份文件解析失败」「解释器崩溃」这类模板抓不到的路径。
func (h *Host) reply(stdout []byte, waitErr error, command string, col *stderrCollector) {
	logs := col.String()
	var interp map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &interp); err == nil {
		if r, ok := interp["result"]; ok {
			h.sendDone(r, logs, col.truncated)
			return
		}
		if e, ok := interp["error"]; ok {
			var msg string
			_ = json.Unmarshal(e, &msg)
			if msg == "" {
				msg = string(e)
			}
			h.sendError(msg, logs, col.truncated)
			return
		}
		// 既无 result 也无 error（如模板里写出的 "{}"）：当作无返回值完成。
		h.sendDone(nil, logs, col.truncated)
		return
	}

	if waitErr == nil {
		// 正常退出却读不到结构化结果：模板 bug 之类的异常，如实报出来。
		h.sendError("codehost: "+command+" produced no parseable result", logs, col.truncated)
		return
	}
	msg := lastNonEmptyLine(logs)
	if msg == "" {
		msg = fmt.Sprintf("codehost: %s exited with error", command)
	}
	h.sendError(msg, logs, col.truncated)
}

// ---------- 协议帧 ----------

func (h *Host) readRequest() (*request, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(h.in, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxMsgSize {
		return nil, fmt.Errorf("codehost: message too large: %d bytes", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(h.in, body); err != nil {
		return nil, err
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("codehost: decode request: %w", err)
	}
	return &req, nil
}

func (h *Host) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(data)))
	if _, err := h.out.Write(header); err != nil {
		return err
	}
	_, err = h.out.Write(data)
	return err
}

func (h *Host) sendDone(result json.RawMessage, logs string, trunc bool) {
	_ = h.writeJSON(doneMsg{Action: "done", Result: result, Logs: logs, Trunc: trunc})
}

func (h *Host) sendError(msg, logs string, trunc bool) {
	_ = h.writeJSON(errorMsg{Action: "error", Error: msg, Logs: logs, Trunc: trunc})
}

type doneMsg struct {
	Action string          `json:"action"`
	Result json.RawMessage `json:"result,omitempty"`
	Logs   string          `json:"logs,omitempty"`          // 脚本 stderr 全文（print 输出，可能截断）
	Trunc  bool            `json:"logs_truncated,omitempty"` // logs 是否因超限被截断
}

type errorMsg struct {
	Action string          `json:"action"`
	Error  string          `json:"error"`
	Logs   string          `json:"logs,omitempty"`          // 错误时的完整 stderr（含 traceback / stack）
	Trunc  bool            `json:"logs_truncated,omitempty"` // logs 是否因超限被截断
}

// ---------- 输出收集 ----------

// cappedBuffer 有上限地收集解释器 stdout。超出的部分照常返回写入长度（让 os/exec 的
// 复制 goroutine 不报错），但不再落盘 —— 结果进协议帧，本就有 10MB 上限。
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.max > 0 {
		remain := b.max - b.buf.Len()
		if remain > 0 {
			if len(p) > remain {
				b.buf.Write(p[:remain])
			} else {
				b.buf.Write(p)
			}
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// stderrCollector 把解释器 stderr 转发给 codehost 自己的 stderr，同时尾部留存 max 字节
// 供 logs 回传，超限置 truncated。
//
// 转发目的地要说清：sandboxd 起 codehost 时把它的 stderr 接到 io.Discard（见
// daemon/worker.go 的 spawnCodehost），所以这段输出**不会**进服务日志。
// 脚本 print 真正回传给 nexus 的是下面那个 tail（协议帧的 logs 字段，尾部 64KB）；
// 保留 dst 是为了 codehost 的单测与 standalone 调试能直接看到输出。
//
// 为什么留尾部而不是前缀：错误 traceback / 最近的 print 都在尾部，回传给 nexus 的
// logs 要的是「最后发生了什么」；且 lastNonEmptyLine 靠尾部才能拿到真正的最后一行。
type stderrCollector struct {
	dst       io.Writer
	max       int
	tail      []byte
	truncated bool
}

func (c *stderrCollector) Write(p []byte) (int, error) {
	if c.dst != nil {
		_, _ = c.dst.Write(p)
	}
	if c.max > 0 && len(p) > 0 {
		c.tail = append(c.tail, p...)
		if len(c.tail) > c.max {
			c.tail = c.tail[len(c.tail)-c.max:]
			c.truncated = true
		}
	}
	return len(p), nil
}

func (c *stderrCollector) String() string { return string(c.tail) }

func lastNonEmptyLine(s string) string {
	s = strings.TrimRight(s, "\r\n")
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
