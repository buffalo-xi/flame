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

// 端到端：起真实 WS 服务，验证长交互的「断连不杀进程、跨连接复用、FrameKill 移除」。
// fakeProc 的 stdin→stdout 直通 + spawn 的 stdout 广播，构成完整闭环：
// ws → stdin → echo → stdout 广播 → ws。

func newTestInteractiveServer(t *testing.T, ex *fakeExec) (*Server, *httptest.Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode) // 静音 gin 的 debug 启动日志
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

// dialInteractive 建连并走完 open→ready，argv 非空表示首次创建。
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

// readUntil 读 ChanStdout 帧，直到累积内容包含 want（进程长驻，输出流不会自然结束）。
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

// 两次连接（首次带 argv、第二次不带）复用同一进程：stdout 广播第二次仍工作，Start 仅 1 次。
func TestInteractiveHandlerReusesProcessAcrossConnections(t *testing.T) {
	ex := &fakeExec{}
	_, _, url := newTestInteractiveServer(t, ex)

	c1 := dialInteractive(t, url, "s1", "shell", []string{"sh"})
	sendStdin(t, c1, "hello")
	readUntil(t, c1, "hello")
	_ = c1.Close()

	// 断开后进程保留：第二次连接不带 argv，复用同一进程
	c2 := dialInteractive(t, url, "s1", "shell", nil)
	sendStdin(t, c2, "world")
	readUntil(t, c2, "world")
	_ = c2.Close()

	if got := ex.startCount(); got != 1 {
		t.Errorf("两次连接应复用同一进程（Start 1 次），got %d", got)
	}
}

// 多工具会话：杀一个不算作废 —— 只有最后一个工具被杀的连接才收到 session_empty。
// 网关据此决定解不解粘性绑定，所以这条判据错了会导致「会话还活着却被解绑」
// （下一个工具的调用可能落到别的后端、重建一份沙箱）。
func TestInteractiveKillSignalsEmptyOnlyWhenNoToolsLeft(t *testing.T) {
	ex := &fakeExec{}
	_, _, url := newTestInteractiveServer(t, ex)

	c1 := dialInteractive(t, url, "s1", "shell", []string{"sh"})
	c2 := dialInteractive(t, url, "s1", "repl", []string{"sh"})

	// 杀 shell：repl 还在 → 读到的只能是连接关闭，不能是 session_empty
	if err := c1.WriteJSON(map[string]any{"type": "kill"}); err != nil {
		t.Fatalf("write kill: %v", err)
	}
	_ = c1.SetReadDeadline(time.Now().Add(3 * time.Second))
	var f map[string]any
	if err := c1.ReadJSON(&f); err == nil && f["type"] == "session_empty" {
		t.Fatalf("还有工具存活时报了 session_empty")
	}

	// 杀最后一个：必须收到带 session_id 的 session_empty
	if err := c2.WriteJSON(map[string]any{"type": "kill"}); err != nil {
		t.Fatalf("write kill: %v", err)
	}
	_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := c2.ReadJSON(&f); err != nil {
		t.Fatalf("最后一个工具被杀后应收到 session_empty: %v", err)
	}
	if f["type"] != "session_empty" || f["session_id"] != "s1" {
		t.Fatalf("want session_empty(s1), got %v", f)
	}
}

// FrameKill 显式杀进程并把它从注册表移除。
func TestInteractiveHandlerKillRemovesProcess(t *testing.T) {
	ex := &fakeExec{}
	_, _, url := newTestInteractiveServer(t, ex)

	c := dialInteractive(t, url, "s1", "shell", []string{"sh"})
	if err := c.WriteJSON(map[string]any{"type": "kill"}); err != nil {
		t.Fatalf("write kill: %v", err)
	}
	waitFor(t, "kill 后进程被杀", func() bool { return ex.proc(0).killed.Load() })
	_ = c.Close()
}
