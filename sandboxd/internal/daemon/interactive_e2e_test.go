package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"flame/tools/sandbox"
)

// End-to-end acceptance of the long-lived interactive endpoint: a real sandboxd service + a real WebSocket + a real
// docker.
//
// The core semantics of docker mode verified here:
//   - the first open lazily creates the container (a sandboxd=1 reusable unit) and the process is docker exec'd into it;
//   - a client disconnect neither kills the process nor destroys the container;
//   - a second open reuses the same container (the container count does not grow) and process state survives across
//     connections;
//   - FrameKill kills the process and destroys the container of the session that becomes empty.
//
// It needs a sandboxd running locally (127.0.0.1:27081 by default, overridable with SANDBOXD_TEST_INTERACTIVE_URL) and a
// usable docker (SANDBOXD_TEST_DOCKER), and skips otherwise.
// The reusable unit's container label is deliberately hardcoded to sandboxd=1 (not referencing the isolate package
// constant), for the same reason as sandboxd-exec=1 in exec_e2e_test.go — the end-to-end tests exercise the external
// contract.

const (
	defaultInteractiveURL = "ws://127.0.0.1:27081/v1/interactive"
	interactiveUnitLabel  = "sandboxd=1"
)

// interactiveE2E is the environment one end-to-end acceptance run needs.
type interactiveE2E struct {
	url  string
	bin  string
	base string
}

