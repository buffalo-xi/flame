package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"

	"github.com/google/uuid"
)

// tmpGuestDir worker 可写目录在沙箱内的挂载点，同时充当脚本的 cwd。
//
// 为什么用 /tmp 这个通用路径：bind mount 的目标必须已存在，而 /tmp 在任何
// rootfs / 镜像里都有；换一个自造路径就得去改每一个 rootfs。
const tmpGuestDir = "/tmp"

const (
	// reapTimeout 回收被丢弃 worker 的最长等待：超时就不等了，绝不拖住归还路径。
	reapTimeout = 5 * time.Second
	// removeTmpDir 的删除重试（见 removeTmpDir 注释）。
	tmpRemoveAttempts   = 3
	tmpRemoveRetryDelay = 50 * time.Millisecond
)

// prepareTmpRoot 准备 worker 可写目录的宿主根目录。
//
// 启动时把整棵目录清空，于是运行期完全不需要删除单个 worker 的目录：
//   - 进程被杀时目录可能仍挂在 jail / 容器上，此时 RemoveAll 会 EBUSY；
//   - 上次异常退出留下的残骸也一并清掉。
//
// 代价是运行期目录只增不减，但池容量有上限，量级可控。
func prepareTmpRoot(root string) error {
	if root == "" {
		return fmt.Errorf("worker: tmp_dir is not configured")
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("worker: clean tmp root %s: %w", root, err)
	}
	if err := os.MkdirAll(root, 0o777); err != nil {
		return fmt.Errorf("worker: create tmp root %s: %w", root, err)
	}
	return nil
}

// Worker 封装单个 worker 进程及其 stdin/stdout 管道。
type Worker struct {
	ID     string
	stdin  io.WriteCloser
	stdout io.ReadCloser
	proc   sandbox.Process
	// alive 由 watch 维护（进程退出即置 false）与 Kill 置位，读侧只调 Alive()。
	// 用原子量是因为「池在借出/归还线程读」与「watch goroutine 写」是并发的。
	alive atomic.Bool
	// exited 在进程真正退出（Wait 返回）时关闭，是「子进程已被回收」的唯一信号。
	exited chan struct{}
	// tmpDir 本 worker 独占的可写目录（挂进沙箱充当 /tmp，同时是脚本 cwd）。
	// 销毁时删除；删除失败留给下次启动的 prepareTmpRoot 兜底。
	tmpDir string
}

