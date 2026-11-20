// Command codehost is sandboxd's script execution host: a resident process running inside the isolation unit (nsjail /
// none) that forks a fresh interpreter process (lua / python / node) on behalf of sandboxd for every script.
//
// Its relationship with sandboxd: sandboxd handles isolation and scheduling, codehost handles "running scripts
// repeatedly and cleanly inside the same isolation unit". It does not import daemon / isolate (which pull in gin and
// docker) and depends only on the standard library — keeping this binary small enough to drop into a rootfs on its own.
//
// Protocol (stdin / stdout, 4-byte big-endian length prefix + JSON):
//
//	sandboxd → codehost: {"action":"execute","lang":"lua","command":"lua","script":"...","inputs":{...},"timeout_ms":5000}
//	sandboxd → codehost: {"action":"shutdown"}
//	codehost → sandboxd: {"action":"done","result":X} | {"action":"error","error":"msg"}
//
// A script's print / log / console output goes to stderr (forwarded to sandboxd's stderr), and stdout carries only the
// structured result.
package main

import (
	"os"

	"flame/sandboxd/internal/codehost"
)

func main() {
	if err := codehost.Run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}
