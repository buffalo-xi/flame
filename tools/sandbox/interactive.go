package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// interactiveIdle 一段输出后多久没有新数据就认为命令结束。
//
// 这是「哨兵」的兜底：正常情况下命令末尾会 echo 一个唯一哨兵，读到它即知结束；
// 但脚本可能把哨兵行一并吞掉（重定向、exec 替换等），此时靠空闲超时收尾，
// 避免一次调用永久挂住。
const interactiveIdle = 30 * time.Second

// InteractiveSession 一次长交互调用的会话身份。
//
// Key 是贯穿三端的复用标识：nexus 用它定位进程、sandboxg 用它做粘性路由、
// sandboxd 用它查 sessionSandbox。有 session 时取 SessionID，无 session 时取 RunID。
type InteractiveSession struct {
	Key       string
	Ephemeral bool // 无 session 的临时进程：sandboxd 用短超时兜底
}

// InteractiveResult 一次交互的结果。
type InteractiveResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Completed bool // false = 没读到哨兵（已按空闲超时返回，命令可能未结束）
}

// InteractiveClient 长交互进程客户端（/v1/interactive）。
//
// 每次调用 = 开一条短 WS：open → 喂命令（带哨兵）→ 读输出到哨兵/空闲超时 → 断开。
// 进程生命周期归 sandboxd（断开不杀，按 session_id 复用），本侧不持有任何状态。
type InteractiveClient struct {
	endpoint string
	token    string
	limits   Limits
}

// NewInteractive 创建长交互客户端。endpoint 形如 ws://sandboxd:8080/v1/interactive。
func NewInteractive(endpoint, token string, limits Limits) *InteractiveClient {
	return &InteractiveClient{endpoint: endpoint, token: token, limits: limits}
}

// Exec 在会话里执行一条命令。
//
// argv 只在进程首次创建时生效（sandboxd 复用已有进程时忽略它）；command 通过 stdin 喂入。
func (c *InteractiveClient) Exec(ctx context.Context, sess InteractiveSession, toolID string,
	argv []string, command string, mounts []Mount, dir string) (InteractiveResult, error) {

	if len(argv) == 0 {
		return InteractiveResult{}, fmt.Errorf("sandbox: interactive requires argv")
	}

	conn, err := Dial(ctx, c.endpoint, c.token)
	if err != nil {
		return InteractiveResult{}, err
	}
	defer conn.Close()

	if err := conn.SendFrame(&Frame{
		Type:      FrameOpen,
		SessionID: sess.Key,
		ToolID:    toolID,
		Ephemeral: sess.Ephemeral,
		Spec:      (&Spec{Argv: argv, Dir: dir, Mounts: mounts, Limits: c.limits}).Wire(),
	}); err != nil {
		return InteractiveResult{}, err
	}

	f, _, err := conn.Recv()
	if err != nil {
		return InteractiveResult{}, fmt.Errorf("sandbox: interactive ready: %w", err)
	}
	switch f.Type {
	case FrameReady:
	case FrameError:
		return InteractiveResult{}, fmt.Errorf("sandbox: interactive: %s", f.Message)
	default:
		return InteractiveResult{}, fmt.Errorf("sandbox: unexpected frame %q (want ready)", f.Type)
	}

	sentinel := "__SH_DONE_" + randHex(8) + "__"
	if err := conn.SendData(ChanStdin, []byte(wrapWithSentinel(command, sentinel, argv[0])+"\n")); err != nil {
		return InteractiveResult{}, err
	}

	return readToSentinel(ctx, conn, sentinel, interactiveIdle)
}

// Do 在长交互进程上发一行 JSON、读回一行 JSON。
//
// 与 Exec 的区别只有「结束条件」：shell 靠哨兵判断一条命令跑完，而浏览器宿主
// 是严格的「一问一答」—— 发一行、收一行，不需要包哨兵。
//
// 按 id 匹配是必要的容错：上一次调用超时后，它的响应可能在下一次调用时才到达；
// 若把它当成这次的结果，模型会拿到一个完全错位的页面状态。反过来说，正因为
// 能按 id 丢弃迟到响应，超时之后**不必杀掉浏览器**（cookie / 标签页都还在）。
func (c *InteractiveClient) Do(ctx context.Context, sess InteractiveSession, toolID string,
	argv []string, req []byte, mounts []Mount, dir string) ([]byte, error) {

	if len(argv) == 0 {
		return nil, fmt.Errorf("sandbox: interactive requires argv")
	}
	conn, err := Dial(ctx, c.endpoint, c.token)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.SendFrame(&Frame{
		Type:      FrameOpen,
		SessionID: sess.Key,
		ToolID:    toolID,
		Ephemeral: sess.Ephemeral,
		Spec:      (&Spec{Argv: argv, Dir: dir, Mounts: mounts, Limits: c.limits}).Wire(),
	}); err != nil {
		return nil, err
	}

	f, _, err := conn.Recv()
	if err != nil {
		return nil, fmt.Errorf("sandbox: interactive ready: %w", err)
	}
	switch f.Type {
	case FrameReady:
	case FrameError:
		return nil, fmt.Errorf("sandbox: %s", f.Message)
	default:
		return nil, fmt.Errorf("sandbox: unexpected frame %q (want ready)", f.Type)
	}

	var wantID int
	var probe struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(req, &probe); err == nil {
		wantID = probe.ID
	}

	if err := conn.SendData(ChanStdin, append(append([]byte{}, req...), '\n')); err != nil {
		return nil, err
	}
	return readJSONLine(ctx, conn, wantID)
}

