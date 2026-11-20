// Package codehost implements sandboxd's script execution host: a language-agnostic resident process that runs inside
// the isolation unit (nsjail / none) and forks a fresh interpreter process on behalf of sandboxd for every script.
//
// Differences from the old worker.lua/py/js:
//   - one body of Go code forks three interpreters (lua / python / node) instead of three per-language workers;
//   - every execute forks a brand-new interpreter, so it is clean at the process level and has no cross-request state
//     left over by construction;
//   - the protocol degenerates into a one-way execute / done / error exchange (the callback / callback_resp
//     interaction is gone), script print / log always goes to stderr, and stdout is reserved for the structured
//     result.
package codehost

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// maxMsgSize is the cap on a single protocol frame, kept in sync with daemon/protocol.go.
	maxMsgSize = 10 * 1024 * 1024
	// maxStderrCapture is the tail cap of the interpreter stderr kept for returning logs (forwarding is not limited
	// by it). It is also the source of the reply's error fallback (lastNonEmptyLine).
	maxStderrCapture = 64 * 1024
	// killWaitDelay caps the wait for the I/O pipes to settle after killing the process: the interpreter's stderr may
	// be held by an escaped grandchild process, so Wait waits at most this long instead of hanging forever.
	killWaitDelay = 5 * time.Second
)

// request is one frame from host → codehost.
type request struct {
	Action    string          `json:"action"`
	Lang      string          `json:"lang"`
	Command   string          `json:"command"`
	Script    string          `json:"script"`
	Inputs    json.RawMessage `json:"inputs"`
	TimeoutMS int64           `json:"timeout_ms"`
}

// Host is one codehost instance: it owns stdin/stdout (the protocol channel) and stderr (the forwarding destination for
// the interpreter's stderr). All three are injected, which makes testing easy.
//
// Note where stderr goes: the sandboxd side wires it to io.Discard (see the note on stderrCollector), so a script's
// print output reaches nexus through the logs field of the protocol frame, not through this path.
type Host struct {
	in     io.Reader
	out    io.Writer
	stderr io.Writer
}

// Run runs the codehost main loop with the default host environment.
func Run(in io.Reader, out, errW io.Writer) error {
	return (&Host{in: in, out: out, stderr: errW}).run()
}

// run is the main loop: read one frame, execute once, reply with one frame. Reading EOF on stdin (sandboxd closed the
// connection) exits.
//
// The only frames in the protocol are execute (in) and done / error (out), plus two control frames:
//   - ping / pong — a liveness probe for sandboxd's pool to use on lend / return / sweeps. It is not the two-way
//     interaction of the old callbacks, it only confirms the host process is still alive.
//   - shutdown — a graceful wrap-up.
func (h *Host) run() error {
	for {
		req, err := h.readRequest()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		switch req.Action {
		case "execute":
			h.execute(req)
		case "ping":
			_ = h.writeJSON(map[string]string{"action": "pong"})
		case "shutdown":
			return nil
		default:
			h.sendError("unknown action: "+req.Action, "", false)
		}
	}
}

