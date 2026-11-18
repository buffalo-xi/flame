package sandbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var sentinelRe = regexp.MustCompile(`__SH_DONE_[0-9a-f]+__`)

// newMockInteractive 起一个模拟 sandboxd /v1/interactive 的 WS 服务。
//
// handler 收到 open 后回 ready，然后回调 onStdin 生成要回写的 stdout 内容。
func newMockInteractive(t *testing.T, onOpen func(*Frame), onStdin func(string) string) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		c := NewConn(ws)

		f, _, err := c.Recv()
		if err != nil || f.Type != FrameOpen {
			return
		}
		if onOpen != nil {
			onOpen(f)
		}
		_ = c.SendFrame(&Frame{Type: FrameReady})

		for {
			_, data, err := c.Recv()
			if err != nil {
				return
			}
			if data == nil || data[0] != ChanStdin {
				continue
			}
			out := onStdin(string(data[1:]))
			_ = c.SendData(ChanStdout, []byte(out))
			return
		}
	}))
}

// 验证 open 帧携带 session_id/tool_id/ephemeral/dir，且哨兵被正确解析、退出码与输出分离。
func TestInteractiveExecSentinel(t *testing.T) {
	var gotOpen *Frame
	srv := newMockInteractive(t, func(f *Frame) { gotOpen = f }, func(stdin string) string {
		m := sentinelRe.FindString(stdin)
		if m == "" {
			t.Errorf("stdin 里没有哨兵: %q", stdin)
		}
		return "hello\nworld\n" + m + "7\n"
	})
	defer srv.Close()

	ep := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/interactive"
	c := NewInteractive(ep, "", Limits{})

	res, err := c.Exec(context.Background(),
		InteractiveSession{Key: "s1", Ephemeral: true},
		"shell", []string{"/bin/bash"}, "echo hello",
		[]Mount{{Host: "/host/ws", Guest: "/workspace"}}, "/workspace")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if gotOpen == nil {
		t.Fatal("没收到 open 帧")
	}
	if gotOpen.SessionID != "s1" || gotOpen.ToolID != "shell" || !gotOpen.Ephemeral {
		t.Errorf("open 帧 = %+v", gotOpen)
	}
	if gotOpen.Spec == nil || gotOpen.Spec.Dir != "/workspace" {
		t.Errorf("open.Spec.Dir = %+v, want /workspace", gotOpen.Spec)
	}
	if len(gotOpen.Spec.Mounts) != 1 || gotOpen.Spec.Mounts[0].Guest != "/workspace" {
		t.Errorf("open.Spec.Mounts = %+v", gotOpen.Spec.Mounts)
	}

	if res.Stdout != "hello\nworld\n" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "hello\nworld\n")
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if !res.Completed {
		t.Error("Completed = false, want true（读到了哨兵）")
	}
}

// 验证 Kill：发 open + kill，服务端能收到 kill 帧。
func TestInteractiveKill(t *testing.T) {
	gotKill := make(chan struct{}, 1)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		c := NewConn(ws)
		if f, _, err := c.Recv(); err != nil || f.Type != FrameOpen {
			return
		}
		_ = c.SendFrame(&Frame{Type: FrameReady})
		for {
			f, _, err := c.Recv()
			if err != nil {
				return
			}
			if f.Type == FrameKill {
				gotKill <- struct{}{}
				return
			}
		}
	}))
	defer srv.Close()

	ep := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/interactive"
	c := NewInteractive(ep, "", Limits{})
	if err := c.Kill(context.Background(), InteractiveSession{Key: "s1", Ephemeral: true}, "shell"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-gotKill:
	case <-time.After(2 * time.Second):
		t.Fatal("服务端没收到 kill 帧")
	}
}

// Windows 的 cmd 会把喂进去的整行原样回显（管道下默认 echo on），于是输出里
// 同一哨兵出现两次：第一次来自回显、后面跟的是未展开的 %errorlevel%，第二次才是
// 真正的 "哨兵+退出码"。只认第一次会让每次 shell 调用都等到 30s 空闲超时。
func TestParseSentinelSkipsEchoedCommand(t *testing.T) {
	const s = "__SH_DONE_ab12__"
	out := "D:\\ws>echo hi & echo " + s + "%errorlevel%\r\nhi \r\n" + s + "0\r\n\r\nD:\\ws>"

	hit := parseSentinel([]byte(out), s)
	if !hit.ok || hit.code != 0 {
		t.Fatalf("应命中回显之后那一处, got %+v", hit)
	}
	if got := stripSentinelAt(out, hit.at); strings.Contains(got, s+"0") || !strings.Contains(got, "hi ") {
		t.Errorf("删的是命中行、不是回显行, got %q", got)
	}

	// 回显已到、真行还被分片截断：此时不能命中（否则会把 %errorlevel% 当输出）。
	partial := "D:\\ws>echo hi & echo " + s + "%errorlevel%\r\nhi \r\n" + s
	if hit := parseSentinel([]byte(partial), s); hit.ok {
		t.Errorf("退出码未到齐时不该命中, got %+v", hit)
	}

	// 非零退出码
	if hit := parseSentinel([]byte(s+"3\r\n"), s); !hit.ok || hit.code != 3 {
		t.Errorf("退出码应为 3, got %+v", hit)
	}
}

// 哨兵包装按 shell 类型选分隔符；退出码用各自的变量。
func TestWrapWithSentinel(t *testing.T) {
	cases := []struct {
		shell string
		want  string
	}{
		{"/bin/bash", "ls; echo __S__$?"},
		{"/bin/sh", "ls; echo __S__$?"},
		// cmd 必须分行：同一行里 %errorlevel% 在解析阶段就被展开成上一条命令的码。
		{"cmd", "ls\necho __S__%errorlevel%"},
		{"C:\\Windows\\System32\\cmd.exe", "ls\necho __S__%errorlevel%"},
		{"powershell", `ls; echo "__S__$LASTEXITCODE"`},
		{"pwsh", `ls; echo "__S__$LASTEXITCODE"`},
	}
	for _, c := range cases {
		if got := wrapWithSentinel("ls", "__S__", c.shell); got != c.want {
			t.Errorf("wrapWithSentinel(%q) = %q, want %q", c.shell, got, c.want)
		}
	}
}
