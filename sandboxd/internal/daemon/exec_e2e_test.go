package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"flame/tools/sandbox"
)

// 单工端点的端到端验收：真 sandboxd 服务 + 真 WebSocket + 真 docker。
//
// 需要本机跑着 sandboxd（缺省 127.0.0.1:27081，可用 SANDBOXD_TEST_EXEC_URL 覆盖）
// 与可用的 docker（SANDBOXD_TEST_DOCKER），否则跳过。
//
// 断言的对象是**对外约定**：一次执行结束后，它为这次执行创建的容器必须消失 ——
// 容器怎么命名、打什么标签见 nexus/docs/sandbox-design.md §9.6。
// 这里刻意硬编码标签而不引用 isolate 包的常量：端到端测的是约定，
// 不该跟着实现内部的常量名一起改。

const (
	defaultExecURL     = "ws://127.0.0.1:27081/v1/exec"
	execContainerLabel = "sandboxd-exec=1"
	containerWait      = 20 * time.Second
)

// execE2E 一次端到端验收需要的环境。
type execE2E struct {
	url  string
	bin  string // docker 可执行文件
	base string // HTTP 基地址（/healthz 用）
}

// execE2EForTest 探测环境：服务不可达或没有 docker 就跳过。
func execE2EForTest(t *testing.T) execE2E {
	t.Helper()

	url := os.Getenv("SANDBOXD_TEST_EXEC_URL")
	if url == "" {
		url = defaultExecURL
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
		t.Skipf("sandboxd not reachable at %s (%v); set SANDBOXD_TEST_EXEC_URL to enable", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("sandboxd /healthz returned %d at %s", resp.StatusCode, base)
	}
	return execE2E{url: url, bin: bin, base: base}
}

// TestExecE2EDisconnectKillsContainer 客户端断链之后，这次执行的容器必须消失。
//
// 这正是 nexus 侧超时 / 进程消失时的形态：不发任何帧，直接断。
func TestExecE2EDisconnectKillsContainer(t *testing.T) {
	e := execE2EForTest(t)
	before := execContainerNames(t, e.bin)

	spec := &sandbox.Spec{Argv: []string{"/bin/sh", "-c", "sleep 300 & sleep 300"}}
	conn := openExec(t, e, spec)

	// 先确认容器真的起来了，否则后面的断言可能只是在测「它压根没跑」。
	// 后台 sleep 攥着 stdout、前台 sleep 撑住 shell —— 一棵进程树。
	appeared := waitNewContainer(t, e.bin, before)

	// 断链：直接关掉 WS，不发送任何帧。
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 核心断言：容器（连同里面那棵进程树）必须被收掉。
	waitContainersGone(t, e.bin, []string{appeared})

	// 服务不能被这次断链拖住。
	assertHealthy(t, e.base)
}

// TestExecE2EFrameKillKillsContainer 显式 kill 帧同样要收干净（连接还活着的那条路）。
func TestExecE2EFrameKillKillsContainer(t *testing.T) {
	e := execE2EForTest(t)
	before := execContainerNames(t, e.bin)

	spec := &sandbox.Spec{Argv: []string{"/bin/sh", "-c", "sleep 300 & sleep 300"}}
	conn := openExec(t, e, spec)
	defer func() { _ = conn.Close() }()

	appeared := waitNewContainer(t, e.bin, before)

	if err := conn.SendFrame(&sandbox.Frame{Type: sandbox.FrameKill}); err != nil {
		t.Fatalf("send kill: %v", err)
	}
	waitContainersGone(t, e.bin, []string{appeared})
}

// TestExecE2ENormalExit 正常跑完的命令：输出与退出码要对，且不留下容器。
//
// 容器可能跑得比 docker ps 还快（--rm 已经删了），所以这里不做「必须看见容器」
// 的断言，只断言「没有多出来的容器」—— 这个判据与快慢无关。
func TestExecE2ENormalExit(t *testing.T) {
	e := execE2EForTest(t)
	before := execContainerNames(t, e.bin)

	spec := &sandbox.Spec{Argv: []string{"/bin/sh", "-c", "echo e2e-ok"}}
	conn := openExec(t, e, spec)
	defer func() { _ = conn.Close() }()

	out, code, err := readExit(conn, containerWait)
	if err != nil {
		t.Fatalf("read exit: %v", err)
	}
	if !strings.Contains(out, "e2e-ok") {
		t.Errorf("stdout = %q，期望包含 e2e-ok", out)
	}
	if code != 0 {
		t.Errorf("exit code = %d，期望 0", code)
	}
	assertNoNewContainers(t, e.bin, before)
}

// openExec 打开一次单工执行，等到 ready 帧。
func openExec(t *testing.T, e execE2E, spec *sandbox.Spec) *sandbox.Conn {
	t.Helper()
	conn, err := sandbox.Dial(context.Background(), e.url, "")
	if err != nil {
		t.Fatalf("dial %s: %v", e.url, err)
	}
	if err := conn.SendFrame(&sandbox.Frame{Type: sandbox.FrameOpen, Spec: spec.Wire()}); err != nil {
		_ = conn.Close()
		t.Fatalf("send open: %v", err)
	}
	f, _, err := conn.Recv()
	if err != nil {
		_ = conn.Close()
		t.Fatalf("wait ready: %v", err)
	}
	if f.Type != sandbox.FrameReady {
		_ = conn.Close()
		t.Fatalf("期望 ready 帧，实收 %q（%s）", f.Type, f.Message)
	}
	return conn
}

// readExit 读到 exit 帧为止，收集 stdout 与退出码。
//
// 读取放在 goroutine 里并加超时：Recv 没有读超时，一旦服务端不发 exit 帧，
// 测试必须能失败退出而不是挂死。
func readExit(conn *sandbox.Conn, limit time.Duration) (string, int, error) {
	type result struct {
		out  string
		code int
		err  error
	}
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
				}
				continue
			}
			switch f.Type {
			case sandbox.FrameExit:
				ch <- result{out: sb.String(), code: f.Code}
				return
			case sandbox.FrameError:
				ch <- result{err: fmt.Errorf("sandboxd: %s", f.Message)}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.out, r.code, r.err
	case <-time.After(limit):
		return "", 0, fmt.Errorf("等待 exit 帧超时（%s）", limit)
	}
}