// readJSONLine 从连接里读出一行 JSON（按 id 匹配）。
//
// 缓冲区有上限：浏览器宿主的响应本来就有 max_bytes 预算，超过这个量级
// 只可能是协议坏了，继续攒下去会把 nexus 的内存吃掉。
func readJSONLine(ctx context.Context, conn *Conn, wantID int) ([]byte, error) {
	const maxLine = 8 << 20

	type outcome struct {
		line []byte
		err  error
	}
	done := make(chan outcome, 1)

	go func() {
		var buf []byte
		for {
			f, data, err := conn.Recv()
			if err != nil {
				done <- outcome{err: err}
				return
			}
			if data != nil {
				if data[0] != ChanStdout {
					continue // stderr 不参与协议
				}
				buf = append(buf, data[1:]...)
				for {
					i := bytes.IndexByte(buf, '\n')
					if i < 0 {
						break
					}
					line := bytes.TrimSpace(buf[:i])
					buf = buf[i+1:]
					if len(line) == 0 {
						continue
					}
					if wantID > 0 {
						var env struct {
							ID int `json:"id"`
						}
						if json.Unmarshal(line, &env) == nil && env.ID != wantID {
							continue // 上次调用迟到的响应，丢掉
						}
					}
					done <- outcome{line: append([]byte{}, line...)}
					return
				}
				if len(buf) > maxLine {
					done <- outcome{err: fmt.Errorf("sandbox: response line exceeds %d bytes", maxLine)}
					return
				}
				continue
			}
			switch f.Type {
			case FrameError:
				done <- outcome{err: fmt.Errorf("sandbox: %s", f.Message)}
				return
			}
		}
	}()

	select {
	case r := <-done:
		return r.line, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Kill 显式销毁会话进程（run 结束快路径，或工具的 kill 参数）。
//
// 幂等：进程不存在时不会真的创建（open 不带 Spec，空 argv 会让 spawn 直接失败），
// 存在则被 removeTool 杀掉。
func (c *InteractiveClient) Kill(ctx context.Context, sess InteractiveSession, toolID string) error {
	conn, err := Dial(ctx, c.endpoint, c.token)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.SendFrame(&Frame{
		Type:      FrameOpen,
		SessionID: sess.Key,
		ToolID:    toolID,
		Ephemeral: sess.Ephemeral,
	}); err != nil {
		return err
	}

	// 等 ready：进程存在才会 ready；不存在时服务端回 error —— 幂等，直接当已销毁。
	f, _, err := conn.Recv()
	if err != nil {
		return nil // 连接已断，视为已销毁
	}
	if f.Type != FrameReady {
		return nil
	}
	if err := conn.SendFrame(&Frame{Type: FrameKill}); err != nil {
		return err
	}
	// 等服务端 removeTool 后关闭连接（读到错误/关闭即处理完毕）。
	_, _, _ = conn.Recv()
	return nil
}

// wrapWithSentinel 把命令包成「执行 + 输出唯一哨兵 + 退出码」，按 shell 类型选分隔符。
func wrapWithSentinel(command, sentinel, shellBin string) string {
	base := strings.ToLower(filepath.Base(shellBin))
	switch {
	case base == "cmd" || base == "cmd.exe":
		// 分成两条物理行：cmd 解析整行时就会把 %errorlevel% 展开掉，写在同一行拿到
		// 的是「上一条命令」的退出码 —— 症状是任何命令都报 0。分行喂，第二行在
		// 第一行执行完才被解析，%errorlevel% 才是本条命令的。
		return command + "\necho " + sentinel + "%errorlevel%"
	case strings.HasPrefix(base, "powershell") || base == "pwsh":
		return command + "; echo \"" + sentinel + "$LASTEXITCODE\""
	default: // bash / sh / zsh ...
		return command + "; echo " + sentinel + "$?"
	}
}

// readToSentinel 读 stdout 直到哨兵出现；idle 内无新输出则兜底返回。
//
// 哨兵前的内容是命令输出，哨兵后紧跟退出码；stderr 单独收集、不参与判定。
func readToSentinel(ctx context.Context, conn *Conn, sentinel string, idle time.Duration) (InteractiveResult, error) {
	var (
		mu     sync.Mutex
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	found := make(chan sentinelHit, 1) // 哨兵命中：退出码 + 位置
	readErr := make(chan error, 1)
	activity := make(chan struct{}, 1) // 有输出（用于重置空闲计时）

	go func() {
		for {
			f, data, err := conn.Recv()
			if err != nil {
				readErr <- err
				return
			}
			if data != nil {
				switch data[0] {
				case ChanStdout:
					mu.Lock()
					stdout.Write(data[1:])
					hit := parseSentinel(stdout.Bytes(), sentinel)
					mu.Unlock()
					if hit.ok {
						found <- hit
						return
					}
				case ChanStderr:
					mu.Lock()
					stderr.Write(data[1:])
					mu.Unlock()
				}
				select {
				case activity <- struct{}{}:
				default:
				}
				continue
			}
			switch f.Type {
			case FrameExit:
				// 进程退出（如命令是 exit）：以已读输出收尾
				mu.Lock()
				hit := parseSentinel(stdout.Bytes(), sentinel)
				mu.Unlock()
				if !hit.ok {
					hit = sentinelHit{code: f.Code}
				}
				found <- hit
				return
			case FrameError:
				readErr <- fmt.Errorf("sandbox: interactive: %s", f.Message)
				return
			}
		}
	}()

	timer := time.NewTimer(idle)
	defer timer.Stop()

	snapshot := func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		return stdout.String(), stderr.String()
	}

	for {
		select {
		case hit := <-found:
			out, errOut := snapshot()
			if hit.ok {
				out = stripSentinelAt(out, hit.at)
			}
			return InteractiveResult{
				Stdout:    out,
				Stderr:    errOut,
				ExitCode:  hit.code,
				Completed: true,
			}, nil
		case err := <-readErr:
			out, errOut := snapshot()
			return InteractiveResult{Stdout: out, Stderr: errOut, ExitCode: -1}, err
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		case <-timer.C:
			// 没解析到哨兵：输出原样返回（此时若去"删哨兵行"，最容易删掉的是
			// cmd 回显出来的那行命令，把真正的输出也带走了）。
			out, errOut := snapshot()
			return InteractiveResult{
				Stdout:    out,
				Stderr:    errOut,
				ExitCode:  -1,
				Completed: false,
			}, nil
		case <-ctx.Done():
			out, errOut := snapshot()
			return InteractiveResult{Stdout: out, Stderr: errOut, ExitCode: -1}, ctx.Err()
		}
	}
}

// sentinelHit 一次哨兵命中：退出码 + 哨兵在输出中的字节位置（删行时要用同一处）。
type sentinelHit struct {
	code int
	at   int
	ok   bool
}

// parseSentinel 在输出里找哨兵并解析其后的退出码。
// 哨兵已出现但退出码还没到（分片）时返回 ok=false，由调用方继续累积。
//
// 为什么不能只认第一次出现：cmd 会把喂进去的整行原样回显（管道下默认 echo on），
// 于是输出里第一处哨兵来自回显 —— 它后面跟的是未展开的 %errorlevel%，不是数字。
// 只认第一次会让 Windows 上的每次 shell 调用都等到空闲超时才返回（Completed=false,
// exit=-1），看起来像"命令卡住了"。PowerShell/bash 不回声，这里也不会因此多做事。
//
// 判据改成「后面能解析出整数的那一处」：回显处跳过，真正的输出处命中。
func parseSentinel(b []byte, sentinel string) sentinelHit {
	needle := []byte(sentinel)
	for off := 0; off <= len(b); {
		idx := bytes.Index(b[off:], needle)
		if idx < 0 {
			return sentinelHit{}
		}
		idx += off
		rest := b[idx+len(needle):]
		if end := bytes.IndexByte(rest, '\n'); end >= 0 {
			rest = rest[:end]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(rest))); err == nil {
			return sentinelHit{code: n, at: idx, ok: true}
		}
		off = idx + len(needle)
	}
	return sentinelHit{}
}

// stripSentinelAt 删掉命中位置 at 所在的整行（哨兵及其后的退出码），保留其余输出。
// at 必须来自 parseSentinel —— 行可能以提示符/回显开头，重新搜一遍会删错行。
func stripSentinelAt(s string, at int) string {
	if at < 0 || at > len(s) {
		return s
	}
	lineStart := strings.LastIndexByte(s[:at], '\n') + 1
	lineEnd := strings.IndexByte(s[at:], '\n')
	if lineEnd < 0 {
		return s[:lineStart]
	}
	return s[:lineStart] + s[at+lineEnd+1:]
}

// randHex 生成 n 字节随机数的十六进制串（哨兵唯一化）。
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
