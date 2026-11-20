package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"

	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// handleExec is the simplex endpoint: one connection = one process, running until it exits.
//
// ⚠️ "Simplex" here means **not pooled** (created per call, discarded once done, no callback channel), not
// "one-directional" and not "short-lived": the wire protocol is a full duplex stream — stdin can be written
// incrementally and half-closed, stdout / stderr are forwarded on separate channels, and kill frames are
// supported. It does however **not** have the built-in hard timeout of the duplex path (compare codeDeadline):
// the only timeout comes from limits.timeout_sec (usage default → request override → global clamp, 0 = no limit).
// A long-lived interactive command can use this path directly; no new endpoint is needed.
//
// Why it is not pooled: pooling it would require "resetting process state", which costs more than starting it
// from scratch, with no reuse benefit.
func (s *Server) handleExec(c *gin.Context) {
	ws, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	wc := sandbox.NewConn(ws)
	defer func() { _ = ws.Close() }()

	// /metrics load counting: one exec connection = one in-flight.
	s.execInFlight.Add(1)
	defer s.execInFlight.Add(-1)

	// 1) open (simplex: carries the complete Spec)
	f, _, rerr := wc.Recv()
	if rerr != nil {
		return
	}
	if f.Type != sandbox.FrameOpen || f.Spec == nil {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "expected open frame with spec"})
		return
	}

	spec := f.Spec.Spec()

	// Resolve the start intent: either a language path (the interpreter is resolved by this service for the current
	// mode) or a direct path (the caller provides the whole argv).
	// A failure returns before the process is started, so a badly assembled command never reaches the isolation
	// layer.
	argv, aerr := buildExecArgv(f.Lang, spec.Argv, spec.Script, spec.Args, s.cfg.ResolveLangCommand)
	if aerr != nil {
		code := "bad_request"
		if errors.Is(aerr, ErrUnsupportedLang) {
			code = "unsupported_lang"
		}
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, ErrCode: code, Message: aerr.Error()})
		return
	}
	spec.Argv = argv
	if gerr := guardMounts(spec.Mounts, s.cfg.AllowedMountRoots); gerr != nil {
		xlog.Error("sandboxd: rejected mounts: %v", gerr)
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: gerr.Error()})
		return
	}

	// Dir must also fall under some mount point.
	//
	// none has no rootfs/image as a boundary: when TranslateToHost matches no mount point it returns the input
	// unchanged, so a host absolute path would be taken as the working directory directly. Under nsjail/docker the
	// rootfs / image itself is the boundary and an in-container /tmp is legitimate, so this is enforced for none
	// only.
	if s.cfg.Mode == isolate.NoneName {
		if derr := guardDir(spec.Dir, spec.Mounts); derr != nil {
			xlog.Error("sandboxd: rejected dir: %v", derr)
			_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: derr.Error()})
			return
		}
	}

	// The network decision must come before starting the process: when it is refused, answer with an error frame
	// straight away instead of letting isolate fail later (by then the container/process is half built, and the
	// error is harder to explain).
	if nerr := s.resolveExecNetwork(spec); nerr != nil {
		xlog.Error("sandboxd: rejected egress: %v", nerr)
		_ = wc.SendFrame(&sandbox.Frame{
			Type: sandbox.FrameError, ErrCode: "egress_denied", Message: nerr.Error(),
		})
		return
	}

	spec.Limits = s.cfg.ExecLimits(spec.Limits)
	// stderr goes back on its own channel instead of being written to sandboxd's stderr
	spec.Stderr = &wsWriter{c: wc, ch: sandbox.ChanStderr}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proc, perr := s.exec.Start(ctx, spec)
	if perr != nil {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: perr.Error()})
		return
	}
	if werr := wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameReady}); werr != nil {
		_ = proc.Kill()
		return
	}

	// 2) client → process stdin (including half-close and force kill)
	//
	// clientGone signals "nobody wants this connection anymore": the read loop is the only place that can notice
	// the client disconnecting, so it closes this channel to tell the forwarding below to wind down.
	clientGone := make(chan struct{})
	go func() {
		defer close(clientGone)
		for {
			cf, data, rerr := wc.Recv()
			if rerr != nil {
				_ = proc.Kill() // client disconnected: kill the whole process tree along with it (the container under docker)
				return
			}
			if data != nil {
				if data[0] == sandbox.ChanStdin {
					if _, werr := proc.Stdin().Write(data[1:]); werr != nil {
						return
					}
				}
				continue
			}
			switch cf.Type {
			case sandbox.FrameStdinEOF:
				_ = proc.Stdin().Close()
			case sandbox.FrameKill:
				_ = proc.Kill()
				return
			}
		}
	}()

	// 3) process stdout → client
	//
	// The forwarding must live in its own goroutine so the main goroutine can keep "a wait that can be interrupted"
	// — otherwise, once the client disconnects while the pipe is still held, this forwarding would hang forever (see
	// below).
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = io.Copy(&wsWriter{c: wc, ch: sandbox.ChanStdout}, proc.Stdout())
	}()

	// 4) Wait for this forwarding to wind down.
	//
	// The normal path is stdout EOF — but "stdout EOF" is not the same as "the process exited": stdout is an fd
	// inherited by the whole process tree, so as long as one grandchild (or a leftover process in the container)
	// still holds it, EOF never arrives.
	//
	// If the client has disconnected by then this output has nowhere to go: kill everything first, then force the
	// pipe shut to unblock the io.Copy above. Otherwise this handler stays stuck forever with a goroutine and an fd
	// — "the process was killed but the connection never finished" is not killed cleanly either.
	select {
	case <-copyDone:
	case <-clientGone:
		_ = proc.Kill()
		_ = proc.Stdout().Close()
		<-copyDone
	}

	werr := proc.Wait()
	_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameExit, Code: exitCode(werr)})
}

