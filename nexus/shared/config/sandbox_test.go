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
