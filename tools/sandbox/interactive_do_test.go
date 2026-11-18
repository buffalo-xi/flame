package sandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Do 是浏览器的「一问一答」通道：发一行 JSON、读回一行。
//
// 这里守两件事：
//  1. open 帧带的是 tool_id=browser（sandboxd 据此换 argv / 资源上限）；
//  2. 上一次调用迟到的响应（id 不匹配）必须被丢弃 —— 否则模型会拿到
//     上一页的快照，对着已经翻篇的页面点 ref。
func TestInteractiveDoMatchesID(t *testing.T) {
	var gotOpen *Frame
	srv := newMockInteractive(t, func(f *Frame) { gotOpen = f }, func(string) string {
		return `{"id":1,"ok":true,"snapshot":"STALE"}` + "\n" +
			`{"id":2,"ok":true,"snapshot":"- e1 [button] \"Sign in\""}` + "\n"
	})
	defer srv.Close()

	ep := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/interactive"
	c := NewInteractive(ep, "", Limits{})

	line, err := c.Do(context.Background(),
		InteractiveSession{Key: "s1", Ephemeral: true},
		"browser", []string{"browserhost"},
		[]byte(`{"id":2,"op":"snapshot"}`),
		[]Mount{{Host: "/host/ws", Guest: "/workspace"}}, "/workspace")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if gotOpen == nil {
		t.Fatal("没收到 open 帧")
	}
	if gotOpen.ToolID != "browser" {
		t.Errorf("tool_id = %q, want browser", gotOpen.ToolID)
	}
	if gotOpen.SessionID != "s1" || !gotOpen.Ephemeral {
		t.Errorf("open 帧 = %+v", gotOpen)
	}

	if strings.Contains(string(line), "STALE") {
		t.Fatalf("迟到的响应没被丢弃: %s", line)
	}
	if !strings.Contains(string(line), `Sign in`) {
		t.Fatalf("拿到的不是这次请求的响应: %s", line)
	}
}

// 服务端不回话时必须按 ctx 超时返回，不能挂死 —— 工具调用有 60s 硬上限，
// 挂死会把整个 agent 回合拖住。
func TestInteractiveDoTimeout(t *testing.T) {
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
		time.Sleep(2 * time.Second) // 故意不回
	}))
	defer srv.Close()

	ep := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/interactive"
	c := NewInteractive(ep, "", Limits{})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Do(ctx, InteractiveSession{Key: "s1", Ephemeral: true},
		"browser", []string{"browserhost"}, []byte(`{"id":1,"op":"snapshot"}`), nil, "/workspace")
	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want ctx deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("超时没生效，耗时 %v", elapsed)
	}
}
