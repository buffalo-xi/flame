package gw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flame/tools/sandbox"

	"github.com/gorilla/websocket"
)

// newFakeBackend 起一个假 sandboxd：收到 open 回 ready，
// 之后由 onMessage 决定怎么回应（nil 表示丢弃）。
func newFakeBackend(t *testing.T, onMessage func(ws *websocket.Conn, f sandbox.Frame)) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.Close() }()
		for {
			_, data, rerr := ws.ReadMessage()
			if rerr != nil {
				return
			}
			var f sandbox.Frame
			if json.Unmarshal(data, &f) != nil {
				continue
			}
			if f.Type == sandbox.FrameOpen {
				b, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameReady})
				if ws.WriteMessage(websocket.TextMessage, b) != nil {
					return
				}
				continue
			}
			if onMessage != nil {
				onMessage(ws, f)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestGateway 构造一个只挂这个后端的网关，并把粘性存储换成可观测的 fake。
func newTestGateway(t *testing.T, backendURL string) (*Gateway, *fakeSticky) {
	t.Helper()
	g, err := New(&Config{
		StickyTTLSec: 2100,
		Backends:     []BackendConfig{{URL: backendURL}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fs := &fakeSticky{bindings: map[string]string{"s1": backendURL}}
	g.sticky = fs
	return g, fs
}

// dialGateway 连上网关的 /v1/interactive 并走完 open → ready。
func dialGateway(t *testing.T, g *Gateway, sessionID string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(g.Router())
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/interactive", nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	open, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameOpen, SessionID: sessionID, ToolID: "shell"})
	if err := conn.WriteMessage(websocket.TextMessage, open); err != nil {
		t.Fatalf("send open: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil { // 等 ready，确认代理已建立
		t.Fatalf("read ready: %v", err)
	}
	return conn
}

// sandboxd 说「这个 session 已经没有长交互工具」时，粘性绑定必须立刻失效 ——
// 否则它还会把会话钉在原后端，最长 sticky_ttl_sec。
func TestProxyUnbindOnSessionEmpty(t *testing.T) {
	backend := newFakeBackend(t, func(ws *websocket.Conn, f sandbox.Frame) {
		if f.Type != sandbox.FrameKill {
			return
		}
		b, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameSessionEmpty, SessionID: "s1"})
		_ = ws.WriteMessage(websocket.TextMessage, b)
	})
	g, fs := newTestGateway(t, backend.URL)

	conn := dialGateway(t, g, "s1")
	kill, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameKill})
	if err := conn.WriteMessage(websocket.TextMessage, kill); err != nil {
		t.Fatalf("send kill: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fs.mu.Lock()
		got := append([][2]string(nil), fs.unbinds...)
		_, stillBound := fs.bindings["s1"]
		fs.mu.Unlock()
		if len(got) > 0 {
			if got[0] != [2]string{"s1", backend.URL} {
				t.Fatalf("解绑参数 = %v, want [s1 %s]", got[0], backend.URL)
			}
			if stillBound {
				t.Errorf("解绑后绑定应被删掉")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("收到 session_empty 后没有触发解绑")
}

// 新起一个只回 /v1/capabilities 的假后端。
func newCapsBackend(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/capabilities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 粘性 TTL 必须覆盖 sandboxd 的空闲回收上界：sticky_ttl > idle + 2×janitor。
// 两个值在两个进程的配置文件里，网关只能靠 capabilities 报出来的值校验。
func TestStickyTTLProblems(t *testing.T) {
	srv := newCapsBackend(t, `{"mode":"none","interactive":{"idle_sec":1800,"ephemeral_idle_sec":120,"janitor_sec":60}}`)
	ctx := context.Background()

	// 2100 > 1800 + 240 → 合格
	ok, err := New(&Config{StickyTTLSec: 2100, Backends: []BackendConfig{{URL: srv.URL}}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if p := ok.stickyTTLProblems(ctx); len(p) != 0 {
		t.Fatalf("2100s 应合格，got %v", p)
	}

	// 1900 不大于 1920 → 报一次
	bad, err := New(&Config{StickyTTLSec: 1900, Backends: []BackendConfig{{URL: srv.URL}}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	p := bad.stickyTTLProblems(ctx)
	if len(p) != 1 {
		t.Fatalf("应报一条，got %v", p)
	}
	for _, want := range []string{"sticky_ttl_sec=1900", "1800", "1920"} {
		if !strings.Contains(p[0], want) {
			t.Errorf("诊断信息缺 %q: %s", want, p[0])
		}
	}
	// 同一个后端只报一次，否则每 2s 刷一条
	if again := bad.stickyTTLProblems(ctx); len(again) != 0 {
		t.Errorf("不该重复告警，got %v", again)
	}

	// 老版本 sandboxd 没报 interactive 段：无从校验，当合格
	legacy := newCapsBackend(t, `{"mode":"none"}`)
	old, err := New(&Config{StickyTTLSec: 1, Backends: []BackendConfig{{URL: legacy.URL}}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if p := old.stickyTTLProblems(ctx); len(p) != 0 {
		t.Errorf("对方未上报时不该告警，got %v", p)
	}
}

// 对端改配置重启后必须重新校验：缓存永不过期会让「新值不合格」被旧值掩盖，
// 且旧值合格时连一条日志都没有。
func TestStickyTTLProblemsRefreshesCaps(t *testing.T) {
	var payload atomic.Value
	payload.Store(`{"mode":"none","interactive":{"idle_sec":1800,"ephemeral_idle_sec":120,"janitor_sec":60}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload.Load().(string)))
	}))
	defer srv.Close()

	old := capsRefresh
	capsRefresh = time.Millisecond
	defer func() { capsRefresh = old }()

	g, err := New(&Config{StickyTTLSec: 2100, Backends: []BackendConfig{{URL: srv.URL}}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	if p := g.stickyTTLProblems(ctx); len(p) != 0 {
		t.Fatalf("2100 对 idle=1800 应合格，got %v", p)
	}

	// sandboxd 改成 idle=2800 并重启（地址不变）
	payload.Store(`{"mode":"none","interactive":{"idle_sec":2800,"ephemeral_idle_sec":120,"janitor_sec":60}}`)
	time.Sleep(5 * time.Millisecond) // 让缓存过期

	p := g.stickyTTLProblems(ctx)
	if len(p) != 1 {
		t.Fatalf("对端把 idle 改大后应重新告警，got %v", p)
	}
	if !strings.Contains(p[0], "2800") || !strings.Contains(p[0], "2920") {
		t.Errorf("诊断信息应基于新值：%s", p[0])
	}
}

// 反面：没有 session_empty（例如同 session 还有别的工具没杀完）就绝不能解绑。
func TestProxyKeepsBindingWithoutEmptyFrame(t *testing.T) {
	backend := newFakeBackend(t, nil) // 对 kill 不回任何东西
	g, fs := newTestGateway(t, backend.URL)

	conn := dialGateway(t, g, "s1")
	kill, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameKill})
	if err := conn.WriteMessage(websocket.TextMessage, kill); err != nil {
		t.Fatalf("send kill: %v", err)
	}
	_ = conn.Close() // 结束本次代理

	// 留出足够时间让（本不该发生的）解绑发生并被记录。
	time.Sleep(200 * time.Millisecond)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.unbinds) != 0 {
		t.Errorf("没有 session_empty 却解绑了: %v", fs.unbinds)
	}
	if _, ok := fs.bindings["s1"]; !ok {
		t.Errorf("绑定应保留（其余工具还在用这个后端）")
	}
}
