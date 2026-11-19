package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// interactiveIdle is how long without new output marks the command as finished.
//
// This is the sentinel's backstop: normally the command echoes a unique sentinel at the end and reading
// it means we are done; but a script may swallow the sentinel line (redirection, exec replacement), and
// the idle timeout then closes the call instead of hanging forever.
const interactiveIdle = 30 * time.Second

// InteractiveSession is the session identity of one long-interactive call.
//
// Key is the reuse identifier that runs through all three ends: nexus uses it to locate the process,
// sandboxg for sticky routing, and sandboxd to look up the sessionSandbox. It is SessionID when there is
// a session and RunID when there is not.
type InteractiveSession struct {
	Key       string
	Ephemeral bool // a temporary process with no session: sandboxd applies its short timeout
}

// InteractiveResult is the outcome of one interaction.
type InteractiveResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Completed bool // false = the sentinel was never seen (returned on idle timeout; the command may still be running)
}

// InteractiveClient is the long-interactive process client (/v1/interactive).
//
// Each call opens a short-lived WS: open → feed the command (with a sentinel) → read output up to the
// sentinel or the idle timeout → disconnect. The process lifetime belongs to sandboxd (a disconnect does
// not kill it; it is reused by session_id), and this side holds no state.
type InteractiveClient struct {
	endpoint string
	token    string
	limits   Limits
}

// NewInteractive creates a long-interactive client. endpoint looks like ws://sandboxd:8080/v1/interactive.
func NewInteractive(endpoint, token string, limits Limits) *InteractiveClient {
	return &InteractiveClient{endpoint: endpoint, token: token, limits: limits}
}

// Exec runs one command in the session.
//
// argv only takes effect when the process is first created (sandboxd ignores it when reusing an existing
// process); command is fed through stdin.
func (c *InteractiveClient) Exec(ctx context.Context, sess InteractiveSession, toolID string,
	argv []string, command string, mounts []Mount, dir string) (InteractiveResult, error) {

	if len(argv) == 0 {
		return InteractiveResult{}, fmt.Errorf("sandbox: interactive requires argv")
	}

	conn, err := Dial(ctx, c.endpoint, c.token)
	if err != nil {
		return InteractiveResult{}, err
	}
	defer conn.Close()

	if err := conn.SendFrame(&Frame{
		Type:      FrameOpen,
		SessionID: sess.Key,
		ToolID:    toolID,
		Ephemeral: sess.Ephemeral,
		Spec:      (&Spec{Argv: argv, Dir: dir, Mounts: mounts, Limits: c.limits}).Wire(),
	}); err != nil {
		return InteractiveResult{}, err
	}

	f, _, err := conn.Recv()
	if err != nil {
		return InteractiveResult{}, fmt.Errorf("sandbox: interactive ready: %w", err)
	}
	switch f.Type {
	case FrameReady:
	case FrameError:
		return InteractiveResult{}, fmt.Errorf("sandbox: interactive: %s", f.Message)
	default:
		return InteractiveResult{}, fmt.Errorf("sandbox: unexpected frame %q (want ready)", f.Type)
	}

	sentinel := "__SH_DONE_" + randHex(8) + "__"
	if err := conn.SendData(ChanStdin, []byte(wrapWithSentinel(command, sentinel, argv[0])+"\n")); err != nil {
		return InteractiveResult{}, err
	}

	return readToSentinel(ctx, conn, sentinel, interactiveIdle)
}

// Do sends one line of JSON to a long-interactive process and reads one line of JSON back.
//
// The only difference from Exec is the completion condition: a shell needs a sentinel to know a command
// finished, while a browser host is strictly request/response — send a line, receive a line, no sentinel
// needed.
//
// Matching by id is necessary fault tolerance: if a previous call timed out, its response may arrive
// during the next one; taking it as this call's result would hand the model a completely misaligned page
// state. Conversely, because late responses can be discarded by id, a timeout **does not require killing
// the browser** (cookies and tabs survive).
func (c *InteractiveClient) Do(ctx context.Context, sess InteractiveSession, toolID string,
	argv []string, req []byte, mounts []Mount, dir string) ([]byte, error) {

	if len(argv) == 0 {
		return nil, fmt.Errorf("sandbox: interactive requires argv")
	}
	conn, err := Dial(ctx, c.endpoint, c.token)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.SendFrame(&Frame{
		Type:      FrameOpen,
		SessionID: sess.Key,
		ToolID:    toolID,
		Ephemeral: sess.Ephemeral,
		Spec:      (&Spec{Argv: argv, Dir: dir, Mounts: mounts, Limits: c.limits}).Wire(),
	}); err != nil {
		return nil, err
	}

	f, _, err := conn.Recv()
	if err != nil {
		return nil, fmt.Errorf("sandbox: interactive ready: %w", err)
	}
	switch f.Type {
	case FrameReady:
	case FrameError:
		return nil, fmt.Errorf("sandbox: %s", f.Message)
	default:
		return nil, fmt.Errorf("sandbox: unexpected frame %q (want ready)", f.Type)
	}

	var wantID int
	var probe struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(req, &probe); err == nil {
		wantID = probe.ID
	}

	if err := conn.SendData(ChanStdin, append(append([]byte{}, req...), '\n')); err != nil {
		return nil, err
	}
	return readJSONLine(ctx, conn, wantID)
}