// execContainerNames 当前带 exec 标签的容器名集合。
func execContainerNames(t *testing.T, bin string) map[string]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "ps", "-a", "--filter", "label="+execContainerLabel,
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

// waitNewContainer 等一个「上次没见过的」容器出现，返回它的名字。
func waitNewContainer(t *testing.T, bin string, before map[string]struct{}) string {
	t.Helper()
	deadline := time.Now().Add(containerWait)
	for time.Now().Before(deadline) {
		for name := range execContainerNames(t, bin) {
			if _, existed := before[name]; !existed {
				return name
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("这次执行没有留下可观测的容器：容器没起来，或起来后又瞬间消失")
	return ""
}

// waitContainersGone 等这批容器全部消失。
func waitContainersGone(t *testing.T, bin string, names []string) {
	t.Helper()
	deadline := time.Now().Add(containerWait)
	var left []string
	for time.Now().Before(deadline) {
		now := execContainerNames(t, bin)
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
	t.Fatalf("容器 %v 在 %s 内没有消失 —— 这次执行没有收干净", left, containerWait)
}

// assertNoNewContainers 确认没有多出容器（与速度无关的判据）。
func assertNoNewContainers(t *testing.T, bin string, before map[string]struct{}) {
	t.Helper()
	for name := range execContainerNames(t, bin) {
		if _, existed := before[name]; !existed {
			t.Errorf("多出了容器 %s —— 这次执行没有收干净", name)
		}
	}
}

// assertHealthy 服务没被这次连接拖住 / 拖死。
func assertHealthy(t *testing.T, base string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("断链之后服务不再响应 /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d", resp.StatusCode)
	}
}
