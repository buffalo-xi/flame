package daemon

import (
	"github.com/gin-gonic/gin"

	"flame/tools/sandbox"
	"flame/tools/xlog"
)

// handleInteractive is the long-interaction endpoint: one connection = one command feed, while the process belongs
// to the registry and outlives the connection.
//
// The differences from handleExec are only two:
//  1. the process comes from interactiveRegistry (lazy creation / reuse) rather than exec.Start;
//  2. a client disconnect does not kill the process — it only means "this call is over" and the process goes back
//     to the registry.
//
// Protocol (reusing the exec frames):
//
//	open(session_id + tool_id [+ argv/mounts on the first call]) → ready
//	→ feed commands over ChanStdin → client disconnects = this call is over (the process stays)
//	→ FrameKill = explicitly kill the process
//
// Neither stdout forwarding nor process-exit detection lives in this handler — they are in the resident goroutine
// started by spawn (stdout broadcast + Wait reaping). All that is left here is one stdin to feed, hence no io.Copy
// and none of the one-shot copyDone/clientGone forwarding logic.
func (s *Server) handleInteractive(c *gin.Context) {
	ws, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	wc := sandbox.NewConn(ws)
	defer func() { _ = ws.Close() }()

	// 1) open: session_id + tool_id, plus argv + mounts on the first call
	f, _, rerr := wc.Recv()
	if rerr != nil {
		return
	}
	if f.Type != sandbox.FrameOpen {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "expected open frame"})
		return
	}
	if f.SessionID == "" || f.ToolID == "" {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "session_id and tool_id are required"})
		return
	}

	var argv []string
	var mounts []sandbox.Mount
	var dir string
	var reqLimits sandbox.Limits
	if f.Spec != nil {
		spec := f.Spec.Spec()
		argv = spec.Argv
		mounts = spec.Mounts
		dir = spec.Dir
		reqLimits = spec.Limits
	}

	if gerr := guardMounts(mounts, s.cfg.AllowedMountRoots); gerr != nil {
		xlog.Error("sandboxd: interactive rejected mounts: %v", gerr)
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: gerr.Error()})
		return
	}

	// The channel carries the shell only. The browser is **not** here any more: it is 200-400 MB, carries login state
	// and has its own capacity limits, so it moved to the standalone browser pool (browserd) — this service decides
	// nothing about how a browser is launched.
	lim := s.cfg.ExecLimits(reqLimits)

	// 2) Lazily create / reuse the process (the process is locked, so this call has it exclusively)
	// f.Ephemeral: a temporary process with no session; the registry reclaims it through the short idle timeout.
	tp, aerr := s.interactive.acquire(f.SessionID, mounts, f.ToolID, argv, lim,
		acquireOpts{dir: dir, ephemeral: f.Ephemeral})
	if aerr != nil {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: aerr.Error()})
		return
	}
	defer tp.unlock() // release the lock when this call ends; the process is not killed

	// Both stdout and stderr follow the current connection; they are reset to discard when the call ends (so nothing
	// is written to a closed connection)
	tp.stderr.set(&wsWriter{c: wc, ch: sandbox.ChanStderr})
	tp.stdoutOut.set(&wsWriter{c: wc, ch: sandbox.ChanStdout})
	defer tp.stderr.set(nil)
	defer tp.stdoutOut.set(nil)

	if werr := wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameReady}); werr != nil {
		return
	}

	// 3) client → process stdin.
	//
	// Disconnecting means this call is over (the process stays); FrameKill explicitly kills it and removes it from
	// the registry.
	for {
		cf, data, rerr := wc.Recv()
		if rerr != nil {
			return // client disconnected, the process stays
		}
		if data != nil {
			if data[0] == sandbox.ChanStdin {
				if _, werr := tp.stdin.Write(data[1:]); werr != nil {
					return
				}
			}
			continue
		}
		switch cf.Type {
		case sandbox.FrameKill:
			// Kill first, then look at what is left: the "empty" notice is only sent once every tool of this session
			// is gone — the gateway unbinds its sticky binding on it. Killing one tool of a multi-tool session does
			// not invalidate it.
			//
			// The frame must go out before the connection closes (the ws is only closed by the defer); a peer that
			// does not know this frame ignores it, so older clients are unaffected.
			if empty := s.interactive.removeTool(f.SessionID, f.ToolID); empty {
				_ = wc.SendFrame(&sandbox.Frame{
					Type:      sandbox.FrameSessionEmpty,
					SessionID: f.SessionID,
				})
			}
			return
		}
	}
}
