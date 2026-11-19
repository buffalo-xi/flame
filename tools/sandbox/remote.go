package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// RemoteOptions configures the remote sandbox client.
type RemoteOptions struct {
	// Endpoint is the one-shot execution endpoint, e.g. ws://sandboxd:8080/v1/exec.
	Endpoint string
	// Token is optional: sent as a Bearer token during the handshake.
	Token string
	// Limits are the default resource caps; non-zero fields in Spec.Limits override them.
	Limits Limits
}

// RemoteSandbox delegates "start a process for each call" to the remote sandbox service (sandboxd) over
// WebSocket.
//
// It carries one-shot (exec) semantics only: one connection per process, Start → write stdin → read
// stdout → Wait. "One-shot" here means unpooled and without callbacks — **not** one-directional or
// short-lived: the protocol is a full duplex stream (stdin / stdout / stderr + half close + kill) and
// the server has no built-in hard timeout, so whether time is bounded depends entirely on
// Spec.Limits / the server's limits.timeout_sec. For managed code snippets (/v1/code, pooled and bounded
// by code_max_sec) use code.Client.
//
// Note: Mounts' Host is an absolute host path, which requires nexus and sandboxd to share the same
// storage with identical mount paths; sandboxd validates it against workspace_root as an allowlist.
type RemoteSandbox struct {
	opts RemoteOptions
}

// NewRemote creates a remote one-shot sandbox. Endpoint is required.
func NewRemote(opts RemoteOptions) (*RemoteSandbox, error) {
	if opts.Endpoint == "" {
		return nil, errors.New("sandbox: remote endpoint is required")
	}
	return &RemoteSandbox{opts: opts}, nil
}

// Name is the implementation identifier.
func (r *RemoteSandbox) Name() string { return "remote" }

// Start starts a process on the remote side and returns a handle bridged onto the network.
func (r *RemoteSandbox) Start(ctx context.Context, spec *Spec) (Process, error) {
	// The start intent is one of two, matching sandboxd's buildExecArgv:
	//   - language path (Lang non-empty): say only "this script of this language"; sandboxd resolves the
	//     interpreter entry point for the current mode — this side never assembles argv (doing so would
	//     hard-code one belief about "what it is called inside the image");
	//   - direct path (Lang empty): argv must be complete.
	// Both checks run before Dial: a wrong intent should not consume a remote connection.
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

	// The language name travels in Frame.Lang (the same field the duplex channel uses) and the script and
	// arguments live in WireSpec — an empty spec.Lang means direct mode, and sandboxd behaves exactly as
	// before.
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

// connWriter turns Write into binary frames (tagged by channel).
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

// remoteProcess is a handle to a remote process: it bridges WS frames into io reads and writes.
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

// monitor drives the whole session: forward stdin, read output, watch for ctx cancellation.
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

// forwardStdin forwards what the caller writes to stdin; closing the pipe sends stdin_eof.
func (p *remoteProcess) forwardStdin() {
	_, _ = io.Copy(&connWriter{c: p.conn, ch: ChanStdin}, p.stdinR)
	_ = p.conn.SendFrame(&Frame{Type: FrameStdinEOF})
}

// readLoop reads remote messages: output goes into the pipes, exit/error ends the process.
func (p *remoteProcess) readLoop(ctx context.Context) {
	for {
		f, data, err := p.conn.Recv()
		if err != nil {
			// A disconnect caused by ctx cancellation or Kill is not an anomaly.
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

// finish records the exit outcome and closes stdout (so readers see EOF).
func (p *remoteProcess) finish(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.mu.Unlock()
	_ = p.stdoutW.Close()
}

// Wait waits for the process to end.
func (p *remoteProcess) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Kill force-kills the remote process.
func (p *remoteProcess) Kill() error {
	_ = p.conn.SendFrame(&Frame{Type: FrameKill})
	return p.conn.Close()
}
