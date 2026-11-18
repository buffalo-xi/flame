package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// 单工 / 代码片段端点由同一份基地址按用途拼出。
// 覆盖：常规、结尾斜杠、wss、旧「完整端点」写法、前后空白、未配置。
func TestSandboxEndpoints(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		wantExec string
		wantCode string
	}{
		{"基地址", "ws://127.0.0.1:27081", "ws://127.0.0.1:27081/v1/exec", "ws://127.0.0.1:27081/v1/code"},
		{"结尾斜杠", "ws://127.0.0.1:27081/", "ws://127.0.0.1:27081/v1/exec", "ws://127.0.0.1:27081/v1/code"},
		{"wss", "wss://sandboxd:443", "wss://sandboxd:443/v1/exec", "wss://sandboxd:443/v1/code"},
		{"旧写法：完整 exec 端点", "ws://h:8080/v1/exec", "ws://h:8080/v1/exec", "ws://h:8080/v1/code"},
		{"旧写法：完整 code 端点", "ws://h:8080/v1/code", "ws://h:8080/v1/exec", "ws://h:8080/v1/code"},
		// /v1/script 是 code 端点 2026-10 之前的名字。它出现在剥壳列表里，是因为
		// 端点可以写进 config.yaml（编译期外），老配置里确实会带这一截。
		{"旧写法：更名前 /v1/script", "ws://h:8080/v1/script", "ws://h:8080/v1/exec", "ws://h:8080/v1/code"},
		// /v1/session 已废弃，刻意不做剥壳 —— 于是这里拼出双路径。
		// 这是**期望行为**：配置写错了就该连不上，而不是被静默「修正」成能连。
		{"已废弃：完整 session 端点", "ws://h:8080/v1/session", "ws://h:8080/v1/session/v1/exec", "ws://h:8080/v1/session/v1/code"},
		{"前后空白", "  ws://h:8080  ", "ws://h:8080/v1/exec", "ws://h:8080/v1/code"},
		{"未配置", "", "", ""},
		{"只有空白", "   ", "", ""},
		{"只有斜杠", "/", "", ""},
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

// 钉住配置键名：基地址是 sandbox.url（不是旧的 exec_url / session_url）。
// 键名漂移时端点会算出空串，启动即被 main 拦下，这里提前守住。
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
