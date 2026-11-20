// Command wsclient is a minimal duplex script test client for sandboxd: it connects to /v1/code, sends open/exec/close
// and prints the ready and result frames.
//
// For manual verification of the script endpoint only; it is not a deliverable.
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
