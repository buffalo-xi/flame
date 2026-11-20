package codehost

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// ---------- Unit tests (no interpreter required) ----------

func TestLuaLiteral(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, "nil"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"string", "hi", `"hi"`},
		{"string escape", `a"b\c`, `"a\"b\\c"`},
		{"int", json.Number("42"), "42"},
		{"array", []any{json.Number("1"), "two"}, `{1, "two"}`},
		{"map", map[string]any{"b": json.Number("2"), "a": json.Number("1")}, `{["a"] = 1, ["b"] = 2}`},
		{"nested", map[string]any{"x": []any{json.Number("1")}}, `{["x"] = {1}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := luaLiteral(c.in); got != c.want {
				t.Fatalf("luaLiteral(%v) = %s, want %s", c.in, got, c.want)
			}
		})
	}
}

func TestLuaLongBracket(t *testing.T) {
	// When the script itself contains ]], '=' must be added automatically to avoid closing it early.
	got := luaLongBracket("x = ']]'")
	if !strings.Contains(got, "[=[x = ']]']=]") {
		t.Fatalf("unexpected long bracket: %q", got)
	}
	got2 := luaLongBracket("plain")
	if !strings.Contains(got2, "[[plain]]") {
		t.Fatalf("unexpected long bracket: %q", got2)
	}
}

// ---------- End to end (needs an interpreter, skipped when missing) ----------

func TestEndToEnd(t *testing.T) {
	python, _ := exec.LookPath("python")
	node, _ := exec.LookPath("node")

	if python == "" {
		t.Log("python not found, skipping python cases")
	} else {
		t.Run("python result", func(t *testing.T) {
			action, msg := runScript(t, "python", python,
				`__return__ = input["a"] + 1`, map[string]any{"a": 41}, 0)
			if action != "done" {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
			if got := numResult(msg); got != "42" {
				t.Fatalf("result=%v, want 42", msg["result"])
			}
		})
		t.Run("python print to stderr", func(t *testing.T) {
			_, msg, errBuf := runScriptFull(t, "python", python,
				`print("hello")`, map[string]any{}, 0)
			if msg["action"] != "done" {
				t.Fatalf("unexpected msg: %v", msg)
			}
			if !strings.Contains(errBuf, "hello") {
				t.Fatalf("stderr should contain print output, got %q", errBuf)
			}
			// New behaviour: print output also goes into the logs field of the done frame (returned to nexus)
			if logs, _ := msg["logs"].(string); !strings.Contains(logs, "hello") {
				t.Fatalf("done.logs should contain print output, got %q", logs)
			}
		})
		t.Run("python error", func(t *testing.T) {
			action, msg := runScript(t, "python", python,
				`raise ValueError("boom")`, map[string]any{}, 0)
			if action != "error" || !strings.Contains(msg["error"].(string), "boom") {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
			// New behaviour: the error frame's logs carry the full traceback, while the error field stays a clean
			// message
			if logs, _ := msg["logs"].(string); !strings.Contains(logs, "Traceback") {
				t.Fatalf("error.logs should contain traceback, got %q", logs)
			}
		})
		t.Run("python logs truncated", func(t *testing.T) {
			action, msg := runScript(t, "python", python,
				`print("x" * 200000)`, map[string]any{}, 0)
			if action != "done" {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
			if msg["logs_truncated"] != true {
				t.Fatalf("logs_truncated should be true, got %v", msg["logs_truncated"])
			}
			logs, _ := msg["logs"].(string)
			if len(logs) > maxStderrCapture {
				t.Fatalf("logs length %d exceeds cap %d", len(logs), maxStderrCapture)
			}
			if len(logs) == 0 {
				t.Fatalf("logs should retain a tail, got empty")
			}
		})
		t.Run("python timeout", func(t *testing.T) {
			action, msg := runScript(t, "python", python,
				`import time; time.sleep(5)`, map[string]any{}, 100)
			if action != "error" || !strings.Contains(msg["error"].(string), "timed out") {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
		})
	}

	if node == "" {
		t.Log("node not found, skipping node cases")
	} else {
		t.Run("node result", func(t *testing.T) {
			action, msg := runScript(t, "js", node,
				`return input.a + 1`, map[string]any{"a": 41}, 0)
			if action != "done" || numResult(msg) != "42" {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
		})
		t.Run("node async result", func(t *testing.T) {
			action, msg := runScript(t, "js", node,
				`await new Promise(r => setTimeout(r, 10)); return 7`, map[string]any{}, 0)
			if action != "done" || numResult(msg) != "7" {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
		})
		t.Run("node error", func(t *testing.T) {
			action, msg := runScript(t, "js", node,
				`throw new Error("boom")`, map[string]any{}, 0)
			if action != "error" || !strings.Contains(msg["error"].(string), "boom") {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
		})
	}

	lua := findLua()
	if lua == "" {
		t.Log("lua not found, skipping lua cases")
	} else {
		t.Run("lua result", func(t *testing.T) {
			action, msg := runScript(t, "lua", lua,
				`return input.a + 1`, map[string]any{"a": 41}, 0)
			if action != "done" || numResult(msg) != "42" {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
		})
		t.Run("lua print to stderr", func(t *testing.T) {
			_, msg, errBuf := runScriptFull(t, "lua", lua,
				`print("hello")`, map[string]any{}, 0)
			if msg["action"] != "done" {
				t.Fatalf("unexpected msg: %v", msg)
			}
			if !strings.Contains(errBuf, "hello") {
				t.Fatalf("stderr should contain print output, got %q", errBuf)
			}
		})
		t.Run("lua error", func(t *testing.T) {
			action, msg := runScript(t, "lua", lua,
				`error("boom")`, map[string]any{}, 0)
			if action != "error" || !strings.Contains(msg["error"].(string), "boom") {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
		})
	}
}

// ---------- Helpers ----------

func frame(v any) []byte {
	data, _ := json.Marshal(v)
	h := make([]byte, 4)
	binary.BigEndian.PutUint32(h, uint32(len(data)))
	return append(h, data...)
}

// runScript runs one execute and returns (action, msg).
func runScript(t *testing.T, lang, command, script string, inputs any, timeoutMS int64) (string, map[string]any) {
	t.Helper()
	_, msg, _ := runScriptFull(t, lang, command, script, inputs, timeoutMS)
	return msg["action"].(string), msg
}

// runScriptFull runs one execute and returns (action, msg, stderr).
func runScriptFull(t *testing.T, lang, command, script string, inputs any, timeoutMS int64) (string, map[string]any, string) {
	t.Helper()
	var in bytes.Buffer
	in.Write(frame(map[string]any{
		"action":     "execute",
		"lang":       lang,
		"command":    command,
		"script":     script,
		"inputs":     inputs,
		"timeout_ms": timeoutMS,
	}))
	in.Write(frame(map[string]any{"action": "shutdown"}))

	var out, errBuf bytes.Buffer
	if err := Run(&in, &out, &errBuf); err != nil {
		t.Fatalf("Run: %v", err)
	}

	msg := readFrame(t, &out)
	return msg["action"].(string), msg, errBuf.String()
}

func readFrame(t *testing.T, r *bytes.Buffer) map[string]any {
	t.Helper()
	h := make([]byte, 4)
	if _, err := r.Read(h); err != nil {
		t.Fatalf("read header: %v", err)
	}
	n := binary.BigEndian.Uint32(h)
	body := make([]byte, n)
	if _, err := r.Read(body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

// numResult normalises result into a string (readFrame uses UseNumber, so numbers are json.Number).
func numResult(msg map[string]any) string {
	if v, ok := msg["result"].(json.Number); ok {
		return v.String()
	}
	return ""
}

// findLua locates the lua interpreter: the common names on PATH → the fixed local path of the none mode
// (langs.lua.commands.none in sandboxd/config.yaml).
func findLua() string {
	for _, name := range []string{"lua", "lua55", "lua5.5", "lua5.4", "lua5.3"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if st, err := os.Stat(`d:/tools/lua5.5/lua55.exe`); err == nil && !st.IsDir() {
		return `d:/tools/lua5.5/lua55.exe`
	}
	return ""
}