// readJSONLine reads one line of JSON from the connection, matching by id.
//
// The buffer is capped: a browser host's response already has a max_bytes budget, so exceeding it means
// the protocol is broken, and keeping on accumulating would eat nexus's memory.
func readJSONLine(ctx context.Context, conn *Conn, wantID int) ([]byte, error) {
	const maxLine = 8 << 20

	type outcome struct {
		line []byte
		err  error
	}
	done := make(chan outcome, 1)

	go func() {
		var buf []byte
		for {
			f, data, err := conn.Recv()
			if err != nil {
				done <- outcome{err: err}
				return
			}
			if data != nil {
				if data[0] != ChanStdout {
					continue // stderr takes no part in the protocol
				}
				buf = append(buf, data[1:]...)
				for {
					i := bytes.IndexByte(buf, '\n')
					if i < 0 {
						break
					}
					line := bytes.TrimSpace(buf[:i])
					buf = buf[i+1:]
					if len(line) == 0 {
						continue
					}
					if wantID > 0 {
						var env struct {
							ID int `json:"id"`
						}
						if json.Unmarshal(line, &env) == nil && env.ID != wantID {
							continue // a late response from an earlier call: drop it
						}
					}
					done <- outcome{line: append([]byte{}, line...)}
					return
				}
				if len(buf) > maxLine {
					done <- outcome{err: fmt.Errorf("sandbox: response line exceeds %d bytes", maxLine)}
					return
				}
				continue
			}
			switch f.Type {
			case FrameError:
				done <- outcome{err: fmt.Errorf("sandbox: %s", f.Message)}
				return
			}
		}
	}()

	select {
	case r := <-done:
		return r.line, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Kill explicitly destroys a session process (the fast path at run end, or a tool's kill parameter).
//
// It is idempotent: when the process does not exist nothing is created (open carries no Spec, and an
// empty argv makes spawn fail immediately); when it exists, removeTool kills it.
func (c *InteractiveClient) Kill(ctx context.Context, sess InteractiveSession, toolID string) error {
	conn, err := Dial(ctx, c.endpoint, c.token)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.SendFrame(&Frame{
		Type:      FrameOpen,
		SessionID: sess.Key,
		ToolID:    toolID,
		Ephemeral: sess.Ephemeral,
	}); err != nil {
		return err
	}

	// Wait for ready: the server reports ready only when the process exists; otherwise it returns an
	// error — idempotent, so treat that as already destroyed.
	f, _, err := conn.Recv()
	if err != nil {
		return nil // the connection is gone, which means it is already destroyed
	}
	if f.Type != FrameReady {
		return nil
	}
	if err := conn.SendFrame(&Frame{Type: FrameKill}); err != nil {
		return err
	}
	// Wait for the server to removeTool and close the connection (an error or a close means it is done).
	_, _, _ = conn.Recv()
	return nil
}

// wrapWithSentinel wraps a command as "run + echo a unique sentinel + exit code", choosing the separator
// by shell type.
func wrapWithSentinel(command, sentinel, shellBin string) string {
	base := strings.ToLower(filepath.Base(shellBin))
	switch {
	case base == "cmd" || base == "cmd.exe":
		// Split across two physical lines: cmd expands %errorlevel% while parsing the whole line, so
		// writing it on one line yields the exit code of the *previous* command — the symptom being that
		// every command reports 0. Feeding it as two lines means the second is parsed only after the first
		// has run, so %errorlevel% is this command's.
		return command + "\necho " + sentinel + "%errorlevel%"
	case strings.HasPrefix(base, "powershell") || base == "pwsh":
		return command + "; echo \"" + sentinel + "$LASTEXITCODE\""
	default: // bash / sh / zsh ...
		return command + "; echo " + sentinel + "$?"
	}
}

// readToSentinel reads stdout until the sentinel appears; with no new output for idle it returns anyway.
//
// Everything before the sentinel is command output; the exit code follows the sentinel. stderr is
// collected separately and takes no part in the decision.
func readToSentinel(ctx context.Context, conn *Conn, sentinel string, idle time.Duration) (InteractiveResult, error) {
	var (
		mu     sync.Mutex
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	found := make(chan sentinelHit, 1) // sentinel hit: exit code + position
	readErr := make(chan error, 1)
	activity := make(chan struct{}, 1) // there was output (resets the idle timer)

	go func() {
		for {
			f, data, err := conn.Recv()
			if err != nil {
				readErr <- err
				return
			}
			if data != nil {
				switch data[0] {
				case ChanStdout:
					mu.Lock()
					stdout.Write(data[1:])
					hit := parseSentinel(stdout.Bytes(), sentinel)
					mu.Unlock()
					if hit.ok {
						found <- hit
						return
					}
				case ChanStderr:
					mu.Lock()
					stderr.Write(data[1:])
					mu.Unlock()
				}
				select {
				case activity <- struct{}{}:
				default:
				}
				continue
			}
			switch f.Type {
			case FrameExit:
				// The process exited (a command like exit): finish with what has been read.
				mu.Lock()
				hit := parseSentinel(stdout.Bytes(), sentinel)
				mu.Unlock()
				if !hit.ok {
					hit = sentinelHit{code: f.Code}
				}
				found <- hit
				return
			case FrameError:
				readErr <- fmt.Errorf("sandbox: interactive: %s", f.Message)
				return
			}
		}
	}()

	timer := time.NewTimer(idle)
	defer timer.Stop()

	snapshot := func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		return stdout.String(), stderr.String()
	}

	for {
		select {
		case hit := <-found:
			out, errOut := snapshot()
			if hit.ok {
				out = stripSentinelAt(out, hit.at)
			}
			return InteractiveResult{
				Stdout:    out,
				Stderr:    errOut,
				ExitCode:  hit.code,
				Completed: true,
			}, nil
		case err := <-readErr:
			out, errOut := snapshot()
			return InteractiveResult{Stdout: out, Stderr: errOut, ExitCode: -1}, err
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		case <-timer.C:
			// No sentinel parsed: return the output as is (trying to "delete the sentinel line" here
			// would most likely delete the command line cmd echoed, taking the real output with it).
			out, errOut := snapshot()
			return InteractiveResult{
				Stdout:    out,
				Stderr:    errOut,
				ExitCode:  -1,
				Completed: false,
			}, nil
		case <-ctx.Done():
			out, errOut := snapshot()
			return InteractiveResult{Stdout: out, Stderr: errOut, ExitCode: -1}, ctx.Err()
		}
	}
}

// sentinelHit is one sentinel match: exit code + the byte position of the sentinel in the output (which
// the line removal must reuse).
type sentinelHit struct {
	code int
	at   int
	ok   bool
}

// parseSentinel finds the sentinel in the output and parses the exit code that follows it.
// When the sentinel has appeared but the exit code has not arrived yet (splitting), it returns ok=false
// and the caller keeps accumulating.
//
// Why not simply take the first occurrence: cmd echoes the whole fed line back (echo is on by default
// under a pipe), so the first sentinel in the output comes from that echo — what follows it is an
// unexpanded %errorlevel%, not a number. Taking only the first occurrence makes every shell call on
// Windows wait for the idle timeout (Completed=false, exit=-1), looking like "the command hung".
// PowerShell/bash do not echo, and this costs them nothing.
//
// The criterion is therefore "the occurrence followed by a parseable integer": skip the echo, hit the
// real output.
func parseSentinel(b []byte, sentinel string) sentinelHit {
	needle := []byte(sentinel)
	for off := 0; off <= len(b); {
		idx := bytes.Index(b[off:], needle)
		if idx < 0 {
			return sentinelHit{}
		}
		idx += off
		rest := b[idx+len(needle):]
		if end := bytes.IndexByte(rest, '\n'); end >= 0 {
			rest = rest[:end]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(rest))); err == nil {
			return sentinelHit{code: n, at: idx, ok: true}
		}
		off = idx + len(needle)
	}
	return sentinelHit{}
}

// stripSentinelAt removes the whole line containing position at (the sentinel and the exit code after it)
// and keeps the rest of the output. at must come from parseSentinel — a line may start with a prompt or
// an echo, and searching again would remove the wrong line.
func stripSentinelAt(s string, at int) string {
	if at < 0 || at > len(s) {
		return s
	}
	lineStart := strings.LastIndexByte(s[:at], '\n') + 1
	lineEnd := strings.IndexByte(s[at:], '\n')
	if lineEnd < 0 {
		return s[:lineStart]
	}
	return s[:lineStart] + s[at+lineEnd+1:]
}

// randHex returns the hex string of n random bytes (to make a sentinel unique).
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
