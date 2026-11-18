// Command iwclient 是 /v1/interactive 长交互端点的手工冒烟客户端：
// 两次连接喂命令，验证进程跨连接存活、状态保留。
//
// 仅用于手工验证，不属于正式交付物。
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const url = "ws://127.0.0.1:27081/v1/interactive"

func main() {
	mounts := []map[string]any{
		{"host": "D:/proj/flame/nexus/data", "guest": "/workspace", "read_only": false},
	}
	argv := []string{"cmd"}

	fmt.Println("=== 1st: create process, set SMOKE_VAR=hello123 ===")
	out1 := run("s_smoke", "shell", argv, mounts, "set SMOKE_VAR=hello123\r\n")
	fmt.Printf("stdout: %q\n", out1)

	fmt.Println("=== 2nd: reuse process, read SMOKE_VAR ===")
	out2 := run("s_smoke", "shell", nil, nil, "echo %SMOKE_VAR%\r\n")
	fmt.Printf("stdout: %q\n", out2)

	if strings.Contains(out2, "hello123") {
		fmt.Println("\nPASS: process survived across connections, state preserved")
		return
	}
	fmt.Println("\nFAIL: state not preserved")
	os.Exit(1)
}

// run 做一次连接：open → 喂命令 → 读 stdout → 断开。
// argv 非空表示首次创建（带 argv + mounts）；nil 表示复用（只带 session_id + tool_id）。
func run(sessionID, toolID string, argv []string, mounts []map[string]any, input string) string {
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()

	open := map[string]any{
		"type":       "open",
		"session_id": sessionID,
		"tool_id":    toolID,
	}
	if argv != nil {
		open["spec"] = map[string]any{
			"argv":   argv,
			"mounts": mounts,
		}
	}
	if err := c.WriteJSON(open); err != nil {
		fmt.Fprintln(os.Stderr, "write open:", err)
		os.Exit(1)
	}

	var f map[string]any
	if err := c.ReadJSON(&f); err != nil {
		fmt.Fprintln(os.Stderr, "read ready:", err)
		os.Exit(1)
	}
	fmt.Printf("ready: type=%v\n", f["type"])
	if f["type"] == "error" {
		fmt.Printf("error: %v\n", f["message"])
		os.Exit(1)
	}

	if input != "" {
		buf := make([]byte, 1+len(input))
		buf[0] = 3 // ChanStdin
		copy(buf[1:], input)
		if err := c.WriteMessage(websocket.BinaryMessage, buf); err != nil {
			fmt.Fprintln(os.Stderr, "write stdin:", err)
			os.Exit(1)
		}
	}

	// 读输出直到超时（进程长驻，输出流不会自然结束）
	out := ""
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		typ, data, err := c.ReadMessage()
		if err != nil {
			break
		}
		if typ == websocket.BinaryMessage && len(data) > 0 && data[0] == 1 { // ChanStdout
			out += string(data[1:])
		}
	}
	return out
}
