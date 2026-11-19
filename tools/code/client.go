// Package code is the "code snippet session" client: it delegates duplex execution to the remote
// sandbox service sandboxd over the /v1/code channel (renamed from /v1/script in 2026-10).
//
// The biggest difference from the old design: this side no longer owns a worker pool.
//   - whether a pool exists and which isolation is used is decided entirely by sandboxd;
//   - each Execute opens one session connection to sandboxd and closes it when done;
//   - a snippet is purely "input → return", with no callback interaction any more.
package code

import (
	"context"
	"fmt"
	"time"

	"flame/tools/sandbox"
)

// Error codes.
//
// ⚠️ timeout / busy / unsupported_lang are **wire protocol values**: sandboxd sends them back verbatim
// in the error frame (see the same-named constants in sandboxd/internal/daemon/handle_code.go); this
// package is only the receiving end. Changes must be made on both sides — editing one side leaves the
// node holding a code nobody recognises.
const (
	ErrCodeUnsupportedLang = "unsupported_lang" // language not supported
	ErrCodeConnectFail     = "connect_fail"     // cannot connect to sandboxd
	ErrCodeSendFail        = "send_fail"        // cannot send the request
	ErrCodeTimeout         = "timeout"          // execution timed out
	ErrCodeBusy            = "busy"             // sandbox busy: concurrency quota exhausted, slot wait rejected (the script never ran)
	ErrCodeWorkerComm      = "worker_comm"      // worker communication error
	ErrCodeScriptError     = "script_error"     // script execution error
	ErrCodeUnknownAction   = "unknown_action"   // unknown action
	ErrCodeCancel          = "cancel"           // context cancelled / timed out
)

// defaultTimeout is used when the caller passes no timeout.
const defaultTimeout = 30 * time.Second

// Error is an error returned by the /v1/code channel, carrying the code and the original error.
//
// It is no longer called ScriptError: it identifies the **channel** (code), not the snippet being run;
// a snippet failing is merely one of its codes (ErrCodeScriptError).
type Error struct {
	Code    string // error code, one of the ErrCodeXxx constants above
	Message string // error message
	Logs    string // full stderr on failure (including traceback / stack)
	Err     error  // original error (optional, for errors.Unwrap)
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Err }

