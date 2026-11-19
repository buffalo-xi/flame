package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ============================================================================
// Wire protocol with sandboxd (shared by both ends)
//
//   text frame     = JSON control frame (Frame)
//   binary frame   = [1-byte channel tag][raw bytes]
//
// stdout/stderr/stdin travel in binary frames, avoiding the 33% bloat and extra allocations of base64.
// ============================================================================

// Binary frame channel tags (first byte).
const (
	ChanStdout byte = 1
	ChanStderr byte = 2
	ChanStdin  byte = 3
)

// Control frame types.
const (
	FrameOpen     = "open"      // client → server: start (one-shot may carry a Spec, including an optional Lang; duplex carries Lang only)
	FrameReady    = "ready"     // server → client: ready
	FrameExec     = "exec"      // client → server: execute the script (duplex)
	FrameDone     = "done"      // server → client: script finished
	FrameStdinEOF = "stdin_eof" // client → server: end of input (half close)
	FrameExit     = "exit"      // server → client: process exited
	FrameKill     = "kill"      // client → server: force kill
	FrameClose    = "close"     // client → server: normal end (the worker may go back to the pool)
	FrameError    = "error"     // server → client: error

	// FrameSessionEmpty is server → client: this session has no long-running interactive tool left.
	//
	// It is only sent on the kill path (that connection is still alive). A session may own several
	// long-running tools at once, held in the registry as a tools map — killing one does not invalidate
	// the session, so the test is "how many are left afterwards", and the frame goes out only when none
	// remain. A process exiting on its own or being reclaimed by the idle janitor has no connection
	// context, and those two paths still rely on the TTL for the sticky binding.
	FrameSessionEmpty = "session_empty"
)

// maxFrameBytes caps a single message (headroom for large output).
const maxFrameBytes = 32 << 20 // 32 MiB

// WireLimits is the serialisable form of Limits (Timeout in seconds, which is easier to inspect by hand).
type WireLimits struct {
	MemoryMB       int     `json:"memory_mb,omitempty"`
	CPUCount       int     `json:"cpu_count,omitempty"`
	MaxPIDs        int     `json:"max_pids,omitempty"`
	MaxFileMB      int     `json:"max_file_mb,omitempty"`
	TimeoutSec     float64 `json:"timeout_sec,omitempty"`
	MaxOutputBytes int64   `json:"max_output_bytes,omitempty"`
}

// Wire converts to the serialisable form.
func (l Limits) Wire() WireLimits {
	w := WireLimits{
		MemoryMB:       l.MemoryMB,
		CPUCount:       l.CPUCount,
		MaxPIDs:        l.MaxPIDs,
		MaxFileMB:      l.MaxFileMB,
		MaxOutputBytes: l.MaxOutputBytes,
	}
	if l.Timeout > 0 {
		w.TimeoutSec = l.Timeout.Seconds()
	}
	return w
}

// Limits converts back to the internal form.
func (w WireLimits) Limits() Limits {
	l := Limits{
		MemoryMB:       w.MemoryMB,
		CPUCount:       w.CPUCount,
		MaxPIDs:        w.MaxPIDs,
		MaxFileMB:      w.MaxFileMB,
		MaxOutputBytes: w.MaxOutputBytes,
	}
	if w.TimeoutSec > 0 {
		l.Timeout = time.Duration(w.TimeoutSec * float64(time.Second))
	}
	return l
}

// WireSpec is the serialisable form of Spec (local IO such as Stderr is dropped).
//
// Note: paths in Spec.Argv / Spec.Dir / Mounts are all "sandbox-internal"; Mounts' Host is an absolute
// host path, which requires nexus and sandboxd to share the same storage.
type WireSpec struct {
	// Script / Args go together with Frame.Lang (the one-shot language path): the language name travels
	// in Frame.Lang (the same field the duplex channel uses), the script and its arguments live here.
	Script string     `json:"script,omitempty"`
	Args   []string   `json:"args,omitempty"`
	Argv   []string   `json:"argv,omitempty"`
	Dir    string     `json:"dir,omitempty"`
	Env    []string   `json:"env,omitempty"`
	Mounts []Mount    `json:"mounts,omitempty"`
	Limits WireLimits `json:"limits,omitempty"`
	// Egress requests outbound network. nil = no opinion (deployment default), false = explicitly
	// offline, true = wants outbound network.
	//
	// This is a **request**, not an outcome: sandboxd decides against the deployment ceiling before it
	// becomes an isolation argument (with network=off, true is rejected). One-shot only — the duplex
	// channel reuses an existing unit whose network was fixed at creation, and its open frame carries no
	// Spec at all.
	Egress *bool `json:"egress,omitempty"`
}