func interactiveE2EForTest(t *testing.T) interactiveE2E {
	t.Helper()

	url := os.Getenv("SANDBOXD_TEST_INTERACTIVE_URL")
	if url == "" {
		url = defaultInteractiveURL
	}
	base := strings.NewReplacer("ws://", "http://", "wss://", "https://").Replace(url)
	if i := strings.Index(base, "/v1/"); i > 0 {
		base = base[:i]
	}

	bin := os.Getenv("SANDBOXD_TEST_DOCKER")
	if bin == "" {
		bin = "docker"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("no docker binary (%s); set SANDBOXD_TEST_DOCKER to enable", bin)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("sandboxd not reachable at %s (%v); set SANDBOXD_TEST_INTERACTIVE_URL to enable", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("sandboxd /healthz returned %d at %s", resp.StatusCode, base)
	}
	return interactiveE2E{url: url, bin: bin, base: base}
}

// openInteractive opens one long-lived interactive connection and waits for the ready frame. A non-nil argv means first
// creation (with a Spec).
func openInteractive(t *testing.T, url, sessionID, toolID string, argv []string) *sandbox.Conn {
	t.Helper()
	conn, err := sandbox.Dial(context.Background(), url, "")
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	f := &sandbox.Frame{Type: sandbox.FrameOpen, SessionID: sessionID, ToolID: toolID}
	if argv != nil {
		f.Spec = (&sandbox.Spec{Argv: argv}).Wire()
	}
	if err := conn.SendFrame(f); err != nil {
		_ = conn.Close()
		t.Fatalf("send open: %v", err)
	}
	rf, _, err := conn.Recv()
	if err != nil {
		_ = conn.Close()
		t.Fatalf("wait ready: %v", err)
	}
	if rf.Type != sandbox.FrameReady {
		_ = conn.Close()
		t.Fatalf("want a ready frame, got %q (%s)", rf.Type, rf.Message)
	}
	return conn
}

// readStdoutUntil reads ChanStdout frames until the accumulated content contains want (the process is long-lived, so
// stdout never ends by itself).
func readStdoutUntil(conn *sandbox.Conn, want string, limit time.Duration) error {
	type result struct{ err error }
	ch := make(chan result, 1)
	go func() {
		var sb strings.Builder
		for {
			f, data, err := conn.Recv()
			if err != nil {
				ch <- result{err: err}
				return
			}
			if data != nil {
				if data[0] == sandbox.ChanStdout {
					sb.Write(data[1:])
					if strings.Contains(sb.String(), want) {
						ch <- result{}
						return
					}
				}
				continue
			}
			if f.Type == sandbox.FrameError {
				ch <- result{err: fmt.Errorf("sandboxd: %s", f.Message)}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.err
	case <-time.After(limit):
		return fmt.Errorf("timed out waiting for stdout to contain %q (%s)", want, limit)
	}
}

// interactiveContainerNames is the set of container names currently carrying the reusable unit label (sandboxd=1).
func interactiveContainerNames(t *testing.T, bin string) map[string]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "ps", "-a", "--filter", "label="+interactiveUnitLabel,
		"--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	set := make(map[string]struct{})
	for _, n := range strings.Fields(string(out)) {
		set[n] = struct{}{}
	}
	return set
}

func waitInteractiveContainer(t *testing.T, bin string, before map[string]struct{}) string {
	t.Helper()
	deadline := time.Now().Add(containerWait)
	for time.Now().Before(deadline) {
		for name := range interactiveContainerNames(t, bin) {
			if _, existed := before[name]; !existed {
				return name
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("this call left no observable reusable unit container behind")
	return ""
}

func waitInteractiveContainersGone(t *testing.T, bin string, names []string) {
	t.Helper()
	deadline := time.Now().Add(containerWait)
	var left []string
	for time.Now().Before(deadline) {
		now := interactiveContainerNames(t, bin)
		left = left[:0]
		for _, n := range names {
			if _, ok := now[n]; ok {
				left = append(left, n)
			}
		}
		if len(left) == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("containers %v did not disappear within %s", left, containerWait)
}

func dockerRemove(t *testing.T, bin, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, bin, "rm", "-f", name).Run()
}

// A disconnect does not kill the process, the same container is reused, and process state survives across connections.
func TestInteractiveE2EReusesProcessAcrossConnections(t *testing.T) {
	e := interactiveE2EForTest(t)
	before := interactiveContainerNames(t, e.bin)
	sid := "e2e-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const tool = "shell"

	// First connection: create the process with argv, feed commands, verify the stdout comes back
	c1 := openInteractive(t, e.url, sid, tool, []string{"/bin/sh"})
	if err := c1.SendData(sandbox.ChanStdin, []byte("export FOO=interactive-bar\n")); err != nil {
		t.Fatalf("send export: %v", err)
	}
	if err := c1.SendData(sandbox.ChanStdin, []byte("echo alive-e2e\n")); err != nil {
		t.Fatalf("send echo: %v", err)
	}
	if err := readStdoutUntil(c1, "alive-e2e", containerWait); err != nil {
		t.Fatalf("first echo: %v", err)
	}
	_ = c1.Close() // disconnect; the process stays

	// The container should have appeared and must survive the disconnect
	name := waitInteractiveContainer(t, e.bin, before)
	t.Cleanup(func() { dockerRemove(t, e.bin, name) })

	// Second connection: no argv, reuse the same process, read back the variable exported earlier
	c2 := openInteractive(t, e.url, sid, tool, nil)
	if err := c2.SendData(sandbox.ChanStdin, []byte("echo $FOO\n")); err != nil {
		t.Fatalf("send readback: %v", err)
	}
	if err := readStdoutUntil(c2, "interactive-bar", containerWait); err != nil {
		t.Fatalf("readback: %v", err)
	}
	_ = c2.Close()

	// Reuse does not create another container
	now := interactiveContainerNames(t, e.bin)
	if _, ok := now[name]; !ok {
		t.Errorf("container %s should still exist after reuse", name)
	}
	if len(now) != len(before)+1 {
		t.Errorf("reuse must not create another container: before=%d now=%d", len(before), len(now))
	}
}

// FrameKill kills the process and destroys the container of the session that becomes empty.
func TestInteractiveE2EKillDestroysContainer(t *testing.T) {
	e := interactiveE2EForTest(t)
	before := interactiveContainerNames(t, e.bin)
	sid := "e2e-kill-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	c := openInteractive(t, e.url, sid, "shell", []string{"/bin/sh"})
	name := waitInteractiveContainer(t, e.bin, before)

	if err := c.SendFrame(&sandbox.Frame{Type: sandbox.FrameKill}); err != nil {
		t.Fatalf("send kill: %v", err)
	}
	waitInteractiveContainersGone(t, e.bin, []string{name})
	_ = c.Close()
}
