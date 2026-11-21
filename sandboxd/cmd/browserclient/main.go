// Command browserclient sends one browser operation to browserd and prints the response.
//
// It exists for the manual verification loop: it speaks the same wire protocol a real caller does (open frame,
// ChanStdin / ChanStdout), so "the pool works" is checked through the same path nexus will use — not through a shortcut.
//
//	browserclient -url ws://127.0.0.1:27090/v1/browser -session s1 -tenant t0 \
//	    -op "{\"id\":1,\"op\":\"open\",\"url\":\"https://example.com\"}"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"flame/tools/sandbox"
)

func main() {
	url := flag.String("url", "ws://127.0.0.1:27090/v1/browser", "browserd browser endpoint")
	token := flag.String("token", "", "bearer token (browserd token)")
	session := flag.String("session", "", "session id (required)")
	tenant := flag.String("tenant", "", "tenant id (per-tenant quota)")
	op := flag.String("op", "", "one browser host operation as JSON, for example {\"id\":1,\"op\":\"open\",\"url\":\"https://example.com\"}")
	timeout := flag.Duration("timeout", 60*time.Second, "call timeout")
	flag.Parse()

	if *session == "" || *op == "" {
		flag.Usage()
		os.Exit(2)
	}

	endpoint := *url
	if *tenant != "" {
		endpoint += "?tenant=" + *tenant
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := sandbox.NewInteractive(endpoint, *token, sandbox.Limits{})
	// argv is a placeholder: browserd launches its own driver and ignores the open frame's spec on purpose.
	out, err := client.Do(ctx, sandbox.InteractiveSession{Key: *session}, "browser",
		[]string{"browserhost"}, []byte(*op), nil, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "browserclient: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
