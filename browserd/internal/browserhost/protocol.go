// Package browserhost is the browser driver: one process per browser session, owning one Chromium and accepting
// operations over a "one JSON object per line" protocol on stdin/stdout.
//
// Why a separate binary rather than letting browserd speak CDP itself:
//   - one process per session is the crash boundary: Chromium and its gpu / renderer / crashpad children sit in this
//     process's tree, so a hung page or a dead browser is reclaimed with one kill, and the pool sees "stdout ended"
//     instead of holding a half-dead lease;
//   - the pool never links CDP: it only moves bytes (see browserpool.worker.pumpStdout), so a page that hangs can hang
//     its own driver, never the service;
//   - everything that decides how Chromium is launched — flags, profile, viewport, cwd — lives in one place.
//
// Why stdout carries only the protocol and all logging goes to stderr: the pool forwards that stream to the caller's
// connection verbatim, so any non-protocol line mixed in would shift the parsing above.
package browserhost

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sync"
)

// Request is one operation request. Which fields apply depends on Op.
type Request struct {
	ID int    `json:"id"`
	Op string `json:"op"`

	// Navigation / opening
	URL    string `json:"url,omitempty"`
	NewTab bool   `json:"new_tab,omitempty"`
	Headed bool   `json:"headed,omitempty"` // only takes effect on the first open

	// Element operations (ref comes from a snapshot)
	Ref      string `json:"ref,omitempty"`
	Text     string `json:"text,omitempty"`
	Key      string `json:"key,omitempty"`
	Selector string `json:"selector,omitempty"`
	Submit   bool   `json:"submit,omitempty"`

	// Scrolling
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`

	// Screenshot
	Path     string `json:"path,omitempty"`
	FullPage bool   `json:"full_page,omitempty"`

	// Tabs
	Target string `json:"target,omitempty"`
	Index  int    `json:"index,omitempty"`

	// Snapshot budget and timeout
	MaxNodes  int `json:"max_nodes,omitempty"`
	MaxBytes  int `json:"max_bytes,omitempty"`
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// TabInfo is a tab summary (used by the tabs operation).
type TabInfo struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active,omitempty"`
}

// Response is the result of one operation.
type Response struct {
	ID int  `json:"id"`
	OK bool `json:"ok"`
	// Code is the machine-readable error code (invalid_ref / not_open / timeout / headed_not_allowed ...), and
	// Error is for humans (and the model). They are split so the caller can act on the code without matching text.
	Code      string    `json:"code,omitempty"`
	Error     string    `json:"error,omitempty"`
	URL       string    `json:"url,omitempty"`
	Title     string    `json:"title,omitempty"`
	Snapshot  string    `json:"snapshot,omitempty"`
	Nodes     int       `json:"nodes,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Text      string    `json:"text,omitempty"`
	Path      string    `json:"path,omitempty"`
	Bytes     int       `json:"bytes,omitempty"`
	Tabs      []TabInfo `json:"tabs,omitempty"`
	// Changed says whether the action may have changed the page. When false, the snapshot can usually be skipped.
	Changed bool `json:"changed,omitempty"`
	// Headed is the display mode the browser actually used. It is filled in on open only — the model must know
	// "did the headed mode I asked for actually take effect"; guessing wrong means it believes someone is watching.
	Headed *bool `json:"headed,omitempty"`
}

// opError is an operation failure carrying an error code.
type opError struct {
	code string
	msg  string
}

func (e *opError) Error() string { return e.msg }

func fail(code, format string, args ...any) *opError {
	return &opError{code: code, msg: fmt.Sprintf(format, args...)}
}

// lineWriter serialises writes to stdout: one JSON object per line. The browser host handles requests on a single
// thread; the lock is only there so that introducing asynchronous events later does not shift lines.
type lineWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func (l *lineWriter) write(r *Response) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(b); err != nil {
		return err
	}
	if err := l.w.WriteByte('\n'); err != nil {
		return err
	}
	return l.w.Flush()
}