// Wire converts to the serialisable form.
func (s *Spec) Wire() *WireSpec {
	if s == nil {
		return nil
	}
	return &WireSpec{
		Script: s.Script,
		Args:   s.Args,
		Argv:   s.Argv,
		Dir:    s.Dir,
		Env:    s.Env,
		Mounts: s.Mounts,
		Limits: s.Limits.Wire(),
		Egress: s.Egress,
	}
}

// Spec converts back to the internal form.
func (w *WireSpec) Spec() *Spec {
	if w == nil {
		return nil
	}
	return &Spec{
		Script: w.Script,
		Args:   w.Args,
		Argv:   w.Argv,
		Dir:    w.Dir,
		Env:    w.Env,
		Mounts: w.Mounts,
		Limits: w.Limits.Limits(),
		Egress: w.Egress,
	}
}

// Frame is a control frame. Fields are used per Type; unused ones are omitted.
type Frame struct {
	Type string `json:"type"`

	// ready
	//
	// ScriptID is the ID of this script session (the sandboxd-side worker ID). It is the join key between
	// the two logs: only sandboxd knows the container identity, it sends this ID back, and this side
	// records when it appeared.
	ScriptID string `json:"script_id,omitempty"`
	Reused   bool   `json:"reused,omitempty"`

	// open
	Lang string    `json:"lang,omitempty"`
	Spec *WireSpec `json:"spec,omitempty"`

	// interactive open: long-lived interactive session identity and tool identifier (/v1/interactive only)
	SessionID string `json:"session_id,omitempty"`
	ToolID    string `json:"tool_id,omitempty"`

	// Ephemeral marks a temporary process (a call without a session): sandboxd uses it to pick the short
	// fallback timeout.
	//
	// The process can still be reused by later calls with the same session_id (several tool calls within
	// one run); destruction relies on nexus killing it explicitly when the run ends (fast path) or on the
	// short timeout. The default false means "kept permanently".
	Ephemeral bool `json:"ephemeral,omitempty"`

	// exec
	Script string         `json:"script,omitempty"`
	Inputs map[string]any `json:"inputs,omitempty"`

	// done
	Result any `json:"result,omitempty"`

	// done / error share this: script stderr (print output / error traceback), possibly truncated.
	Logs string `json:"logs,omitempty"`

	// exit / error
	Code    int    `json:"code,omitempty"`
	ErrCode string `json:"err_code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ============================================================================
// Conn: one WS connection to sandboxd
// ============================================================================

// Conn wraps frame reading and writing. Writes are serialised (gorilla does not allow concurrent writes).
type Conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

// Dial opens a connection to sandboxd.
func Dial(ctx context.Context, endpoint, token string) (*Conn, error) {
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, resp, err := d.DialContext(ctx, endpoint, h)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("sandbox: dial %s: %w (http %d)", endpoint, err, resp.StatusCode)
		}
		return nil, fmt.Errorf("sandbox: dial %s: %w", endpoint, err)
	}
	ws.SetReadLimit(maxFrameBytes)
	return &Conn{ws: ws}, nil
}

// NewConn builds a Conn from an established WebSocket connection (server side).
func NewConn(ws *websocket.Conn) *Conn {
	ws.SetReadLimit(maxFrameBytes)
	return &Conn{ws: ws}
}

// SendFrame sends a control frame (text frame).
func (c *Conn) SendFrame(f *Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("sandbox: marshal frame: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

// SendData sends a chunk of raw bytes (ch is the channel tag).
func (c *Conn) SendData(ch byte, p []byte) error {
	buf := make([]byte, 1+len(p))
	buf[0] = ch
	copy(buf[1:], p)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.BinaryMessage, buf)
}

// Recv reads the next message and returns either a control frame or raw bytes.
// When data is non-nil, data[0] is the channel tag and data[1:] is the payload.
func (c *Conn) Recv() (*Frame, []byte, error) {
	for {
		typ, data, err := c.ws.ReadMessage()
		if err != nil {
			return nil, nil, err
		}
		switch typ {
		case websocket.TextMessage:
			var f Frame
			if err := json.Unmarshal(data, &f); err != nil {
				return nil, nil, fmt.Errorf("sandbox: bad control frame: %w", err)
			}
			return &f, nil, nil
		case websocket.BinaryMessage:
			if len(data) == 0 {
				continue
			}
			return nil, data, nil
		default:
			continue
		}
	}
}

// Close closes the connection.
func (c *Conn) Close() error { return c.ws.Close() }
