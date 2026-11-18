// Command codehost 是 sandboxd 的脚本执行宿主：一个跑在隔离单元内部（nsjail / none）
// 的常驻进程，替 sandboxd 逐次 fork 全新解释器进程（lua / python / node）执行脚本。
//
// 与 sandboxd 的关系：sandboxd 负责隔离与调度，codehost 负责「在同一隔离单元里
// 反复、干净地跑脚本」。它不 import daemon / isolate（那些依赖 gin、docker），
// 只依赖标准库 —— 保证这个二进制尽量小、可单独塞进 rootfs。
//
// 协议（stdin / stdout，4 字节大端长度前缀 + JSON）：
//
//	sandboxd → codehost: {"action":"execute","lang":"lua","command":"lua","script":"...","inputs":{...},"timeout_ms":5000}
//	sandboxd → codehost: {"action":"shutdown"}
//	codehost → sandboxd: {"action":"done","result":X} | {"action":"error","error":"msg"}
//
// 脚本的 print / log / console 输出走 stderr（转发给 sandboxd 日志），stdout 只承载
// 结构化结果。
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