// spawnCodehost 在隔离环境内启动一个常驻的 codehost 宿主进程，通过 stdin/stdout
// 通信。它是三种 mode（none / nsjail / docker）共用的脚本执行器：sandboxd 长驻它，
// 每次 execute 由它 fork 全新解释器进程。docker 下 codehost 来自镜像内
// /usr/local/bin/codehost（见 sandboxd/Dockerfile），改代码后必须重建镜像。
//
// 与旧 spawnWorker（L3 热解释器）的差别：
//   - 启动的是 codehost 二进制（单 argv），而不是「<解释器> <worker 脚本>」；
//   - 不挂载 workersDir —— codehost 内嵌包装模板，不再需要 worker.lua/py/js；
//   - 解释器入口（command）在每次 execute 时才随请求下发，由 codehost 去 fork。
//
// 路径约定同旧 worker：tmpRoot 下新建的独占子目录 → tmpGuestDir（可写 + cwd）。
// codehost 的 cwd 是 /tmp，它 fork 的解释器继承这个 cwd，脚本写相对路径就落在
// 本会话独占的 tmpDir 里，随 Worker 销毁一并清掉。
func spawnCodehost(sb sandbox.Sandbox, binary, tmpRoot string, limits sandbox.Limits) (*Worker, error) {
	if sb == nil {
		return nil, fmt.Errorf("worker: no isolate implementation")
	}

	binary, err := resolveCodehostBinary(binary)
	if err != nil {
		return nil, fmt.Errorf("worker: abs codehost binary: %w", err)
	}

	// ⚠️ tmpRoot 必须转绝对：os.MkdirTemp 会原样继承传入的相对路径，而 docker 的
	// -v 只接受绝对宿主路径（相对路径会被当作 named volume）。nsjail 的 chroot 同样
	// 只认宿主绝对路径，这里统一转掉。
	absTmpRoot, err := filepath.Abs(tmpRoot)
	if err != nil {
		return nil, fmt.Errorf("worker: abs tmp dir: %w", err)
	}

	// 每个 codehost 一个独占的可写目录，避免互相看见对方的临时文件。
	//
	// ⚠️ chmod 0777 不能省：MkdirTemp 建出来是 0700、属主是 sandboxd，而进程在
	// nsjail 内是另一个 uid（--user 99999）—— 不放宽的话 fork 出的解释器连一个
	// 临时文件都写不出来（EACCES）。
	tmpDir, err := os.MkdirTemp(absTmpRoot, "codehost-")
	if err != nil {
		return nil, fmt.Errorf("worker: create tmp dir under %s: %w", tmpRoot, err)
	}
	if err := os.Chmod(tmpDir, 0o777); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("worker: chmod tmp dir %s: %w", tmpDir, err)
	}

	spec := &sandbox.Spec{
		Argv: []string{binary},
		Dir:  tmpGuestDir, // cwd 必须是可写目录
		// 解释器 stderr（print / traceback）不直出屏幕：它们已通过 stderrCollector
		// 尾部留存进 done/error 帧的 logs 字段回传（错误另由 handle_code 落 xlog），
		// 这里丢弃转发即可，避免 sandboxd 终端被脚本输出刷屏。
		Stderr: io.Discard,
		Mounts: []sandbox.Mount{
			{Host: tmpDir, Guest: tmpGuestDir},
		},
		Limits: limits,
	}

	// 防御性断言：Dir 必须落在挂载点之下。
	if err := guardDir(spec.Dir, spec.Mounts); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("worker: internal path error: %w", err)
	}

	proc, err := sb.Start(context.Background(), spec)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("worker: start codehost: %w", err)
	}

	w := &Worker{
		ID:     uuid.NewString(),
		stdin:  proc.Stdin(),
		stdout: proc.Stdout(),
		proc:   proc,
		exited: make(chan struct{}),
		tmpDir: tmpDir,
	}
	w.alive.Store(true)

	// 退出监视：子进程的回收只由它负责（见 watch），池的热路径据此判断「还能不能用」，
	// 不必每次借出 / 归还都发一轮 ping。
	go w.watch()

	// 健康检查：发 ping，等 pong，确认 codehost 真正启动成功。
	// 只在建立连接时做这一次；每次 execute 的热路径上不再探活。
	if err := w.ping(); err != nil {
		w.Destroy()
		return nil, fmt.Errorf("worker: codehost health check: %w", err)
	}
	return w, nil
}

