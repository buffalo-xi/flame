package daemon

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// closeGrace is the longest wait for the worker to wind down after a close.
const closeGrace = 5 * time.Second

// errCodeTimeout is the error code for a session timeout, aligned with ErrCodeTimeout ("timeout") of tools/code
// on the nexus side. It deliberately does not import that client package (the dependency direction is the other
// way): this is a wire protocol value between the two sides, the same nature as unsupported_lang, so a change
// must be mirrored in tools/code/client.go.
//
// Why it gets a dedicated code: the nexus code executor dispatches on the code (service/flow/executors/code.go:81),
// and an empty code is normalized by orCode into script_error — so "timeout" would show up on the node as "script
// error", and on_error rules could not classify timeouts either.
const errCodeTimeout = "timeout"

// errCodeBusy is the error code for a rejected session (concurrency quota exhausted, slot wait timed out),
// aligned with ErrCodeBusy ("busy") of tools/code on the nexus side, same as above: a wire protocol value that
// must be changed together.
//
// Why it gets a dedicated code: it is a **retryable transient rejection** (the script never ran at all), and once
// folded into other / script a workflow can neither configure on_error.retry by code nor tell from the log whether
// "the pool was full" or "the script blew up".
const errCodeBusy = "busy"

// codeDeadline is the total time limit of one interaction.
//
// ⚠️ The semantics are "how long one interaction may live at most", **not** "how long it may sit idle before being
// reclaimed": the clock starts at the open frame and never resets, covering exec and every callback. The config key
// is code_max_sec (the old name session_idle_sec is still accepted, see applyLegacyKeys in config.go); this function
// is the authority on the semantics.
//
// Invariant: a timeout sent by the caller **can only tighten** it, never relax it — configuring nexus-side
// script.<lang>.timeout larger than this is the same as not configuring it (exactly the trap hit before:
// setting 600 and expecting a 10-minute script, which was cut off at 120s).
func codeDeadline(reqTimeout time.Duration, maxSec int) time.Duration {
	hardCap := time.Duration(maxSec) * time.Second
	if reqTimeout > 0 && reqTimeout < hardCap {
		return reqTimeout
	}
	return hardCap
}

