package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// End to end: a real WS server is started to verify the long-lived interactive behaviour of "a disconnect does not
// kill the process, connections reuse it, FrameKill removes it". The fakeProc stdin passthrough plus spawn's stdout
// broadcast close the loop: ws → stdin → echo → stdout broadcast → ws.

func newTestInteractiveServer(t *testing.T, ex *fakeExec) (*Server, *httptest.Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode) // silence gin's debug startup log
	s := &Server{
		cfg:         &Config{Exec: UseConfig{}, Limits: LimitsConfig{}},
		interactive: newInteractiveRegistry(ex, time.Hour),
		upgrader:    websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
	r := gin.New()
	r.GET("/v1/interactive", s.handleInteractive)
	ts := httptest.NewServer(r)
	t.Cleanup(func() { ts.Close(); s.interactive.Close() })
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/interactive"
	return s, ts, url
}

// dialInteractive connects and completes open→ready; a non-nil argv means first creation.
func dialInteractive(t *testing.T, url, sessionID, toolID string, argv []string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	open := map[string]any{"type": "open", "session_id": sessionID, "tool_id": toolID}
	if argv != nil {
		open["spec"] = map[string]any{"argv": argv}
	}
	if err := c.WriteJSON(open); err != nil {
		t.Fatalf("write open: %v", err)
	}
	var f map[string]any
	if err := c.ReadJSON(&f); err != nil {
		t.Fatalf("read ready: %v", err)
	}
	if f["type"] != "ready" {
		t.Fatalf("want ready, got %v (message=%v)", f["type"], f["message"])
	}
	return c
}

func sendStdin(t *testing.T, c *websocket.Conn, s string) {
	t.Helper()
	buf := make([]byte, 1+len(s))
	buf[0] = 3 // ChanStdin
	copy(buf[1:], s)
	if err := c.WriteMessage(websocket.BinaryMessage, buf); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
}

// readUntil reads ChanStdout frames until the accumulated content contains want (the process is long-lived, so the
// output stream never ends by itself).
func readUntil(t *testing.T, c *websocket.Conn, want string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got strings.Builder
	for {
		typ, data, err := c.ReadMessage()
		if err != nil {
			if strings.Contains(got.String(), want) {
				return
			}
			t.Fatalf("read until %q: got %q (err %v)", want, got.String(), err)
		}
		if typ == websocket.BinaryMessage && len(data) > 0 && data[0] == 1 { // ChanStdout
			got.Write(data[1:])
			if strings.Contains(got.String(), want) {
				return
			}
		}
	}
}

// Two connections (the first with argv, the second without) reuse the same process: the stdout broadcast still works
// on the second one and Start happened only once.
func TestInteractiveHandlerReusesProcessAcrossConnections(t *testing.T) {
	ex := &fakeExec{}
	_, _, url := newTestInteractiveServer(t, ex)

	c1 := dialInteractive(t, url, "s1", "shell", []string{"sh"})
	sendStdin(t, c1, "hello")
	readUntil(t, c1, "hello")
	_ = c1.Close()

	// The process survives the disconnect: the second connection carries no argv and reuses the same process
	c2 := dialInteractive(t, url, "s1", "shell", nil)
	sendStdin(t, c2, "world")
	readUntil(t, c2, "world")
	_ = c2.Close()

	if got := ex.startCount(); got != 1 {
		t.Errorf("both connections should reuse one process (Start once), got %d", got)
	}
}

// A multi-tool session: killing one does not invalidate it — only the connection that kills the last tool receives
// session_empty. The gateway uses this criterion to decide whether to drop the sticky binding, so getting it wrong
// means "the session is still alive but gets unbound" (the next tool call may land on another backend and rebuild a
// sandbox).
func TestInteractiveKillSignalsEmptyOnlyWhenNoToolsLeft(t *testing.T) {
	ex := &fakeExec{}
	_, _, url := newTestInteractiveServer(t, ex)

	c1 := dialInteractive(t, url, "s1", "shell", []string{"sh"})
	c2 := dialInteractive(t, url, "s1", "repl", []string{"sh"})

	// Kill shell: repl is still alive → only a closed connection may be read, never session_empty
	if err := c1.WriteJSON(map[string]any{"type": "kill"}); err != nil {
		t.Fatalf("write kill: %v", err)
	}
	_ = c1.SetReadDeadline(time.Now().Add(3 * time.Second))
	var f map[string]any
	if err := c1.ReadJSON(&f); err == nil && f["type"] == "session_empty" {
		t.Fatalf("session_empty was reported while another tool was still alive")
	}

	// Kill the last one: session_empty carrying session_id must arrive
	if err := c2.WriteJSON(map[string]any{"type": "kill"}); err != nil {
		t.Fatalf("write kill: %v", err)
	}
	_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := c2.ReadJSON(&f); err != nil {
		t.Fatalf("session_empty should arrive after the last tool is killed: %v", err)
	}
	if f["type"] != "session_empty" || f["session_id"] != "s1" {
		t.Fatalf("want session_empty(s1), got %v", f)
	}
}

// FrameKill explicitly kills the process and removes it from the registry.
func TestInteractiveHandlerKillRemovesProcess(t *testing.T) {
	ex := &fakeExec{}
	_, _, url := newTestInteractiveServer(t, ex)

	c := dialInteractive(t, url, "s1", "shell", []string{"sh"})
	if err := c.WriteJSON(map[string]any{"type": "kill"}); err != nil {
		t.Fatalf("write kill: %v", err)
	}
	waitFor(t, "the process to be killed after a kill frame", func() bool { return ex.proc(0).killed.Load() })
	_ = c.Close()
}
