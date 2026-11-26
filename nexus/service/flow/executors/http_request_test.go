package executors

import (
	"io"
	"net/http"
	"testing"
)

// buildHTTPRequest is the half of the http node that can be checked without a server: it is where url / method / params /
// headers / body_type are turned into the request the docs describe. The claims asserted here are the ones http.md makes.
func TestBuildHTTPRequestRendersAndDefaults(t *testing.T) {
	tmplCtx := map[string]interface{}{
		"input": map[string]interface{}{"host": "example.com", "q": "hello world", "token": "t0k"},
	}

	t.Run("url/method/params/headers", func(t *testing.T) {
		req, err := buildHTTPRequest(map[string]interface{}{
			"url": "https://{{.input.host}}/api",
			"params": []interface{}{
				map[string]interface{}{"key": "q", "value": "{{.input.q}}"},
				map[string]interface{}{"key": "", "value": "dropped"},
			},
			"headers": []interface{}{
				map[string]interface{}{"key": "X-Token", "value": "{{.input.token}}"},
			},
		}, tmplCtx)
		if err != nil {
			t.Fatalf("buildHTTPRequest = %v", err)
		}
		if req.URL.Host != "example.com" || req.URL.Path != "/api" {
			t.Errorf("url = %s, want https://example.com/api", req.URL)
		}
		// An absent method means GET, as the doc's default column says.
		if req.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", req.Method)
		}
		if got := req.URL.Query().Get("q"); got != "hello world" {
			t.Errorf("params q = %q, want the rendered template value", got)
		}
		if got := req.Header.Get("X-Token"); got != "t0k" {
			t.Errorf("header X-Token = %q, want t0k", got)
		}
	})

	t.Run("method override", func(t *testing.T) {
		req, err := buildHTTPRequest(map[string]interface{}{
			"url": "https://example.com", "method": "POST",
		}, tmplCtx)
		if err != nil {
			t.Fatalf("buildHTTPRequest = %v", err)
		}
		if req.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", req.Method)
		}
	})

	t.Run("body_type decides the default content type", func(t *testing.T) {
		for bodyType, want := range map[string]string{
			"":      "application/json",
			"json":  "application/json",
			"form":  "application/x-www-form-urlencoded",
			"other": "", // only json / form are known; anything else leaves the header alone
		} {
			req, err := buildHTTPRequest(map[string]interface{}{
				"url": "https://example.com", "method": "POST", "body": `{"a":1}`, "body_type": bodyType,
			}, tmplCtx)
			if err != nil {
				t.Fatalf("body_type %q: buildHTTPRequest = %v", bodyType, err)
			}
			if got := req.Header.Get("Content-Type"); got != want {
				t.Errorf("body_type %q: Content-Type = %q, want %q", bodyType, got, want)
			}
			body, _ := io.ReadAll(req.Body)
			if string(body) != `{"a":1}` {
				t.Errorf("body_type %q: body = %q, want the rendered body", bodyType, body)
			}
		}
	})

	t.Run("an explicit content type wins", func(t *testing.T) {
		req, err := buildHTTPRequest(map[string]interface{}{
			"url": "https://example.com", "method": "POST", "body": "a=1",
			"headers": []interface{}{map[string]interface{}{"key": "Content-Type", "value": "text/plain"}},
		}, tmplCtx)
		if err != nil {
			t.Fatalf("buildHTTPRequest = %v", err)
		}
		if got := req.Header.Get("Content-Type"); got != "text/plain" {
			t.Errorf("Content-Type = %q, want the explicit header", got)
		}
	})

	t.Run("no body means no content type", func(t *testing.T) {
		req, err := buildHTTPRequest(map[string]interface{}{
			"url": "https://example.com", "method": "POST",
		}, tmplCtx)
		if err != nil {
			t.Fatalf("buildHTTPRequest = %v", err)
		}
		if got := req.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want empty when there is no body", got)
		}
	})

	t.Run("url is required", func(t *testing.T) {
		if _, err := buildHTTPRequest(map[string]interface{}{}, tmplCtx); err == nil {
			t.Fatal("buildHTTPRequest without url must fail")
		}
	})
}