// resolveExecNetwork decides the request's network intent against the deployment ceiling and writes it back as an
// explicit boolean.
//
// This is the "deployment ceiling × request intent" layer; the tenant grant has already been evaluated on the nexus
// side — without a grant it never sends egress=true. The verdict is written back into spec.Egress and isolate only
// executes it, never judging policy again.
//
// Why nil stays nil: nil means "the caller expressed no opinion" (an older nexus, or a non-exec channel) and the
// deployment default applies; only an explicit false means "this one definitely must be offline". Both are offline
// under on-request, but they are treated differently under mode=none — see the branch below.
func (s *Server) resolveExecNetwork(spec *sandbox.Spec) error {
	want := spec.Egress != nil && *spec.Egress

	switch s.cfg.EffectiveNetwork() {
	case NetworkOff:
		if want {
			return errors.New("network=off: this deployment provides no egress, so the request's egress cannot be honoured")
		}
		// Clear it to nil: in this tier the fact is decided by the configuration (none / --iface_no_lo), and the
		// request's false must not take part — same conclusion, but a single source.
		spec.Egress = nil

	case NetworkOn:
		// Always online: the request's egress is ignored — the control in this tier belongs to the deployment, not
		// the caller.
		//
		// It must be explicitly cleared to nil: keeping false would make isolate start with --network none, turning
		// "on ignores the request" into "on obeys the request" — the exact opposite semantics.
		spec.Egress = nil

	case NetworkOnRequest:
		if !want && s.cfg.Mode == isolate.NoneName {
			// The host process shares the network with the outside world and cannot be cut off. When the request
			// explicitly asks for offline execution the only option is to refuse: silently running online would be a
			// lie (the agent would believe the data never left the machine).
			return errors.New("mode=none cannot provide offline execution (a host process cannot be blocked from the network); " +
				"either switch to docker / nsjail or declare network as on")
		}
		egress := want
		spec.Egress = &egress

	default:
		return fmt.Errorf("network: unknown policy %q", s.cfg.EffectiveNetwork())
	}
	return nil
}

// buildExecArgv resolves a simplex request into the final command line.
//
// Two mutually exclusive paths:
//   - language path (lang non-empty): argv = [interpreter, script, args...], the interpreter being resolved for the
//     current mode by resolve — so "what it is called inside the image" stays knowledge held only by sandboxd;
//   - direct path (lang empty): argv is entirely the caller's (shell commands, bundled interpreters), untouched.
//
// resolve is injected as a callback to make unit testing easy (no Server / config dependency needed).
func buildExecArgv(lang string, argv []string, script string, args []string,
	resolve func(string) (string, error)) ([]string, error) {

	if lang == "" {
		if len(argv) == 0 {
			return nil, errors.New("empty argv")
		}
		return argv, nil
	}
	// Passing both lang and argv would assemble an unpredictable command (such as `lua lua /x.lua`), so it is
	// rejected outright.
	if len(argv) > 0 {
		return nil, fmt.Errorf("lang %q and argv are mutually exclusive", lang)
	}
	if script == "" {
		return nil, fmt.Errorf("lang %q requires a script path", lang)
	}
	cmd, err := resolve(lang)
	if err != nil {
		return nil, err
	}
	return append([]string{cmd, script}, args...), nil
}
