// Package sandbox 提供统一的「进程隔离执行」抽象。
//
// 覆盖两种使用形态（共用同一个 Start，差异只在调用方怎么用 Process）：
//  1. 池化长驻进程：启动一次、反复通过 stdin/stdout 交互（工作流脚本池）
//  2. 一次性命令：起进程 → 写输入 → 读输出 → 等退出（agent exec 工具）
//
// 本包只保留「抽象 + 远端客户端（remote）」：
//   - nexus 侧通过 NewRemote 把执行委托给 sandboxd，自身不做隔离；
//   - 隔离实现（none / nsjail / docker）位于 sandboxd 的 isolate 包，
//     它们同样实现本包的 Sandbox 接口。
//
// 路径语义：Spec 中的路径均为「沙箱内」语义，隔离实现负责把 Mounts
// 翻译成挂载参数（none 实现则反向翻译回宿主路径）。
package sandbox

import (
	"context"
	"io"
	"time"
)

// Sandbox 一种隔离实现。
type Sandbox interface {
	// Name 实现标识：none / nsjail / docker。
	Name() string
	// Start 在沙箱内启动一个进程。
	// 长驻还是一次性由调用方决定：长驻则保持 stdin 打开反复读写；
	// 一次性则写完 Close 后 Wait（或直接用 Run）。
	Start(ctx context.Context, spec *Spec) (Process, error)
}

// Process 沙箱内进程句柄（与 *exec.Cmd 同形，两种形态都能复用）。
type Process interface {
	Stdin() io.WriteCloser // 调用方负责 Close 表示"发送结束"
	Stdout() io.ReadCloser // 进程标准输出
	Wait() error           // 等待退出（非零码通常为 *exec.ExitError）
	Kill() error           // 强制终止
}

// Spec 一次沙箱内进程的启动意图。
//
// 启动意图有两种表达方式，二选一：
//   - 语言路径（Lang 非空）：只说「跑哪个语言的哪个脚本」，解释器入口由 sandboxd
//     按当前隔离方式解析（见 Script / Args）；
//   - 直传路径（Lang 为空）：Argv 由调用方完全决定（shell 命令、自带解释器等）。
type Spec struct {
	// Lang 语言名（可选）。非空时启用语言路径：由 sandboxd 按 mode 解析解释器入口。
	//
	// 为什么需要它：解释器入口名随 mode 而变（none 看宿主 PATH、nsjail 看 chroot、
	// docker 看镜像内），而「镜像里到底叫什么」只有 sandboxd 知道。调用方自己拼
	// argv 就只能硬编码一份认知，一旦漂移就会出现「同一份脚本双工能跑、单工跑不了」。
	Lang string
	// Script 脚本在沙箱内的路径；仅在 Lang 非空时使用。
	Script string
	// Args 传给脚本的参数；仅在 Lang 非空时使用（追加在 Script 之后）。
	Args []string
	// Argv 完整命令行（可执行文件 + 参数），沙箱内路径。
	// 与 Lang 互斥：Lang 非空时本字段必须为空。
	Argv   []string
	Dir    string    // 工作目录（沙箱内路径）
	Env    []string  // 环境变量（nil 表示不额外设置）
	Stdin  []byte    // 一次性场景的输入（Run 使用；长驻场景用 Process.Stdin）
	Stderr io.Writer // stderr 去向（nil = io.Discard）
	Mounts []Mount   // 挂载表（none 实现忽略）
	Limits Limits    // 资源上限

	// Egress 网络意图：nil = 没表态（按部署默认），false = 明确要断网，true = 要出网。
	//
	// 这是**意图**不是结果：sandboxd 会按部署上限裁决后才落到隔离参数上（network=off
	// 时 true 会被拒在起进程之前）。isolate 只认「要不要把网络关掉」，它不判断策略。
	//
	// 只对 Start（单工）有意义：Acquire 建的长驻单元会跨请求复用，网络在建成时就写死，
	// 之后的调用改不了 —— 双工通道的调用方也不该设这个字段。
	Egress *bool
}

// Mount 宿主路径 → 沙箱内挂载点。
type Mount struct {
	Host     string
	Guest    string
	ReadOnly bool
}

// Limits 资源上限（0 值表示该维度不额外限制，由实现决定默认）。
type Limits struct {
	MemoryMB int
	CPUCount int
	MaxPIDs  int
	// MaxFileMB 单文件大小上限（MB）。
	MaxFileMB int
	Timeout   time.Duration
	// MaxOutputBytes 单流（stdout / stderr）收集上限，防止大输出吃爆内存。
	// 仅 Run 使用；0 表示不限制。
	MaxOutputBytes int64
}

// Result 一次性执行结果。
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	// Truncated 输出是否因 MaxOutputBytes 被截断。
	Truncated bool
}

// MergeLimits 用 spec 中的非零字段覆盖默认值。
func MergeLimits(def, override Limits) Limits {
	out := def
	if override.MemoryMB > 0 {
		out.MemoryMB = override.MemoryMB
	}
	if override.CPUCount > 0 {
		out.CPUCount = override.CPUCount
	}
	if override.MaxPIDs > 0 {
		out.MaxPIDs = override.MaxPIDs
	}
	if override.MaxFileMB > 0 {
		out.MaxFileMB = override.MaxFileMB
	}
	if override.Timeout > 0 {
		out.Timeout = override.Timeout
	}
	if override.MaxOutputBytes > 0 {
		out.MaxOutputBytes = override.MaxOutputBytes
	}
	return out
}
