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

// Do is the browser's request/response channel: send one JSON line, read one line back.
//
// It guards two things:
//  1. the open frame carries tool_id=browser (sandboxd swaps argv / limits on it);
//  2. a late response from an earlier call (id mismatch) must be dropped - otherwise the model gets
//     the previous page's snapshot and clicks a ref on a page that has already moved on.
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
		t.Fatal("no open frame received")
	}
	if gotOpen.ToolID != "browser" {
		t.Errorf("tool_id = %q, want browser", gotOpen.ToolID)
	}
	if gotOpen.SessionID != "s1" || !gotOpen.Ephemeral {
		t.Errorf("open frame = %+v", gotOpen)
	}

	if strings.Contains(string(line), "STALE") {
		t.Fatalf("a late response was not dropped: %s", line)
	}
	if !strings.Contains(string(line), `Sign in`) {
		t.Fatalf("the response does not belong to this request: %s", line)
	}
}

// When the server does not answer, the call must return on the ctx timeout instead of hanging - tool
// calls have a hard 60s cap, and hanging would stall the whole agent turn.
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
		time.Sleep(2 * time.Second) // deliberately no reply
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
		t.Errorf("the timeout did not fire, took %v", elapsed)
	}
}
