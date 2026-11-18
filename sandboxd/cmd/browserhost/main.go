// Command browserhost 是 sandboxd 的浏览器宿主：跑在隔离单元内部，替 sandboxd
// 长驻一个 Chromium，并按「一行一个 JSON」的协议接受操作。
//
// 它与 sandboxd 的关系和 codehost 一样：sandboxd 负责隔离与调度，它负责
// 「在同一个隔离单元里持续驱动一个浏览器」。刻意不 import daemon / isolate
// （那些依赖 gin、docker），保证这个二进制能单独塞进镜像 / rootfs。
//
// 协议（stdin / stdout，一行一个 JSON）：
//
//	{"id":1,"op":"open","url":"https://example.com"}
//	{"id":1,"ok":true,"url":"https://example.com/","title":"Example Domain","snapshot":"..."}
//
// stdout 只走协议；日志与 Chrome 自己的输出一律走 stderr。
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
