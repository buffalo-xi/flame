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

// newMockInteractive starts a WS server that mimics sandboxd /v1/interactive.
//
// After the open frame the handler replies ready, then onStdin produces the stdout to write back.
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

// The open frame carries session_id/tool_id/ephemeral/dir, the sentinel is parsed and the exit code is split off the output.
func TestInteractiveExecSentinel(t *testing.T) {
	var gotOpen *Frame
	srv := newMockInteractive(t, func(f *Frame) { gotOpen = f }, func(stdin string) string {
		m := sentinelRe.FindString(stdin)
		if m == "" {
			t.Errorf("no sentinel on stdin: %q", stdin)
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
		t.Fatal("no open frame received")
	}
	if gotOpen.SessionID != "s1" || gotOpen.ToolID != "shell" || !gotOpen.Ephemeral {
		t.Errorf("open frame = %+v", gotOpen)
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
		t.Error("Completed = false, want true (the sentinel was read)")
	}
}

// Kill: send open + kill, the server receives the kill frame.
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
		t.Fatal("the server did not receive the kill frame")
	}
}

// Windows cmd echoes the whole fed line back (echo is on by default under a pipe), so the output
// carries the sentinel twice: the first comes from the echo and is followed by an unexpanded %errorlevel%,
// the second is the real "sentinel + exit code". Taking the first makes every shell call wait for the 30s idle timeout.
func TestParseSentinelSkipsEchoedCommand(t *testing.T) {
	const s = "__SH_DONE_ab12__"
	out := "D:\\ws>echo hi & echo " + s + "%errorlevel%\r\nhi \r\n" + s + "0\r\n\r\nD:\\ws>"

	hit := parseSentinel([]byte(out), s)
	if !hit.ok || hit.code != 0 {
		t.Fatalf("expected the hit after the echo, got %+v", hit)
	}
	if got := stripSentinelAt(out, hit.at); strings.Contains(got, s+"0") || !strings.Contains(got, "hi ") {
		t.Errorf("the hit line was removed, not the echo line, got %q", got)
	}

	// The echo arrived but the real line is still truncated by the chunk split: no hit now, or %errorlevel% would be read as output.
	partial := "D:\\ws>echo hi & echo " + s + "%errorlevel%\r\nhi \r\n" + s
	if hit := parseSentinel([]byte(partial), s); hit.ok {
		t.Errorf("no hit while the exit code is incomplete, got %+v", hit)
	}

	// Non-zero exit code.
	if hit := parseSentinel([]byte(s+"3\r\n"), s); !hit.ok || hit.code != 3 {
		t.Errorf("the exit code should be 3, got %+v", hit)
	}
}

// The sentinel wrapper picks the separator by shell type; each shell reports the exit code in its own variable.
func TestWrapWithSentinel(t *testing.T) {
	cases := []struct {
		shell string
		want  string
	}{
		{"/bin/bash", "ls; echo __S__$?"},
		{"/bin/sh", "ls; echo __S__$?"},
		// cmd needs its own line: on the same line %errorlevel% is expanded at parse time to the previous command's code.
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
