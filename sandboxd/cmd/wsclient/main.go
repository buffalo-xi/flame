// Command wsclient 是一个最小的 sandboxd 双工脚本测试客户端：
// 连 /v1/code，发 open/exec/close，打印 ready 与结果帧。
//
// 仅用于手工验证脚本端点，不属于正式交付物。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: wsclient <ws-url> <lang> <script>")
		os.Exit(2)
	}
	url, lang, script := os.Args[1], os.Args[2], os.Args[3]

	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()

	send := func(m map[string]any) {
		if err := c.WriteJSON(m); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}
	recv := func() map[string]any {
		var f map[string]any
		if err := c.ReadJSON(&f); err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		return f
	}

	send(map[string]any{"type": "open", "lang": lang})
	r := recv()
	fmt.Printf("ready: type=%v script_id=%v reused=%v\n", r["type"], r["script_id"], r["reused"])

	send(map[string]any{"type": "exec", "script": script})
	r = recv()
	b, _ := json.Marshal(r)
	fmt.Printf("result: %s\n", string(b))

	send(map[string]any{"type": "close"})
	time.Sleep(100 * time.Millisecond)
}