// execute runs once: select the template by lang → weave the complete script → write it to disk → fork the interpreter
// → collect the result (stdout) and the print output (stderr forwarding) → reply done / error.
//
// Zombie reaping: the interpreter is the only child that is Waited, and it is Waited exactly once whether it exits
// normally or is killed on timeout, so no zombies accumulate. killTree additionally covers the descendants it forked
// (see proc_*.go).
func (h *Host) execute(req *request) {
	if req.Command == "" {
		h.sendError("execute: empty command", "", false)
		return
	}
	tmpl, ok := templates[req.Lang]
	if !ok {
		h.sendError("unsupported language: "+req.Lang, "", false)
		return
	}

	src := tmpl.build(req.Script, req.Inputs)

	// The temporary script file is written to cwd (".") rather than os.TempDir(): sandboxd sets codehost's cwd to the
	// writable directory exclusive to this session, so the script and its temporary artefacts land there and are
	// cleaned up with the session instead of leaking into the host /tmp.
	f, err := os.CreateTemp(".", "codehost-*"+tmpl.ext)
	if err != nil {
		h.sendError("codehost: create temp script: "+err.Error(), "", false)
		return
	}
	fname := f.Name()
	_, werr := f.WriteString(src)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(fname)
		if werr != nil {
			h.sendError("codehost: write temp script: "+werr.Error(), "", false)
		} else {
			h.sendError("codehost: close temp script: "+cerr.Error(), "", false)
		}
		return
	}
	defer os.Remove(fname)

	// Timeout: 0 / unset = no limit (standalone debugging); once wired in, sandboxd sends it on every call.
	ctx := context.Background()
	cancel := context.CancelFunc(nil)
	if req.TimeoutMS > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, req.Command, fname)
	cmd.Env = interpreterEnv()

	var stdout cappedBuffer
	stdout.max = maxMsgSize
	cmd.Stdout = &stdout

	col := &stderrCollector{dst: h.stderr, max: maxStderrCapture}
	cmd.Stderr = col

	setProcessGroup(cmd)
	// The memory cap is applied uniformly by the isolation layer (nsjail's cgroup v2 memory.max) and the interpreter
	// no longer sets RLIMIT_AS on its own — a VSZ cap would kill runtimes like node/V8 that reserve large amounts of
	// virtual address space. What matters: on ctx timeout / cancellation it goes through killTree (the whole group)
	// rather than the default single-pid kill — a script may fork descendants, and killing only the interpreter would
	// leave escaped processes behind.
	cmd.Cancel = func() error { return killTree(cmd) }
	cmd.WaitDelay = killWaitDelay

	if err := cmd.Start(); err != nil {
		h.sendError("codehost: start "+req.Command+": "+err.Error(), "", false)
		return
	}

	waitErr := cmd.Wait()

	// The timeout takes precedence over every other judgement: ctx fired with DeadlineExceeded, so the script
	// certainly did not finish. The timeout still carries logs: the script may already have printed something, and
	// bringing it back helps troubleshooting.
	if ctx.Err() == context.DeadlineExceeded {
		h.sendError(fmt.Sprintf("script execution timed out after %dms", req.TimeoutMS), col.String(), col.truncated)
		return
	}

	h.reply(stdout.Bytes(), waitErr, req.Command, col)
}

// interpreterEnv is the environment of the forked interpreter.
//
// The protocol is UTF-8 end to end: the script source arrives as UTF-8, and its print / log output travels back inside a
// JSON frame. Python's stdio follows the locale by default - cp936 on a Chinese Windows - so a script's print() would emit
// GBK and turn into U+FFFD where that frame is encoded, which is exactly the mojibake a user sees in a code node's logs.
// The other two interpreters write UTF-8 bytes regardless and ignore both variables; under nsjail / docker the locale is
// UTF-8 already, so setting them there is harmless.
func interpreterEnv() []string {
	return append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
}

// reply parses the interpreter's stdout ({"result": X} / {"error": msg} / {}) and replies with a frame.
//
// The stdout fallback: when no JSON can be parsed, an error is assembled from the exit code plus the stderr tail,
// covering the paths the template cannot catch — "a syntax error makes the whole file fail to parse", "the
// interpreter crashed" and the like.
func (h *Host) reply(stdout []byte, waitErr error, command string, col *stderrCollector) {
	logs := col.String()
	var interp map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &interp); err == nil {
		if r, ok := interp["result"]; ok {
			h.sendDone(r, logs, col.truncated)
			return
		}
		if e, ok := interp["error"]; ok {
			var msg string
			_ = json.Unmarshal(e, &msg)
			if msg == "" {
				msg = string(e)
			}
			h.sendError(msg, logs, col.truncated)
			return
		}
		// Neither result nor error (the "{}" the templates emit): treat it as done without a return value.
		h.sendDone(nil, logs, col.truncated)
		return
	}

	if waitErr == nil {
		// A clean exit with no structured result: an anomaly such as a template bug, so report it honestly.
		h.sendError("codehost: "+command+" produced no parseable result", logs, col.truncated)
		return
	}
	msg := lastNonEmptyLine(logs)
	if msg == "" {
		msg = fmt.Sprintf("codehost: %s exited with error", command)
	}
	h.sendError(msg, logs, col.truncated)
}

