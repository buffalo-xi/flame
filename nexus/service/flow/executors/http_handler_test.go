package executors

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// The response mode is either forced (and then validated) or detected from the content type; anything else is a
// complete response.
func TestDetermineResponseMode(t *testing.T) {
	respWith := func(contentType string) *http.Response {
		h := http.Header{}
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		return &http.Response{Header: h, StatusCode: 200}
	}

	cases := []struct {
		name       string
		forced     string
		resp       *http.Response
		want       string
		wantErrMsg string
	}{
		{"forced complete", "complete", respWith("text/event-stream"), "complete", ""},
		{"forced sse", "sse_stream", respWith("application/json"), "sse_stream", ""},
		{"forced unknown", "chunked", respWith(""), "", "unsupported response_mode: chunked"},
		{"detected sse with parameters", "", respWith("text/event-stream; charset=utf-8"), "sse_stream", ""},
		{"detected complete for json", "", respWith("application/json"), "complete", ""},
		{"no content type is complete", "", respWith(""), "complete", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{}
			if tc.forced != "" {
				cfg["response_mode"] = tc.forced
			}
			got, err := determineResponseMode(cfg, tc.resp)
			if tc.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("determineResponseMode = %q, %v, want %q", got, err, tc.wantErrMsg)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("determineResponseMode = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

// Only text-like responses are kept as text; everything else is base64 so the value stays a valid string.
func TestBodyToString(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
	if got := bodyToString(png, "image/png"); got != base64.StdEncoding.EncodeToString(png) {
		t.Errorf("bodyToString(image/png) = %q, want base64", got)
	}
	if got := bodyToString([]byte("plain"), ""); got != "plain" {
		t.Errorf("bodyToString(no content type) = %q, want text", got)
	}
	if got := bodyToString([]byte(`{"a":1}`), "application/json"); got != `{"a":1}` {
		t.Errorf("bodyToString(json) = %q, want text", got)
	}
}

func TestIsTextContent(t *testing.T) {
	cases := map[string]bool{
		"":                                true,
		"text/plain; charset=utf-8":       true,
		"application/json":                true,
		"application/problem+json":        true,
		"application/xml":                 true,
		"application/xhtml+xml":           true,
		"application/javascript":          true,
		"image/png":                       false,
		"application/octet-stream":        false,
		"multipart/form-data; boundary=x": false,
	}
	for contentType, want := range cases {
		if got := isTextContent(contentType); got != want {
			t.Errorf("isTextContent(%q) = %v, want %v", contentType, got, want)
		}
	}
}

// The SSE spec allows exactly one leading space after "data:"; a second one is content.
func TestTrimLeadingSpace(t *testing.T) {
	cases := map[string]string{
		"":    "",
		"x":   "x",
		" x":  "x",
		"  x": " x",
		"x ":  "x ",
	}
	for in, want := range cases {
		if got := trimLeadingSpace(in); got != want {
			t.Errorf("trimLeadingSpace(%q) = %q, want %q", in, got, want)
		}
	}
}

// toMap carries the metadata only when present, and always carries the data.
func TestSSEEventToMap(t *testing.T) {
	got := (&SSEEvent{Data: "d"}).toMap()
	if _, ok := got["id"]; ok {
		t.Error("an empty id must not be emitted")
	}
	if _, ok := got["event"]; ok {
		t.Error("an empty event name must not be emitted")
	}
	if got["data"] != "d" {
		t.Errorf("data = %v, want d", got["data"])
	}

	full := (&SSEEvent{ID: "1", Event: "token", Data: "d"}).toMap()
	if full["id"] != "1" || full["event"] != "token" {
		t.Errorf("full = %v, want id and event", full)
	}
}

// mergeInputs lets the event data win (it is what this frame is about) without modifying the node's own inputs.
func TestMergeInputs(t *testing.T) {
	args := map[string]any{"topic": "billing", "mode": "complete"}
	merged := mergeInputs(nil, args, map[string]any{"mode": "sse_stream", "event": "x"})
	if merged["mode"] != "sse_stream" {
		t.Errorf("mode = %v, want the event data to win", merged["mode"])
	}
	if merged["topic"] != "billing" {
		t.Errorf("topic = %v, want the node input kept", merged["topic"])
	}
	if args["mode"] != "complete" {
		t.Errorf("the caller's inputs were modified: %v", args)
	}
}

// The handler must be a real script language: template is pure rendering with no side effects, so it is rejected
// rather than silently accepted.
func TestParseHandler(t *testing.T) {
	ctx := newExecContext()

	script, lang, _, err := parseHandler(map[string]any{}, ctx)
	if err != nil || script != "" || lang != "" {
		t.Errorf("parseHandler(no handler) = %q/%q/%v, want empty", script, lang, err)
	}

	script, lang, timeout, err := parseHandler(map[string]any{
		"handler": map[string]any{"script": "return 1"},
	}, ctx)
	if err != nil || script != "return 1" || lang != "lua" {
		t.Errorf("parseHandler(defaults) = %q/%q/%v, want the script with lua", script, lang, err)
	}
	// No handler timeout configured: the run's script timeout applies (the built-in fallback here).
	if timeout != 300 {
		t.Errorf("timeout = %d, want the fallback 300", timeout)
	}

	_, _, timeout, err = parseHandler(map[string]any{
		"handler": map[string]any{
			"script":  "return 1",
			"lang":    "python",
			"timeout": map[string]any{"enabled": true, "seconds": float64(2)},
		},
	}, ctx)
	if err != nil || timeout != 2 {
		t.Errorf("timeout = %d, %v, want the configured 2s", timeout, err)
	}

	if _, _, _, err := parseHandler(map[string]any{
		"handler": map[string]any{"script": "{{ .input.x }}", "lang": "template"},
	}, ctx); err == nil || !strings.Contains(err.Error(), "does not support the template language") {
		t.Errorf("parseHandler(template) = %v, want the explicit rejection", err)
	}
}

// The template rejection happens after the request was already sent, so the test asserts both the error and the side
// effect (the same ordering the doc warns about for response_mode).
func TestHTTPExecutorRejectsTemplateHandler(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	node := newExecNode("2", "http", map[string]any{
		"http": map[string]any{
			"url":     srv.URL,
			"handler": map[string]any{"lang": "template", "script": "{{ .input.body }}"},
		},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&HttpExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must reject a template handler")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1: the request is sent before the handler is parsed", hits)
	}
}

// A binary body becomes base64 rather than raw bytes: the variable stays a string and the node succeeds.
func TestHTTPExecutorBinaryBodyIsBase64(t *testing.T) {
	payload := []byte{0x89, 'P', 'N', 'G', 0x00}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	node := newExecNode("2", "http", map[string]any{"http": map[string]any{"url": srv.URL}})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&HttpExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if got, want := nr.OutputVars["body"], base64.StdEncoding.EncodeToString(payload); got != want {
		t.Errorf("body = %v, want base64 %q", got, want)
	}
}

// An HTTP error status is data, not a node failure: the flow decides with `status` in a branch, and the node itself
// succeeds.
func TestHTTPExecutorNon2xxIsData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	node := newExecNode("2", "http", map[string]any{"http": map[string]any{"url": srv.URL}})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&HttpExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q), want the status kept as data", err, nr.ErrCode)
	}
	if got := fmt.Sprint(nr.OutputVars["status"]); got != "500" {
		t.Errorf("status = %v, want 500", nr.OutputVars["status"])
	}
	if got, _ := nr.OutputVars["body"].(string); got != `{"error":"boom"}` {
		t.Errorf("body = %q, want the error body", got)
	}
}

// Every frame is collected with its metadata, and multi-line data is joined with a newline as the SSE spec says.
func TestHTTPExecutorSSEKeepsMetadataAndMultilineData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("id: 1\nevent: token\ndata: first\ndata: second\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	node := newExecNode("2", "http", map[string]any{"http": map[string]any{"url": srv.URL}})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&HttpExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}

	raw, err := json.Marshal(nr.OutputVars["sse_events"])
	if err != nil {
		t.Fatalf("sse_events is not serialisable: %v", err)
	}
	for _, want := range []string{`"id":"1"`, `"event":"token"`, `"data":"first\nsecond"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("sse_events = %s, want it to contain %s", raw, want)
		}
	}
}