// handleCode is the duplex endpoint: script plus callbacks.
//
// Convention: one connection executes the script exactly once (nexus opens one connection per Execute).
// That is exactly what makes "one execute completed" a decidable clean state, so the worker can safely go back to
// the pool.
func (s *Server) handleCode(c *gin.Context) {
	ws, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	wc := sandbox.NewConn(ws)
	defer func() { _ = ws.Close() }()

	// 1) open: duplex carries only lang (+ optional limits) — the script path and mounts are decided by this service
	f, _, rerr := wc.Recv()
	if rerr != nil {
		return
	}
	if f.Type != sandbox.FrameOpen || f.Lang == "" {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "expected open frame with lang"})
		return
	}

	pool, ok := s.pools[f.Lang]
	if !ok {
		_ = wc.SendFrame(&sandbox.Frame{
			Type:    sandbox.FrameError,
			ErrCode: "unsupported_lang",
			Message: "unsupported language: " + f.Lang,
		})
		return
	}

	var reqLimits sandbox.Limits
	if f.Spec != nil {
		reqLimits = f.Spec.Limits.Limits()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Total interaction limit (which is also the timeout budget of a single codehost execute): one connection
	// executes exactly one script, so the two semantics coincide. It is computed up front and sent to codehost with
	// the execute — so an endless script is killed in the interpreter layer instead of waiting for the whole
	// connection to hit the deadline.
	deadline := codeDeadline(reqLimits.Timeout, s.cfg.CodeMaxSec)

	// 2) Borrow a slot (concurrency gate plus pool reuse; on timeout it reports "busy" instead of waiting forever)
	w, reused, aerr := pool.Acquire(ctx)
	if aerr != nil {
		_ = wc.SendFrame(&sandbox.Frame{
			Type: sandbox.FrameError, ErrCode: errCodeBusy, Message: aerr.Error(),
		})
		return
	}

	var reusable atomic.Bool // true only when the session ended normally and the worker is clean
	var clean atomic.Bool    // the worker is in the "one execute completed" state

	// Interaction detail: the interpreter session identity only exists in this service — nexus knows the language
	// name but cannot tell "which codehost session this call landed on". script(w.ID) is the join key for
	// reconciling the two sides (returned in the ready frame, see Client.onReady in tools/code).
	//
	// Only one line at the end, and whether to log it is decided by cfg.CodeLog: lua is a µs-level high-frequency
	// call, and one line at each end would turn the log into noise (see ScriptLogXxx in config.go).
	started := time.Now()
	defer func() {
		// Fix the duration before returning: the return path may include waiting for a killed process, which should
		// not count towards the call duration.
		dur := time.Since(started).Round(time.Millisecond)
		pool.Release(w, reusable.Load())
		// Not reusable = this session did not wind down cleanly (interrupted / timed out / protocol error), so it is
		// logged unconditionally; everything else goes through the code_log policy (off / slow / all).
		if !reusable.Load() || s.cfg.CodeLogEnabled(dur) {
			xlog.Info("sandboxd: code %s done lang=%s reused=%v dur=%s reusable=%v",
				w.ID, f.Lang, reused, dur, reusable.Load())
		}
	}()

	if werr := wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameReady, ScriptID: w.ID, Reused: reused}); werr != nil {
		xlog.Info("sandboxd: code %s client gone before ready", w.ID)
		return
	}

	workerDone := make(chan struct{})
	clientDone := make(chan struct{})
	var workerOnce sync.Once
	stopWorker := func() { workerOnce.Do(func() { close(workerDone) }) }

	// 3a) worker → client (returns once this execute is over)
	go func() {
		defer stopWorker()
		for {
			msg, rerr := w.Recv()
			if rerr != nil {
				return
			}
			action, _ := msg["action"].(string)
			switch action {
			case "pong":
				continue

			case "done":
				clean.Store(true)
				_ = wc.SendFrame(&sandbox.Frame{
					Type:   sandbox.FrameDone,
					Result: msg["result"],
					Logs:   strOf(msg["logs"]),
				})
				return

			case "error":
				m, _ := msg["error"].(string)
				logs := strOf(msg["logs"])
				// A script error must be logged: the tail only records "script done" (without the outcome), so
				// without this line a blown-up script would leave nothing but a done line in the log, with no trace of
				// the failure. stderr carries the traceback and is recorded truncated to avoid flooding.
				if logs != "" {
					xlog.Error("sandboxd: code %s error lang=%s: %s\n%s",
						w.ID, f.Lang, m, truncateLog(logs, 4096))
				} else {
					xlog.Error("sandboxd: code %s error lang=%s: %s", w.ID, f.Lang, m)
				}
				_ = wc.SendFrame(&sandbox.Frame{
					Type:    sandbox.FrameError,
					Message: m,
					Logs:    logs,
				})
				return

			default:
				_ = wc.SendFrame(&sandbox.Frame{
					Type: sandbox.FrameError, Message: "unknown worker action: " + action,
				})
				return
			}
		}
	}()

	// 3b) client → worker (protocol translation: WS control frames ↔ worker stdio frames)
	go func() {
		defer close(clientDone)
		execSent := false
		for {
			cf, data, rerr := wc.Recv()
			if rerr != nil {
				return // client disconnected: the worker side is handled by the wind-down logic below
			}
			if data != nil {
				continue // duplex uses no binary frames
			}

			switch cf.Type {
			case sandbox.FrameExec:
				if execSent {
					_ = wc.SendFrame(&sandbox.Frame{
						Type: sandbox.FrameError, Message: "one execute per script",
					})
					return
				}
				execSent = true
				clean.Store(false)
				// lang / command / timeout_ms are what codehost (L3) needs: it forks the interpreter from them and
				// applies the per-execute timeout. The L2 worker script does not know these fields and ignores them,
				// using script / inputs / callbacks as before. Both sides share the same execute frame and each takes
				// what it needs.
				if serr := w.Send(map[string]any{
					"action":     "execute",
					"lang":       pool.lang,
					"command":    pool.cfg.Command,
					"script":     cf.Script,
					"inputs":     cf.Inputs,
					"timeout_ms": deadline.Milliseconds(),
				}); serr != nil {
					return
				}

			case sandbox.FrameKill:
				w.Kill()
				return

			case sandbox.FrameClose:
				// Normal completion: wait for the worker to wind down (it should already be done by now)
				select {
				case <-workerDone:
					reusable.Store(clean.Load())
				case <-time.After(closeGrace):
					xlog.Error("sandboxd: code %s close but worker not idle, discard", w.ID)
				}
				return
			}
		}
	}()

	// 4) Total interaction limit: the request timeout can only tighten it (see codeDeadline), preventing a stuck
	// connection from holding a worker indefinitely.
	select {
	case <-clientDone:
	case <-time.After(deadline):
		// The message names the limit and its source: this error travels all the way to the workflow node, and a vague
		// "session idle timeout" would read as "reclaimed while idle" instead of "the script ran too long", without
		// revealing what the limit is.
		_ = wc.SendFrame(&sandbox.Frame{
			Type:    sandbox.FrameError,
			ErrCode: errCodeTimeout,
			Message: fmt.Sprintf(
				"script execution timeout after %v (script cap: code_max_sec=%ds, a request timeout can only lower it)",
				deadline, s.cfg.CodeMaxSec),
		})
	}

	// 5) Make sure the worker side winds down (force-kill it if still alive — unknown state, never returned to the
	// pool)
	select {
	case <-workerDone:
	default:
		w.Kill()
		<-workerDone
	}
}

// strOf safely extracts an optional string field from a worker frame (missing / non-string both yield an empty
// string). Used for fields such as logs, which may be absent, so that a .(string) assertion on nil cannot panic.
func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// truncateLog truncates log text to max bytes, annotating the total size at the end when it was cut. Used so that
// script stderr (tracebacks / print output can be long) does not flood the log.
func truncateLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... (truncated, %d bytes total)", len(s))
}
