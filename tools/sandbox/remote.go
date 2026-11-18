package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// RemoteOptions 远端沙箱客户端的构造参数。
type RemoteOptions struct {
	// Endpoint 单工执行端点，形如 ws://sandboxd:8080/v1/exec。
	Endpoint string
	// Token 可选：作为 Bearer 令牌随握手发送。
	Token string
	// Limits 默认资源上限；Spec.Limits 中的非零字段会覆盖它。
	Limits Limits
}

// RemoteSandbox 通过 WebSocket 把「每次新建进程」的执行委托给远端沙箱服务（sandboxd）。
//
// 只承载单工（exec）语义：一条连接对应一个进程，Start → 写 stdin → 读 stdout → Wait。
// 这里的「单工」指不池化、不带回调，**不是**单向或短命 —— 协议同样是完整双向流
// （stdin / stdout / stderr + 半关闭 + kill），且服务端没有内建硬超时，
// 是否限时完全取决于 Spec.Limits / 服务端 limits.timeout_sec。
// 托管代码片段（/v1/code、走池并受 code_max_sec 约束）请用 code.Client。
//
// 注意：Spec.Mounts 中的 Host 是宿主绝对路径，要求 nexus 与 sandboxd
// 共享同一份存储且挂载路径一致；sandboxd 会用 workspace_root 做白名单校验。
type RemoteSandbox struct {
	opts RemoteOptions
}

// NewRemote 创建远端单工沙箱。Endpoint 必填。
func NewRemote(opts RemoteOptions) (*RemoteSandbox, error) {
	if opts.Endpoint == "" {
		return nil, errors.New("sandbox: remote endpoint is required")
	}
	return &RemoteSandbox{opts: opts}, nil
}

// Name 实现标识。
func (r *RemoteSandbox) Name() string { return "remote" }

// Start 在远端启动一个进程，返回桥接到网络的进程句柄。
func (r *RemoteSandbox) Start(ctx context.Context, spec *Spec) (Process, error) {
	// 启动意图二选一，与 sandboxd 的 buildExecArgv 同口径：
	//   - 语言路径（Lang 非空）：只说「哪个语言的哪个脚本」，解释器入口由 sandboxd
	//     按当前 mode 解析 —— 本侧不拼 argv（拼了就等于硬编码一份「镜像里叫什么」）；
	//   - 直传路径（Lang 为空）：argv 必须完整。
	// 两条判据都在 Dial 之前生效：意图不对就不该占用一次远端连接。
	if spec.Lang != "" {
		if len(spec.Argv) > 0 {
			return nil, fmt.Errorf("sandbox: lang %q and argv are mutually exclusive", spec.Lang)
		}
		if spec.Script == "" {
			return nil, fmt.Errorf("sandbox: lang %q requires a script", spec.Lang)
		}
	} else if len(spec.Argv) == 0 {
		return nil, errors.New("sandbox: empty argv")
	}

	wire := spec.Wire()
	wire.Limits = MergeLimits(r.opts.Limits, spec.Limits).Wire()

	conn, err := Dial(ctx, r.opts.Endpoint, r.opts.Token)
	if err != nil {
		return nil, err
	}

	// 语言名走 Frame.Lang（与双工同一个字段），脚本与参数在 WireSpec 里 ——
	// spec.Lang 为空即直传模式，sandboxd 侧行为与从前完全一致。
	if err := conn.SendFrame(&Frame{Type: FrameOpen, Lang: spec.Lang, Spec: wire}); err != nil {
		_ = conn.Close()
		return nil, err
	}

	f, _, err := conn.Recv()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox: wait ready: %w", err)
	}
	switch f.Type {
	case FrameReady:
		// ok
	case FrameError:
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox: start failed: %s", f.Message)
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox: unexpected frame %q (want ready)", f.Type)
	}

	p := newRemoteProcess(conn, spec.Stderr)
	go p.monitor(ctx)
	return p, nil
}

// connWriter 把 Write 转成二进制帧（按通道标记）。
type connWriter struct {
	c  *Conn
	ch byte
}

func (w *connWriter) Write(p []byte) (int, error) {
	if err := w.c.SendData(w.ch, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// remoteProcess 远端进程句柄：把 WS 帧桥接成 io 读写。
type remoteProcess struct {
	conn *Conn

	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderr  io.Writer

	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newRemoteProcess(conn *Conn, stderr io.Writer) *remoteProcess {
	if stderr == nil {
		stderr = io.Discard
	}
	sr, sw := io.Pipe()
	or, ow := io.Pipe()
	return &remoteProcess{
		conn:    conn,
		stdinR:  sr,
		stdinW:  sw,
		stdoutR: or,
		stdoutW: ow,
		stderr:  stderr,
		done:    make(chan struct{}),
	}
}

func (p *remoteProcess) Stdin() io.WriteCloser { return p.stdinW }
func (p *remoteProcess) Stdout() io.ReadCloser { return p.stdoutR }

// monitor 驱动整条会话：转发 stdin、读取输出、监听 ctx 取消。
func (p *remoteProcess) monitor(ctx context.Context) {
	defer close(p.done)
	defer p.conn.Close()

	go p.forwardStdin()

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Kill()
		case <-stop:
		}
	}()

	p.readLoop(ctx)
	close(stop)
}

// forwardStdin 把调用方写入的 stdin 转发给远端；管道关闭即发 stdin_eof。
func (p *remoteProcess) forwardStdin() {
	_, _ = io.Copy(&connWriter{c: p.conn, ch: ChanStdin}, p.stdinR)
	_ = p.conn.SendFrame(&Frame{Type: FrameStdinEOF})
}

// readLoop 读取远端消息：输出写入管道，exit/error 结束进程。
func (p *remoteProcess) readLoop(ctx context.Context) {
	for {
		f, data, err := p.conn.Recv()
		if err != nil {
			// ctx 取消 / Kill 导致的断开，不算异常
			if ctx.Err() != nil {
				p.finish(ctx.Err())
			} else {
				p.finish(fmt.Errorf("sandbox: connection lost: %w", err))
			}
			return
		}

		if data != nil {
			switch data[0] {
			case ChanStdout:
				_, _ = p.stdoutW.Write(data[1:])
			case ChanStderr:
				_, _ = p.stderr.Write(data[1:])
			}
			continue
		}

		switch f.Type {
		case FrameExit:
			if f.Code == 0 {
				p.finish(nil)
			} else {
				p.finish(&ExitError{Code: f.Code})
			}
			return
		case FrameError:
			p.finish(fmt.Errorf("sandbox: %s", f.Message))
			return
		}
	}
}

// finish 记录退出结果并关闭 stdout（让读取方收到 EOF）。
func (p *remoteProcess) finish(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.mu.Unlock()
	_ = p.stdoutW.Close()
}

// Wait 等待进程结束。
func (p *remoteProcess) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Kill 强杀远端进程。
func (p *remoteProcess) Kill() error {
	_ = p.conn.SendFrame(&Frame{Type: FrameKill})
	return p.conn.Close()
}
