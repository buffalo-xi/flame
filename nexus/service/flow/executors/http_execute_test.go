package executors

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// The http node against a real server: the fixed outputs the doc promises (mode / status / headers / body) must be
// written, and the request must carry what the config said.
func TestHTTPExecutorWritesFixedOutputs(t *testing.T) {
	var gotPath, gotQuery, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotMethod = r.URL.Path, r.URL.RawQuery, r.Method
		w.Header().Set("X-Server", "yes")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	node := newExecNode("2", "http", map[string]any{
		"http": map[string]any{
			"url":    srv.URL + "/v1",
			"method": "POST",
			"params": []any{map[string]any{"key": "q", "value": "1"}},
		},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&HttpExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}

	if gotPath != "/v1" || gotQuery != "q=1" || gotMethod != http.MethodPost {
		t.Errorf("server saw %s %s?%s, want POST /v1?q=1", gotMethod, gotPath, gotQuery)
	}
	if got := nr.OutputVars["body"]; got != `{"ok":true}` {
		t.Errorf("body = %v, want the response body", got)
	}
	// The declared type is number, so the value comes back as json.Number rather than float64; a template renders it as
	// 200 either way, which is what the doc promises.
	if got := fmt.Sprint(nr.OutputVars["status"]); got != "200" {
		t.Errorf("status = %v, want 200", nr.OutputVars["status"])
	}
	if got, _ := nr.OutputVars["mode"].(string); got != "complete" {
		t.Errorf("mode = %q, want complete (no SSE content type)", got)
	}
	headers, _ := nr.OutputVars["headers"].(string)
	if !strings.Contains(headers, "X-Server") {
		t.Errorf("headers = %q, want the response headers serialised", headers)
	}
}

// An unknown response_mode is read after the request has been sent: the side effect happens, then the node fails with a
// config error. That ordering is what the static rule warns about, so it is asserted here.
func TestHTTPExecutorUnknownResponseModeFailsAfterSending(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	node := newExecNode("2", "http", map[string]any{
		"http": map[string]any{"url": srv.URL, "response_mode": "chunked"},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&HttpExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail on an unknown response_mode")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "unsupported response_mode") {
		t.Errorf("ErrMsg = %q, want it to name the mode", nr.ErrMsg)
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1: the request is sent before the mode is read", hits)
	}
}

// A missing url is a config error and nothing is sent.
func TestHTTPExecutorMissingURLSendsNothing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()

	node := newExecNode("2", "http", map[string]any{
		"http": map[string]any{"method": "GET"},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&HttpExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail without a url")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "url is required") {
		t.Errorf("ErrMsg = %q, want it to name the missing url", nr.ErrMsg)
	}
	if hits != 0 {
		t.Errorf("server hits = %d, want 0", hits)
	}
}
