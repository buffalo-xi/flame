package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// The simplex / code snippet endpoints are assembled from one base address by purpose.
// Covered: the normal form, a trailing slash, wss, the old "full endpoint" form, surrounding whitespace, unconfigured.
func TestSandboxEndpoints(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		wantExec string
		wantCode string
	}{
		{"base address", "ws://127.0.0.1:27081", "ws://127.0.0.1:27081/v1/exec", "ws://127.0.0.1:27081/v1/code"},
		{"trailing slash", "ws://127.0.0.1:27081/", "ws://127.0.0.1:27081/v1/exec", "ws://127.0.0.1:27081/v1/code"},
		{"wss", "wss://sandboxd:443", "wss://sandboxd:443/v1/exec", "wss://sandboxd:443/v1/code"},
		{"old form: full exec endpoint", "ws://h:8080/v1/exec", "ws://h:8080/v1/exec", "ws://h:8080/v1/code"},
		{"old form: full code endpoint", "ws://h:8080/v1/code", "ws://h:8080/v1/exec", "ws://h:8080/v1/code"},
		// /v1/script is the name the code endpoint had before 2026-10. It is in the strip list because the endpoint can be
		// written into config.yaml (outside compile time), and old configurations really do carry that segment.
		{"old form: /v1/script before the rename", "ws://h:8080/v1/script", "ws://h:8080/v1/exec", "ws://h:8080/v1/code"},
		// /v1/session is deprecated and deliberately not stripped — so the path is doubled here.
		// That is **expected**: a wrong configuration should fail to connect rather than be silently "corrected" into one
		// that works.
		{"deprecated: full session endpoint", "ws://h:8080/v1/session", "ws://h:8080/v1/session/v1/exec", "ws://h:8080/v1/session/v1/code"},
		{"surrounding whitespace", "  ws://h:8080  ", "ws://h:8080/v1/exec", "ws://h:8080/v1/code"},
		{"not configured", "", "", ""},
		{"whitespace only", "   ", "", ""},
		{"slash only", "/", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := SandboxConfig{URL: tc.url}
			if got := c.ExecEndpoint(); got != tc.wantExec {
				t.Errorf("ExecEndpoint() = %q, want %q", got, tc.wantExec)
			}
			if got := c.CodeEndpoint(); got != tc.wantCode {
				t.Errorf("CodeEndpoint() = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

// Pin the configuration key names: the base address is sandbox.url (not the old exec_url / session_url).
// If the key drifts the endpoints compute to empty strings and main refuses to start, so this guards it early.
func TestSandboxConfigYAMLKey(t *testing.T) {
	var c Config
	src := "sandbox:\n  url: ws://h:8080\n  token: tok\n"
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Sandbox.URL != "ws://h:8080" {
		t.Errorf("Sandbox.URL = %q, want %q", c.Sandbox.URL, "ws://h:8080")
	}
	if c.Sandbox.Token != "tok" {
		t.Errorf("Sandbox.Token = %q, want %q", c.Sandbox.Token, "tok")
	}
	if got := c.Sandbox.ExecEndpoint(); got != "ws://h:8080/v1/exec" {
		t.Errorf("ExecEndpoint() = %q, want %q", got, "ws://h:8080/v1/exec")
	}
}

// The browser pool derives two endpoints from its own base address: the WebSocket channel and the plain-HTTP capability
// snapshot. The capability one must be converted (net/http rejects ws://), and a full endpoint written into url must not
// be doubled.
func TestBrowserEndpoints(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		wantChannel string
		wantCaps    string
	}{
		{"base address", "ws://127.0.0.1:27090", "ws://127.0.0.1:27090/v1/browser", "http://127.0.0.1:27090/v1/capabilities"},
		{"trailing slash", "ws://h:27090/", "ws://h:27090/v1/browser", "http://h:27090/v1/capabilities"},
		{"wss becomes https", "wss://browserd:443", "wss://browserd:443/v1/browser", "https://browserd:443/v1/capabilities"},
		{"full browser endpoint", "ws://h:27090/v1/browser", "ws://h:27090/v1/browser", "http://h:27090/v1/capabilities"},
		{"full capabilities endpoint", "ws://h:27090/v1/capabilities", "ws://h:27090/v1/browser", "http://h:27090/v1/capabilities"},
		{"already http", "http://h:27090", "http://h:27090/v1/browser", "http://h:27090/v1/capabilities"},
		{"not configured", "", "", ""},
		{"whitespace only", "   ", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := BrowserConfig{URL: tc.url}
			if got := c.BrowserEndpoint(); got != tc.wantChannel {
				t.Errorf("BrowserEndpoint() = %q, want %q", got, tc.wantChannel)
			}
			if got := c.CapabilitiesEndpoint(); got != tc.wantCaps {
				t.Errorf("CapabilitiesEndpoint() = %q, want %q", got, tc.wantCaps)
			}
		})
	}
}

// Pin the browser key names: a typo here would silently disable the browser tool (empty url = "no browser configured").
func TestBrowserConfigYAMLKey(t *testing.T) {
	var c Config
	src := "browser:\n  url: ws://h:27090\n  token: tok\n"
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := c.Browser.BrowserEndpoint(); got != "ws://h:27090/v1/browser" {
		t.Errorf("BrowserEndpoint() = %q, want %q", got, "ws://h:27090/v1/browser")
	}
	if c.Browser.Token != "tok" {
		t.Errorf("Browser.Token = %q, want %q", c.Browser.Token, "tok")
	}
}
