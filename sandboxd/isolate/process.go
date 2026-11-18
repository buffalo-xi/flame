package isolate

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"flame/tools/sandbox"
)

// waitDelayAfterKill cmd.Wait 在进程退出后等待 I/O 管道收尾的上限。
//
// 为什么必须设：只要 Stderr 不是 *os.File（handle_exec 传的是 websocket 帧写入器），
// os/exec 就会为它起一个内部复制 goroutine，而 Wait 会等这个 goroutine 结束。
// 这段等待的唯一出口是管道 EOF —— 而 stderr 是被整棵进程树继承的 fd，
// 只要有一个孙进程还攥着它，EOF 就永远不来，Wait 会永久卡住。
// 设了它，Wait 最多等这么久，然后关掉管道返回 os/exec.ErrWaitDelay
// （见 os/exec 的 awaitGoroutines）。
const waitDelayAfterKill = 5 * time.Second

// killFunc 收掉「这次执行的全部后果」。
//
// 为什么是一个函数而不是直接 cmd.Process.Kill()：一次执行往往是一棵进程树，
// 而 docker 模式下被执行的东西根本不在宿主进程树里（容器由 dockerd 托管）——
// 这两种情况下「杀一个 pid」都不成立。默认实现见 sysproc_*.go（进程组 /
// taskkill /T），docker 会换成「先删容器、再收 CLI」。
//
// 返回值遵循 exec.Cmd.Cancel 的约定：已结束返回 os.ErrProcessDone。
type killFunc func() error

// execProcess 是所有本地隔离实现共用的 Process 实现：
// 把 *exec.Cmd 的管道包装成统一句柄，并在 Wait/Kill 后释放 cancel。
//
// 退出码统一归一化成 *sandbox.ExitError（远端实现无此限制，
// 但两端保持同一种错误类型，调用方只需处理一种）。
type execProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cancel context.CancelFunc
	kill   killFunc

	mu       sync.Mutex
	finished bool // Wait 之后为真：这次执行已经结束
}

func (p *execProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *execProcess) Stdout() io.ReadCloser { return p.stdout }

func (p *execProcess) Wait() error {
	err := p.cmd.Wait()
	p.markFinished()

	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &sandbox.ExitError{Code: ee.ExitCode()}
	}
	return err
}

// Kill 收掉这次执行。
//
// 不标记 finished：重复 Kill 是合法且有用的（客户端断开与转发收尾会各杀一次，
// 第二次是重试）—— 只有 Wait 才意味着这次执行真的结束了。
func (p *execProcess) Kill() error {
	p.mu.Lock()
	if p.finished {
		p.mu.Unlock()
		return os.ErrProcessDone
	}
	kill := p.kill
	p.mu.Unlock()

	err := kill()
	p.releaseCancel()
	return err
}

// markFinished 标记结束并释放 cancel。
func (p *execProcess) markFinished() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return
	}
	p.finished = true
	p.releaseCancelLocked()
}

// releaseCancel 释放 cancel（Kill 路径：进程已杀，ctx 的定时器没必要继续挂着）。
func (p *execProcess) releaseCancel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releaseCancelLocked()
}

func (p *execProcess) releaseCancelLocked() {
	releaseCancel(p.cancel)
	p.cancel = nil
}

// startWithPipes 建立 stdin/stdout 管道、接好 stderr，然后启动进程。
// cancel 会在进程结束（Wait/Kill）后释放，避免提前取消掉刚启动的进程。
//
// kill 是「怎么杀掉这次执行」的收口，nil 表示默认（进程组 / taskkill，见 sysproc_*.go）。
// 它同时被绑到 cmd.Cancel 上 —— 这一步不能省：exec.CommandContext 的默认取消动作是
// Process.Kill()，只杀一个 pid，会把进程组 / 容器这套收口整个绕过去
// （超时、ctx 取消都走那条路）。
//
// ⚠️ 因此要求调用方必须用 exec.CommandContext 构造 cmd：os/exec 拒绝为
// exec.Command 出来的 Cmd 设置 Cancel，会在 Start 时报
// "command with a non-nil Cancel was not created with CommandContext"。
func startWithPipes(cmd *exec.Cmd, spec *sandbox.Spec, cancel context.CancelFunc, kill killFunc) (sandbox.Process, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		releaseCancel(cancel)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		releaseCancel(cancel)
		return nil, err
	}
	// stderr 为 nil 时保持 nil：os/exec 会直接接到 /dev/null（或 Windows 的 NUL），
	// 不建复制 goroutine。若填 io.Discard 反而会多出一条 Wait 必须等待的管道 ——
	// 而孙进程能把它一直攥着（见 waitDelayAfterKill）。
	cmd.Stderr = spec.Stderr

	if kill == nil {
		kill = func() error { return killTree(cmd) }
	}
	setProcessGroup(cmd)
	cmd.Cancel = kill
	cmd.WaitDelay = waitDelayAfterKill

	if err := cmd.Start(); err != nil {
		releaseCancel(cancel)
		return nil, err
	}
	return &execProcess{cmd: cmd, stdin: stdin, stdout: stdout, cancel: cancel, kill: kill}, nil
}

// releaseCancel 安全释放 cancel。
func releaseCancel(cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
}
