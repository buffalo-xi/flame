package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"flame/tools/sandbox"

	"flame/sandboxd/isolate"
)

// 阶段②端到端验证：none 模式下 spawnCodehost 启动真实 codehost 二进制，经 Worker
// 协议执行真实解释器（python / node）。缺 codehost 二进制或解释器则跳过。
//
// codehost 二进制定位优先级：SANDBOXD_CODEHOST_BIN → sandboxd/bin/ → PATH。
// 构建：cd sandboxd && go build -o bin/codehost ./cmd/codehost

func findCodehostBinary(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("SANDBOXD_CODEHOST_BIN"); b != "" {
		return b
	}
	name := "codehost"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// 相对 internal/daemon 包 → sandboxd/bin/
	if local := filepath.Join("..", "..", "bin", name); fileExists(local) {
		return local
	}
	if b, err := exec.LookPath("codehost"); err == nil {
		return b
	}
	t.Skip("codehost binary not found (build: go build -o bin/codehost ./cmd/codehost)")
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// findLua 定位 lua 解释器：PATH 常见名 → 本机 none 模式固定路径
// （sandboxd/config.yaml 的 langs.lua.commands.none）。
func findLua() string {
	for _, name := range []string{"lua", "lua55", "lua5.5", "lua5.4", "lua5.3"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if fileExists(`d:/tools/lua5.5/lua55.exe`) {
		return `d:/tools/lua5.5/lua55.exe`
	}
	return ""
}

// TestCodehostIntegration：spawn + 单次 execute 的结果 / 超时路径。
func TestCodehostIntegration(t *testing.T) {
	bin := findCodehostBinary(t)
	iso := isolate.NewNone()

	python, _ := exec.LookPath("python")
	node, _ := exec.LookPath("node")
	lua := findLua()

	execute := func(t *testing.T, lang, command, script string, inputs any, timeoutMS int64) (string, map[string]any) {
		t.Helper()
		w, err := spawnCodehost(iso, bin, t.TempDir(), sandbox.Limits{})
		if err != nil {
			t.Fatalf("spawnCodehost: %v", err)
		}
		defer w.Destroy()

		if err := w.Send(map[string]any{
			"action":     "execute",
			"lang":       lang,
			"command":    command,
			"script":     script,
			"inputs":     inputs,
			"timeout_ms": timeoutMS,
		}); err != nil {
			t.Fatalf("send execute: %v", err)
		}
		msg, err := w.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		return msg["action"].(string), msg
	}

	if python == "" {
		t.Log("python not found, skipping python cases")
	} else {
		t.Run("python result", func(t *testing.T) {
			action, msg := execute(t, "python", python, `__return__ = input["a"] + 1`, map[string]any{"a": 41}, 0)
			if action != "done" {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
			if n, ok := msg["result"].(json.Number); !ok || n.String() != "42" {
				t.Fatalf("result=%v (%T), want 42", msg["result"], msg["result"])
			}
		})
		t.Run("python timeout", func(t *testing.T) {
			action, msg := execute(t, "python", python, `import time; time.sleep(5)`, map[string]any{}, 100)
			if action != "error" || !strings.Contains(msg["error"].(string), "timed out") {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
		})
	}

	if node == "" {
		t.Log("node not found, skipping node cases")
	} else {
		t.Run("node result", func(t *testing.T) {
			action, msg := execute(t, "js", node, `return input.a * 2`, map[string]any{"a": 21}, 0)
			if action != "done" {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
			if n, ok := msg["result"].(json.Number); !ok || n.String() != "42" {
				t.Fatalf("result=%v (%T), want 42", msg["result"], msg["result"])
			}
		})
	}

	if lua == "" {
		t.Log("lua not found, skipping lua cases")
	} else {
		t.Run("lua result", func(t *testing.T) {
			action, msg := execute(t, "lua", lua, `return input.a + 1`, map[string]any{"a": 41}, 0)
			if action != "done" {
				t.Fatalf("action=%s msg=%v", action, msg)
			}
			if n, ok := msg["result"].(json.Number); !ok || n.String() != "42" {
				t.Fatalf("result=%v (%T), want 42", msg["result"], msg["result"])
			}
		})
	}
}

// TestCodehostReuse：同一个 codehost 常驻复用，连续多次 execute 各 fork 全新解释器。
func TestCodehostReuse(t *testing.T) {
	bin := findCodehostBinary(t)
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python not found")
	}

	w, err := spawnCodehost(isolate.NewNone(), bin, t.TempDir(), sandbox.Limits{})
	if err != nil {
		t.Fatalf("spawnCodehost: %v", err)
	}
	defer w.Destroy()

	for i := 1; i <= 3; i++ {
		if err := w.Send(map[string]any{
			"action":     "execute",
			"lang":       "python",
			"command":    python,
			"script":     `__return__ = input["n"] + 1`,
			"inputs":     map[string]any{"n": i},
			"timeout_ms": 5000,
		}); err != nil {
			t.Fatalf("send #%d: %v", i, err)
		}
		msg, err := w.Recv()
		if err != nil {
			t.Fatalf("recv #%d: %v", i, err)
		}
		if msg["action"] != "done" {
			t.Fatalf("#%d action=%s msg=%v", i, msg["action"], msg)
		}
		if n, ok := msg["result"].(json.Number); !ok || n.String() != strconv.Itoa(i+1) {
			t.Fatalf("#%d result=%v (%T), want %d", i, msg["result"], msg["result"], i+1)
		}
	}
}
