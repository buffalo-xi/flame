package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"flame/tools/sandbox"
)

// End-to-end acceptance of the simplex endpoint: a real sandboxd service + a real WebSocket + a real docker.
//
// It needs a sandboxd running locally (127.0.0.1:27081 by default, overridable with SANDBOXD_TEST_EXEC_URL) and a usable
// docker (SANDBOXD_TEST_DOCKER), and skips otherwise.
//
// What is asserted is the **external contract**: once an execution ends, the container created for it must be gone. How
// containers are named and labelled is documented in nexus/docs/sandbox-design.md §9.6. The label is deliberately
// hardcoded here rather than referencing the isolate package constant: an end-to-end test checks the contract and
// should not change whenever an internal constant is renamed.

const (
	defaultExecURL     = "ws://127.0.0.1:27081/v1/exec"
	execContainerLabel = "sandboxd-exec=1"
	containerWait      = 20 * time.Second
)

// execE2E is the environment one end-to-end acceptance run needs.
type execE2E struct {
	url  string
	bin  string // docker executable
	base string // HTTP base address (for /healthz)
}

// execE2EForTest probes the environment: skip when the service is unreachable or docker is missing.
func execE2EForTest(t *testing.T) execE2E {
	t.Helper()

	url := os.Getenv("SANDBOXD_TEST_EXEC_URL")
	if url == "" {
		url = defaultExecURL
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
		t.Skipf("sandboxd not reachable at %s (%v); set SANDBOXD_TEST_EXEC_URL to enable", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("sandboxd /healthz returned %d at %s", resp.StatusCode, base)
	}
	return execE2E{url: url, bin: bin, base: base}
}

// TestExecE2EDisconnectKillsContainer: after the client drops the connection, the container of this execution must be
// gone.
//
// That is exactly the shape of a timeout / vanished process on the nexus side: it sends no frame at all, it just
// disconnects.
func TestExecE2EDisconnectKillsContainer(t *testing.T) {
	e := execE2EForTest(t)
	before := execContainerNames(t, e.bin)

	spec := &sandbox.Spec{Argv: []string{"/bin/sh", "-c", "sleep 300 & sleep 300"}}
	conn := openExec(t, e, spec)

	// Confirm the container is really up first, otherwise the later assertion may only be testing "it never ran".
	// The background sleep holds stdout while the foreground sleep keeps the shell alive — a process tree.
	appeared := waitNewContainer(t, e.bin, before)

	// Disconnect: close the WS outright, without sending any frame.
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The core assertion: the container (and the process tree inside it) must be collected.
	waitContainersGone(t, e.bin, []string{appeared})

	// The service must not be held up by this disconnect.
	assertHealthy(t, e.base)
}

// TestExecE2EFrameKillKillsContainer: an explicit kill frame must clean up just as thoroughly (the path where the
// connection is still alive).
func TestExecE2EFrameKillKillsContainer(t *testing.T) {
	e := execE2EForTest(t)
	before := execContainerNames(t, e.bin)

	spec := &sandbox.Spec{Argv: []string{"/bin/sh", "-c", "sleep 300 & sleep 300"}}
	conn := openExec(t, e, spec)
	defer func() { _ = conn.Close() }()

	appeared := waitNewContainer(t, e.bin, before)

	if err := conn.SendFrame(&sandbox.Frame{Type: sandbox.FrameKill}); err != nil {
		t.Fatalf("send kill: %v", err)
	}
	waitContainersGone(t, e.bin, []string{appeared})
}

// TestExecE2ENormalExit: a command that runs to completion — the output and the exit code must be right, and no
// container may be left behind.
//
// The container may be faster than docker ps (already deleted by --rm), so there is no "the container must be visible"
// assertion here, only "no extra container appeared" — a criterion independent of timing.
func TestExecE2ENormalExit(t *testing.T) {
	e := execE2EForTest(t)
	before := execContainerNames(t, e.bin)

	spec := &sandbox.Spec{Argv: []string{"/bin/sh", "-c", "echo e2e-ok"}}
	conn := openExec(t, e, spec)
	defer func() { _ = conn.Close() }()

	out, code, err := readExit(conn, containerWait)
	if err != nil {
		t.Fatalf("read exit: %v", err)
	}
	if !strings.Contains(out, "e2e-ok") {
		t.Errorf("stdout = %q, want it to contain e2e-ok", out)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	assertNoNewContainers(t, e.bin, before)
}

// openExec opens one simplex execution and waits for the ready frame.
func openExec(t *testing.T, e execE2E, spec *sandbox.Spec) *sandbox.Conn {
	t.Helper()
	conn, err := sandbox.Dial(context.Background(), e.url, "")
	if err != nil {
		t.Fatalf("dial %s: %v", e.url, err)
	}
	if err := conn.SendFrame(&sandbox.Frame{Type: sandbox.FrameOpen, Spec: spec.Wire()}); err != nil {
		_ = conn.Close()
		t.Fatalf("send open: %v", err)
	}
	f, _, err := conn.Recv()
	if err != nil {
		_ = conn.Close()
		t.Fatalf("wait ready: %v", err)
	}
	if f.Type != sandbox.FrameReady {
		_ = conn.Close()
		t.Fatalf("want a ready frame, got %q (%s)", f.Type, f.Message)
	}
	return conn
}

// readExit reads until the exit frame, collecting stdout and the exit code.
//
// The read happens in a goroutine with a timeout: Recv has no read deadline, so once the server fails to send an exit
// frame the test must be able to fail rather than hang.
func readExit(conn *sandbox.Conn, limit time.Duration) (string, int, error) {
	type result struct {
		out  string
		code int
		err  error
	}
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
				}
				continue
			}
			switch f.Type {
			case sandbox.FrameExit:
				ch <- result{out: sb.String(), code: f.Code}
				return
			case sandbox.FrameError:
				ch <- result{err: fmt.Errorf("sandboxd: %s", f.Message)}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.out, r.code, r.err
	case <-time.After(limit):
		return "", 0, fmt.Errorf("timed out waiting for the exit frame (%s)", limit)
	}
}

// execContainerNames is the set of container names currently carrying the exec label.
func execContainerNames(t *testing.T, bin string) map[string]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "ps", "-a", "--filter", "label="+execContainerLabel,
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

// waitNewContainer waits for a container "not seen before" to appear and returns its name.
func waitNewContainer(t *testing.T, bin string, before map[string]struct{}) string {
	t.Helper()
	deadline := time.Now().Add(containerWait)
	for time.Now().Before(deadline) {
		for name := range execContainerNames(t, bin) {
			if _, existed := before[name]; !existed {
				return name
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("this execution left no observable container: either it never started, or it vanished right after")
	return ""
}

// waitContainersGone waits until all of these containers are gone.
func waitContainersGone(t *testing.T, bin string, names []string) {
	t.Helper()
	deadline := time.Now().Add(containerWait)
	var left []string
	for time.Now().Before(deadline) {
		now := execContainerNames(t, bin)
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
	t.Fatalf("containers %v did not disappear within %s — this execution was not cleaned up", left, containerWait)
}

// assertNoNewContainers confirms no extra container appeared (a timing-independent criterion).
func assertNoNewContainers(t *testing.T, bin string, before map[string]struct{}) {
	t.Helper()
	for name := range execContainerNames(t, bin) {
		if _, existed := before[name]; !existed {
			t.Errorf("an extra container %s appeared — this execution was not cleaned up", name)
		}
	}
}

// assertHealthy: the service was not held up or killed by this connection.
func assertHealthy(t *testing.T, base string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the service stopped answering /healthz after the disconnect: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d", resp.StatusCode)
	}
}
