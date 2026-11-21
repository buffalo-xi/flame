// Command browserhost is browserd's browser driver: one process per browser session, keeping one Chromium alive and
// accepting operations over a "one JSON object per line" protocol.
//
// Its relationship with browserd is the same as codehost's with sandboxd: the pool handles capacity, queueing and
// reclamation, while this handles "driving one browser continuously". It deliberately imports nothing from browserpool
// (no gin, no registry), so the binary can be dropped into an image on its own.
//
// Protocol (stdin / stdout, one JSON object per line):
//
//	{"id":1,"op":"open","url":"https://example.com"}
//	{"id":1,"ok":true,"url":"https://example.com/","title":"Example Domain","snapshot":"..."}
//
// stdout carries only the protocol; logs and Chrome's own output always go to stderr.
package main

import (
	"flag"
	"fmt"
	"os"

	"flame/browserd/internal/browserhost"
)

func main() {
	var (
		execPath      = flag.String("exec-path", "", "browser executable path (default: autodetect)")
		profileDir    = flag.String("profile", "", "chrome user-data-dir (default: temporary dir)")
		downloadsDir  = flag.String("downloads", "", "download directory (default: <cwd>/downloads)")
		allowHeaded   = flag.Bool("allow-headed", false, "allow headed (visible) browsing")
		defaultHeaded = flag.Bool("default-headed", false, "browse headed when the request does not say")
		selfTest      = flag.Bool("selftest", false, "report browser version and exit")
	)
	flag.Parse()

	if *selfTest {
		line, err := browserhost.SelfTest(*execPath)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "browserhost: selftest: %v\n", err)
			os.Exit(1)
		}
		_, _ = fmt.Fprintf(os.Stdout, "{\"engine\":\"chromium\",\"version\":%q}\n", line)
		return
	}

	err := browserhost.Run(os.Stdin, os.Stdout, os.Stderr, browserhost.Config{
		ExecPath:      *execPath,
		ProfileDir:    *profileDir,
		DownloadsDir:  *downloadsDir,
		AllowHeaded:   *allowHeaded,
		DefaultHeaded: *defaultHeaded,
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "browserhost: %v\n", err)
		os.Exit(1)
	}
}