// ---------- Protocol frames ----------

func (h *Host) readRequest() (*request, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(h.in, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxMsgSize {
		return nil, fmt.Errorf("codehost: message too large: %d bytes", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(h.in, body); err != nil {
		return nil, err
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("codehost: decode request: %w", err)
	}
	return &req, nil
}

func (h *Host) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(data)))
	if _, err := h.out.Write(header); err != nil {
		return err
	}
	_, err = h.out.Write(data)
	return err
}

func (h *Host) sendDone(result json.RawMessage, logs string, trunc bool) {
	_ = h.writeJSON(doneMsg{Action: "done", Result: result, Logs: logs, Trunc: trunc})
}

func (h *Host) sendError(msg, logs string, trunc bool) {
	_ = h.writeJSON(errorMsg{Action: "error", Error: msg, Logs: logs, Trunc: trunc})
}

type doneMsg struct {
	Action string          `json:"action"`
	Result json.RawMessage `json:"result,omitempty"`
	Logs   string          `json:"logs,omitempty"`           // the full script stderr (print output, possibly truncated)
	Trunc  bool            `json:"logs_truncated,omitempty"` // whether logs were truncated by the cap
}

type errorMsg struct {
	Action string `json:"action"`
	Error  string `json:"error"`
	Logs   string `json:"logs,omitempty"`           // the full stderr on error (including a traceback / stack)
	Trunc  bool   `json:"logs_truncated,omitempty"` // whether logs were truncated by the cap
}

// ---------- Output collection ----------

// cappedBuffer collects the interpreter's stdout up to a cap. Beyond the cap the write length is still reported (so
// os/exec's copying goroutine does not error) but nothing more is stored — the result goes into a protocol frame,
// which already has a 10MB cap.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.max > 0 {
		remain := b.max - b.buf.Len()
		if remain > 0 {
			if len(p) > remain {
				b.buf.Write(p[:remain])
			} else {
				b.buf.Write(p)
			}
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// stderrCollector forwards the interpreter's stderr to codehost's own stderr while keeping the last max bytes for
// returning as logs, setting truncated when the cap is exceeded.
//
// The forwarding destination needs to be clear: when sandboxd starts codehost it wires its stderr to io.Discard (see
// spawnCodehost in daemon/worker.go), so this output does **not** reach the service log. What actually returns a
// script's print output to nexus is the tail below (the logs field of the protocol frame, the last 64KB); dst is kept
// so that codehost's unit tests and standalone debugging can see the output directly.
//
// Why the tail rather than the head: error tracebacks / the most recent prints are at the tail, and the logs returned
// to nexus must answer "what happened last"; lastNonEmptyLine also needs the tail to find the real last line.
type stderrCollector struct {
	dst       io.Writer
	max       int
	tail      []byte
	truncated bool
}

func (c *stderrCollector) Write(p []byte) (int, error) {
	if c.dst != nil {
		_, _ = c.dst.Write(p)
	}
	if c.max > 0 && len(p) > 0 {
		c.tail = append(c.tail, p...)
		if len(c.tail) > c.max {
			c.tail = c.tail[len(c.tail)-c.max:]
			c.truncated = true
		}
	}
	return len(p), nil
}

func (c *stderrCollector) String() string { return string(c.tail) }

func lastNonEmptyLine(s string) string {
	s = strings.TrimRight(s, "\r\n")
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
