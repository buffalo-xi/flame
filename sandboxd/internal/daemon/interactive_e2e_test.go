package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"flame/tools/sandbox"
)

// 长交互端点的端到端验收：真 sandboxd 服务 + 真 WebSocket + 真 docker。
//
// 验证 docker 模式下的核心语义：
//   - 首次 open 懒建容器（sandboxd=1 复用单元），进程 docker exec 进去；
//   - 客户端断开不杀进程、不销毁容器；
//   - 再次 open 复用同一容器（容器数不增加），且进程状态跨连接保留；
//   - FrameKill 杀进程并把变空的 session 的容器一并销毁。
//
// 需要本机跑着 sandboxd（缺省 127.0.0.1:27081，可用 SANDBOXD_TEST_INTERACTIVE_URL 覆盖）
// 与可用的 docker（SANDBOXD_TEST_DOCKER），否则跳过。
// 复用单元的容器标签刻意硬编码 sandboxd=1（不引用 isolate 包常量），
// 与 exec_e2e_test.go 的 sandboxd-exec=1 同理 —— 端到端测的是对外约定。

const (
	defaultInteractiveURL = "ws://127.0.0.1:27081/v1/interactive"
	interactiveUnitLabel  = "sandboxd=1"
)

// interactiveE2E 一次端到端验收需要的环境。
type interactiveE2E struct {
	url  string
	bin  string
	base string
}

