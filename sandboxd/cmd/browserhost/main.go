// Command browserhost is sandboxd's browser host: it runs inside the isolation unit, keeps a Chromium alive on behalf
// of sandboxd, and accepts operations over a "one JSON object per line" protocol.
//
// Its relationship with sandboxd is the same as codehost's: sandboxd handles isolation and scheduling, while this
// handles "driving one browser continuously inside the same isolation unit". It deliberately does not import daemon /
// isolate (which pull in gin and docker), so this binary can be dropped into an image / rootfs on its own.
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

	"flame/sandboxd/internal/browserhost"
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