// resolveCodehostBinary 归一化 codehost 二进制路径。
//
// binary 可能是纯命令名（走 PATH）、容器内路径（/usr/local/bin/codehost）或宿主
// 路径（相对 / Windows 绝对）。只对「宿主侧」路径做 Abs 转换：
//   - Unix 绝对路径（/ 开头）是容器内路径，语义属于容器，必须原样透传 ——
//     filepath.Abs 是平台相关的：在 Windows 上它会把 /usr/local/bin/codehost
//     转成 d:\usr\...，docker/nsjail 就 exec 不到它了（报 read header: EOF 的根因）；
//   - 其余含分隔符的路径按 sandboxd 的 cwd 转绝对：none 下 exec 的 cmd.Dir 会被
//     设成本会话独占的 tmpDir，相对路径会相对 tmpDir 解析而落空（no such file）；
//   - 纯命令名不动，交给 exec 走 PATH。
func resolveCodehostBinary(binary string) (string, error) {
	switch {
	case strings.HasPrefix(binary, "/"):
		return binary, nil
	case strings.ContainsAny(binary, `/\`):
		abs, err := filepath.Abs(binary)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	return binary, nil
}

// watch 回收子进程并维护存活标志。
//
// 为什么由它独占 Wait：exec.Cmd.Wait 不能被并发 / 重复调用，而 Kill 之后必须有人
// 回收子进程（否则僵尸态会堆在进程表里）。所以「谁 Wait」只有一个答案 —— 这里；
// Destroy 只等 exited，不再二次调用 Wait。
//
// 这件事把「探活」从热路径上摘掉了：进程一退出 alive 就是 false，借出 / 归还
// 只需读这个标志，不必发「ping → 等 pong」的同步往返（lua 是 µs 级调用，那两次
// 往返的固定开销会盖过脚本本身）。janitor 仍会每 30s 主动 ping 一次兜底。
func (w *Worker) watch() {
	_ = w.proc.Wait() // 进程已退出 / 被杀，返回错误属正常
	w.alive.Store(false)
	close(w.exited)
}

// ping 发送 ping 并等待 pong，验证 worker 进程存活。
//
// 只在建立连接时（健康检查）与 janitor 巡检时使用，**不在借出 / 归还的热路径上**。
func (w *Worker) ping() error {
	if err := sendMsg(w.stdin, map[string]any{"action": "ping"}); err != nil {
		return fmt.Errorf("send ping: %w", err)
	}

	type result struct {
		msg map[string]any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := recvMsg(w.stdout)
		ch <- result{msg, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("recv pong: %w", r.err)
		}
		action, _ := r.msg["action"].(string)
		if action != "pong" {
			return fmt.Errorf("unexpected response: %s", action)
		}
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout waiting for pong")
	}
}

// Send 发送消息给 worker（写入 stdin）。
func (w *Worker) Send(msg map[string]any) error { return sendMsg(w.stdin, msg) }

// Recv 从 worker 接收消息（读取 stdout）。
func (w *Worker) Recv() (map[string]any, error) { return recvMsg(w.stdout) }

// Kill 强杀 worker 进程并标记不可复用。
func (w *Worker) Kill() {
	if !w.alive.Load() {
		return
	}
	w.alive.Store(false)
	if w.stdin != nil {
		_ = w.stdin.Close()
	}
	if w.stdout != nil {
		_ = w.stdout.Close()
	}
	if w.proc != nil {
		_ = w.proc.Kill()
	}
}

// Destroy 彻底销毁 worker：强杀 → 回收子进程 → 删除它独占的 tmp 目录。
//
// 与 Kill 的区别（restart 生命周期下每次会话结束都会走这里，不能省）：
//   - Kill 只发信号，**不 Wait** —— 隔离实现的 Process.Kill 不回收，被丢弃的 worker
//     会以僵尸态留在进程表里。L3 下每个请求都丢弃一个 worker，不回收就是按请求泄漏。
//   - tmp 目录原先只在下次启动时整棵清空；restart 档下必须随 worker 一起删，
//     否则目录数会随请求量单调增长。
func (w *Worker) Destroy() {
	if w == nil {
		return
	}
	w.Kill()
	w.reap()
	removeTmpDir(w.tmpDir)
	w.tmpDir = ""
}

// reap 等子进程被 watch 回收（Kill 之后通常立刻返回）。
// 加超时只是防御「信号没送达 / 进程 D 态」这类极端情况，避免卡住归还路径。
// 这里**不**自己调 Wait：exec.Cmd.Wait 不能并发调用，回收由 watch 独占。
func (w *Worker) reap() {
	if w.proc == nil {
		return
	}
	select {
	case <-w.exited:
	case <-time.After(reapTimeout):
		// 回收不了也不阻塞调用方：留给进程表兜底，至少不拖垮服务
	}
}

// removeTmpDir 删除 worker 独占目录。
//
// 带重试是因为进程刚被杀时，隔离层（nsjail 的 mount namespace / docker 的挂载）
// 可能还没释放，此刻 RemoveAll 会 EBUSY；retry 后一般就好了。彻底失败也不致命 ——
// 下次启动的 prepareTmpRoot 会把整棵 tmp_root 清空。
func removeTmpDir(dir string) {
	if dir == "" {
		return
	}
	for attempt := 0; attempt < tmpRemoveAttempts; attempt++ {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(tmpRemoveRetryDelay)
	}
	xlog.Error("sandboxd: remove worker tmp dir %s failed, left for next startup cleanup", dir)
}

// Alive 返回 worker 是否仍被认为存活（进程退出由 watch 置 false）。
func (w *Worker) Alive() bool { return w.alive.Load() }