func interactiveE2EForTest(t *testing.T) interactiveE2E {
	t.Helper()

	url := os.Getenv("SANDBOXD_TEST_INTERACTIVE_URL")
	if url == "" {
		url = defaultInteractiveURL
	}
	base := strings.NewReplacer("ws://", "http://", "wss://", "https://").Replace(url)
	if i := strings.Index(base, "/v1/"); i > 0 {
		base = base[:i]
	}

	bin := os.Getenv("SANDBOXD_TEST_DOCKER")
	if bin == "" {
		bin = "docker"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("no docker binary (%s); set SANDBOXD_TEST_DOCKER to enable", bin)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("sandboxd not reachable at %s (%v); set SANDBOXD_TEST_INTERACTIVE_URL to enable", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("sandboxd /healthz returned %d at %s", resp.StatusCode, base)
	}
	return interactiveE2E{url: url, bin: bin, base: base}
}

// openInteractive 打开一次长交互连接，等到 ready 帧。argv 非空表示首次创建（带 Spec）。
func openInteractive(t *testing.T, url, sessionID, toolID string, argv []string) *sandbox.Conn {
	t.Helper()
	conn, err := sandbox.Dial(context.Background(), url, "")
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	f := &sandbox.Frame{Type: sandbox.FrameOpen, SessionID: sessionID, ToolID: toolID}
	if argv != nil {
		f.Spec = (&sandbox.Spec{Argv: argv}).Wire()
	}
	if err := conn.SendFrame(f); err != nil {
		_ = conn.Close()
		t.Fatalf("send open: %v", err)
	}
	rf, _, err := conn.Recv()
	if err != nil {
		_ = conn.Close()
		t.Fatalf("wait ready: %v", err)
	}
	if rf.Type != sandbox.FrameReady {
		_ = conn.Close()
		t.Fatalf("期望 ready 帧，实收 %q（%s）", rf.Type, rf.Message)
	}
	return conn
}

// readStdoutUntil 读 ChanStdout 帧，直到累积内容包含 want（进程长驻，stdout 不会自然结束）。
func readStdoutUntil(conn *sandbox.Conn, want string, limit time.Duration) error {
	type result struct{ err error }
	ch := make(chan result, 1)
	go func() {
		var sb strings.Builder
		for {
			f, data, err := conn.Recv()
			if err != nil {
				ch <- result{err: err}
				return
			}
			if data != nil {
				if data[0] == sandbox.ChanStdout {
					sb.Write(data[1:])
					if strings.Contains(sb.String(), want) {
						ch <- result{}
						return
					}
				}
				continue
			}
			if f.Type == sandbox.FrameError {
				ch <- result{err: fmt.Errorf("sandboxd: %s", f.Message)}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.err
	case <-time.After(limit):
		return fmt.Errorf("等 stdout 包含 %q 超时（%s）", want, limit)
	}
}

// interactiveContainerNames 当前带复用单元标签（sandboxd=1）的容器名集合。
func interactiveContainerNames(t *testing.T, bin string) map[string]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "ps", "-a", "--filter", "label="+interactiveUnitLabel,
		"--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	set := make(map[string]struct{})
	for _, n := range strings.Fields(string(out)) {
		set[n] = struct{}{}
	}
	return set
}

func waitInteractiveContainer(t *testing.T, bin string, before map[string]struct{}) string {
	t.Helper()
	deadline := time.Now().Add(containerWait)
	for time.Now().Before(deadline) {
		for name := range interactiveContainerNames(t, bin) {
			if _, existed := before[name]; !existed {
				return name
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("这次调用没有留下可观测的复用单元容器")
	return ""
}

func waitInteractiveContainersGone(t *testing.T, bin string, names []string) {
	t.Helper()
	deadline := time.Now().Add(containerWait)
	var left []string
	for time.Now().Before(deadline) {
		now := interactiveContainerNames(t, bin)
		left = left[:0]
		for _, n := range names {
			if _, ok := now[n]; ok {
				left = append(left, n)
			}
		}
		if len(left) == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("容器 %v 在 %s 内没有消失", left, containerWait)
}

func dockerRemove(t *testing.T, bin, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, bin, "rm", "-f", name).Run()
}

// 断连不杀进程、复用同一容器、进程状态跨连接保留。
func TestInteractiveE2EReusesProcessAcrossConnections(t *testing.T) {
	e := interactiveE2EForTest(t)
	before := interactiveContainerNames(t, e.bin)
	sid := "e2e-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const tool = "shell"

	// 首次：带 argv 创建进程，喂命令，验证 stdout 回传
	c1 := openInteractive(t, e.url, sid, tool, []string{"/bin/sh"})
	if err := c1.SendData(sandbox.ChanStdin, []byte("export FOO=interactive-bar\n")); err != nil {
		t.Fatalf("send export: %v", err)
	}
	if err := c1.SendData(sandbox.ChanStdin, []byte("echo alive-e2e\n")); err != nil {
		t.Fatalf("send echo: %v", err)
	}
	if err := readStdoutUntil(c1, "alive-e2e", containerWait); err != nil {
		t.Fatalf("first echo: %v", err)
	}
	_ = c1.Close() // 断连，进程保留

	// 容器应已出现且断连后仍在
	name := waitInteractiveContainer(t, e.bin, before)
	t.Cleanup(func() { dockerRemove(t, e.bin, name) })

	// 再次：不带 argv，复用同一进程，读回上一次 export 的变量
	c2 := openInteractive(t, e.url, sid, tool, nil)
	if err := c2.SendData(sandbox.ChanStdin, []byte("echo $FOO\n")); err != nil {
		t.Fatalf("send readback: %v", err)
	}
	if err := readStdoutUntil(c2, "interactive-bar", containerWait); err != nil {
		t.Fatalf("readback: %v", err)
	}
	_ = c2.Close()

	// 复用不新增容器
	now := interactiveContainerNames(t, e.bin)
	if _, ok := now[name]; !ok {
		t.Errorf("复用后容器 %s 应仍在", name)
	}
	if len(now) != len(before)+1 {
		t.Errorf("复用不该新增容器：before=%d now=%d", len(before), len(now))
	}
}

// FrameKill 杀进程，并把变空的 session 的容器一并销毁。
func TestInteractiveE2EKillDestroysContainer(t *testing.T) {
	e := interactiveE2EForTest(t)
	before := interactiveContainerNames(t, e.bin)
	sid := "e2e-kill-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	c := openInteractive(t, e.url, sid, "shell", []string{"/bin/sh"})
	name := waitInteractiveContainer(t, e.bin, before)

	if err := c.SendFrame(&sandbox.Frame{Type: sandbox.FrameKill}); err != nil {
		t.Fatalf("send kill: %v", err)
	}
	waitInteractiveContainersGone(t, e.bin, []string{name})
	_ = c.Close()
}