// NewError builds an Error.
func NewError(code, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

// Result is the output of one script execution: the returned value plus the print output (logs).
type Result struct {
	Value any    // value returned by the script (formerly result)
	Logs  string // full print output (may be truncated)
}

// Client is the script session client.
//
// Stateless and safe to reuse concurrently: every Execute opens its own session connection, so nested
// calls (running a script inside a binding transform, say) do not block themselves.
type Client struct {
	endpoint string         // of the form ws://sandboxd:8080/v1/code
	token    string         // optional Bearer token
	limits   sandbox.Limits // default resource limits (apart from timeout, sandboxd usually decides)

	// onReady is an optional interaction observer: fired once sandboxd reports ready, carrying the
	// script ID it assigned (the sandbox-side worker ID) and whether it reused a pooled object.
	//
	// Why it matters: the container and interpreter identity exist only on the sandboxd side, and this
	// script ID is the only clue available here — without catching it the two logs cannot be reconciled
	// (sandboxd records the container, this side records the business context, and the script ties them
	// together).
	// Contract: set once at startup and read-only afterwards, hence no lock; do not change it while
	// Execute calls are in flight.
	onReady func(scriptID string, reused bool)
}

// NewClient creates a script session client.
func NewClient(endpoint, token string, limits sandbox.Limits) *Client {
	return &Client{endpoint: endpoint, token: token, limits: limits}
}

// SetOnReady installs the "interaction ready" observer (optional; nothing is recorded by default).
// It must be called once before any Execute (see Client.onReady for the concurrency contract).
func (c *Client) SetOnReady(fn func(scriptID string, reused bool)) { c.onReady = fn }

// Close is kept so existing defer calls still compile (the client has no pool and nothing to release).
func (c *Client) Close() {}

// Execute runs one script.
//
//	ctx     - context, for cancellation
//	lang    - language: "lua" / "python" / "js"
//	script  - script body
//	inputs  - input variables
//	timeout - timeout in seconds; <=0 uses defaultTimeout
func (c *Client) Execute(ctx context.Context, lang, script string, inputs map[string]any,
	timeout int) (*Result, error) {

	execTimeout := time.Duration(timeout) * time.Second
	if execTimeout <= 0 {
		execTimeout = defaultTimeout
	}

	conn, err := sandbox.Dial(ctx, c.endpoint, c.token)
	if err != nil {
		return nil, NewError(ErrCodeConnectFail,
			fmt.Sprintf("connect sandboxd: %v", err), err)
	}
	defer func() { _ = conn.Close() }()

	// Resource limits: only the timeout is sent (everything else is decided by sandboxd's per-purpose
	// defaults and clamped there).
	lim := c.limits
	lim.Timeout = execTimeout

	// 1) open: the duplex channel carries lang + limits only — the worker script path and mounts are
	// sandboxd's decision.
	if err := conn.SendFrame(&sandbox.Frame{
		Type: sandbox.FrameOpen,
		Lang: lang,
		Spec: &sandbox.WireSpec{Limits: lim.Wire()},
	}); err != nil {
		return nil, NewError(ErrCodeSendFail, fmt.Sprintf("send open: %v", err), err)
	}

	// 2) Wait for ready.
	rf, _, rerr := conn.Recv()
	if rerr != nil {
		return nil, NewError(ErrCodeWorkerComm, fmt.Sprintf("wait ready: %v", rerr), rerr)
	}
	switch rf.Type {
	case sandbox.FrameReady:
		// Catch the script ID: it is the only key for reconciling with sandboxd's logs — sandboxd knows
		// which container this call landed in, this side does not, and it sends the ID back.
		if c.onReady != nil {
			c.onReady(rf.ScriptID, rf.Reused)
		}
	case sandbox.FrameError:
		return nil, NewError(orCode(rf.ErrCode), rf.Message, nil)
	default:
		return nil, NewError(ErrCodeWorkerComm,
			fmt.Sprintf("unexpected frame %q (want ready)", rf.Type), nil)
	}

	// 3) exec
	if err := conn.SendFrame(&sandbox.Frame{
		Type:   sandbox.FrameExec,
		Script: script,
		Inputs: inputs,
	}); err != nil {
		return nil, NewError(ErrCodeSendFail, fmt.Sprintf("send execute: %v", err), err)
	}

	// 4) Result loop (waiting for done / error / exit).
	deadline := time.NewTimer(execTimeout)
	defer deadline.Stop()

	type recvResult struct {
		f    *sandbox.Frame
		data []byte
		err  error
	}
	recvCh := make(chan recvResult, 1)

	abort := func(code, msg string, cause error) (*Result, error) {
		// Kill actively: the remote side destroys the worker (it does not go back to the pool), so a
		// half-finished state is never reused.
		_ = conn.SendFrame(&sandbox.Frame{Type: sandbox.FrameKill})
		return nil, NewError(code, msg, cause)
	}

	for {
		go func() {
			f, data, err := conn.Recv()
			recvCh <- recvResult{f: f, data: data, err: err}
		}()

		var r recvResult
		select {
		case r = <-recvCh:
		case <-deadline.C:
			return abort(ErrCodeTimeout,
				fmt.Sprintf("execution timeout after %v", execTimeout), nil)
		case <-ctx.Done():
			return abort(ErrCodeCancel,
				fmt.Sprintf("execution canceled: %v", ctx.Err()), ctx.Err())
		}

		if r.err != nil {
			return nil, NewError(ErrCodeWorkerComm,
				fmt.Sprintf("worker communication: %v", r.err), r.err)
		}
		if r.f == nil {
			continue // the duplex channel does not use binary frames
		}

		switch r.f.Type {
		case sandbox.FrameDone:
			// Normal completion → tell the sandbox service this worker can be reclaimed.
			_ = conn.SendFrame(&sandbox.Frame{Type: sandbox.FrameClose})
			return &Result{Value: r.f.Result, Logs: r.f.Logs}, nil

		case sandbox.FrameError:
			se := NewError(orCode(r.f.ErrCode), r.f.Message, nil)
			se.Logs = r.f.Logs
			return nil, se

		case sandbox.FrameExit:
			if r.f.Code != 0 {
				return nil, NewError(ErrCodeScriptError,
					fmt.Sprintf("worker exited with code %d", r.f.Code), nil)
			}
			return nil, NewError(ErrCodeWorkerComm, "worker exited unexpectedly", nil)

		default:
			// Unknown frame: ignore it (forward compatibility).
		}
	}
}

// orCode falls back to the generic script error when the code is empty.
func orCode(code string) string {
	if code == "" {
		return ErrCodeScriptError
	}
	return code
}
