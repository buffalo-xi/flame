package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultPython  = "python"
	defaultTimeout = 30 * time.Second
	hostScriptRel  = "_host/pluginhost.py"

	// maxStdoutBytes only guards against runaway output: the effective limit on a result (12 KB) is
	// enforced by the runner.
	maxStdoutBytes = 8 << 20
	// maxStderrBytes: stderr is only used for logs, truncating is enough.
	maxStderrBytes = 4 << 10
)

// Host is the host of one plugin directory: it starts Python subprocesses to run list / invoke.
//
// It holds no state and caches no manifest — scanning and caching belong to Registry (see
// registry.go). Every call gets its own process: a crash is isolated by construction, and a timeout
// kills the whole process group (sysproc_*.go).
type Host struct {
	Dir     string        // plugin root ({root_dir}/plugins)
	Python  string        // interpreter command; empty = python
	Timeout time.Duration // per-call timeout; 0 = 30s
}

// NewHost builds a host. A timeout <= 0 means the default 30s.
func NewHost(dir, python string, timeout time.Duration) *Host {
	return &Host{Dir: dir, Python: python, Timeout: timeout}
}

// Script returns the host script path.
func (h *Host) Script() string {
	return filepath.Join(h.Dir, filepath.FromSlash(hostScriptRel))
}

func (h *Host) python() string {
	if p := strings.TrimSpace(h.Python); p != "" {
		return p
	}
	return defaultPython
}

func (h *Host) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return defaultTimeout
}

// List scans the directory and returns the tool manifest plus per-entry scan errors.
func (h *Host) List(ctx context.Context) (*ListResult, error) {
	out, stderr, err := h.run(ctx, nil, "list", "--dir", h.Dir)
	if err != nil {
		return nil, err
	}
	var res ListResult
	if err := json.Unmarshal(jsonLine(out), &res); err != nil {
		return nil, &CallError{
			Code:    CodeOther,
			Message: fmt.Sprintf("failed to parse the plugin manifest: %v%s", err, stderrSuffix(stderr)),
		}
	}
	return &res, nil
}

// Invoke runs one tool call and returns the plugin's result (untrimmed; trimming happens in the
// runner).
func (h *Host) Invoke(ctx context.Context, tool, impl string, payload *InvokePayload) (any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &CallError{Code: CodeConfig, Message: fmt.Sprintf("failed to serialise the call arguments: %v", err)}
	}
	out, stderr, err := h.run(ctx, body,
		"invoke", "--dir", h.Dir, "--tool", tool, "--impl", impl)
	if err != nil {
		// The process never started or was killed: append the tail of stderr, otherwise all that is
		// left is an exit status
		var ce *CallError
		if errors.As(err, &ce) {
			ce.Message += stderrSuffix(stderr)
			return nil, ce
		}
		return nil, err
	}

	var res InvokeResponse
	if err := json.Unmarshal(jsonLine(out), &res); err != nil {
		return nil, &CallError{
			Code:    CodeOther,
			Message: fmt.Sprintf("failed to parse the plugin response: %v%s", err, stderrSuffix(stderr)),
		}
	}
	if res.Error != nil {
		return nil, &CallError{Code: normalizeCode(res.Error.Code), Message: res.Error.Message}
	}
	if !res.OK {
		return nil, &CallError{Code: CodeOther, Message: "the plugin returned ok=false without an error message"}
	}
	return res.Result, nil
}

// run starts one host process. A nil stdin means no input is fed.
func (h *Host) run(ctx context.Context, stdin []byte, args ...string) ([]byte, string, error) {
	cctx, cancel := context.WithTimeout(ctx, h.timeout())
	defer cancel()

	full := append([]string{h.Script()}, args...)
	cmd := exec.CommandContext(cctx, h.python(), full...)
	cmd.Dir = h.Dir
	// The protocol is fixed to UTF-8: on Windows Python encodes and decodes pipes with the locale
	// (cp936) by default, so a non-ASCII query would be decoded into surrogates on stdin and blow up
	// when stdout is encoded.
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	// Process group: on timeout the whole group is killed, otherwise subprocesses python started
	// (pip, curl and the like) linger
	setProcAttr(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 5 * time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	outW := newLimitedWriter(maxStdoutBytes)
	errW := newLimitedWriter(maxStderrBytes)
	cmd.Stdout = outW
	cmd.Stderr = errW

	err := cmd.Run()
	stderr := strings.TrimSpace(errW.String())

	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, stderr, &CallError{
				Code:    CodeNetwork,
				Message: fmt.Sprintf("the plugin call timed out (%s)", h.timeout()),
			}
		}
		if errors.Is(cctx.Err(), context.Canceled) {
			return nil, stderr, &CallError{Code: CodeNetwork, Message: "the plugin call was cancelled"}
		}
		return nil, stderr, &CallError{
			Code:    CodeOther,
			Message: fmt.Sprintf("the plugin host failed: %v", err),
		}
	}
	return outW.Bytes(), stderr, nil
}

// jsonLine takes the last line of valid JSON on stdout.
//
// The host already guarantees stdout holds a single line, but a plugin may print before the
// redirection takes effect (module-level code), or the interpreter may emit a warning itself, so
// this has to tolerate both.
func jsonLine(out []byte) []byte {
	b := bytes.TrimSpace(out)
	if len(b) == 0 {
		return b
	}
	if b[0] == '{' && json.Valid(b) {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		ln := bytes.TrimSpace(lines[i])
		if len(ln) > 0 && ln[0] == '{' && json.Valid(ln) {
			return ln
		}
	}
	return b
}

func stderrSuffix(stderr string) string {
	if strings.TrimSpace(stderr) == "" {
		return ""
	}
	return " (stderr: " + truncate(stderr, 400) + ")"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// limitedWriter is a buffer with a byte ceiling: content beyond the limit is dropped while len(p) is
// still returned, so the child process does not fail on a short write (the same reasoning as the
// struct of that name in the tools package).
type limitedWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedWriter(limit int) *limitedWriter { return &limitedWriter{limit: limit} }

func (w *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.limit <= 0 { // <=0 means unlimited
		return w.buf.Write(p)
	}
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return n, nil
	}
	if n > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return n, nil
	}
	w.buf.Write(p)
	return n, nil
}

func (w *limitedWriter) Bytes() []byte  { return w.buf.Bytes() }
func (w *limitedWriter) String() string { return w.buf.String() }
