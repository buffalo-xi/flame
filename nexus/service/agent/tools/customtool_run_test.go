package tools

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flame/nexus/shared/capability"
	"flame/tools/sandbox"
)

// runStubSandbox captures the Spec and stdin, and uses out / wait to decide how the process behaves.
//
// No real process: the execution kernel's decisions (over-limit, masking, JSON parsing, exit codes) all live in Go and are covered
// by a stub; starting a real sandbox would only make the cases depend on the environment (whether sandboxd / the interpreter is
// ready).
type runStubSandbox struct {
	sandbox.Sandbox
	spec    *sandbox.Spec
	stdin   bytes.Buffer
	out     string
	errText string
	wait    error
}

func (s *runStubSandbox) Name() string { return "stub" }

func (s *runStubSandbox) Start(_ context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	s.spec = spec
	s.stdin.Reset()
	// stderr is written straight into spec.Stderr by the implementation (as the real one does); doing the same here is what makes
	// it possible to verify that the error text carries the stderr.
	if s.errText != "" && spec.Stderr != nil {
		_, _ = spec.Stderr.Write([]byte(s.errText))
	}
	return runStubProcess{sb: s}, nil
}

type runStubProcess struct {
	sandbox.Process
	sb *runStubSandbox
}

func (p runStubProcess) Stdin() io.WriteCloser { return nopWriteCloser{&p.sb.stdin} }

func (p runStubProcess) Stdout() io.ReadCloser {
	return io.NopCloser(strings.NewReader(p.sb.out))
}

func (p runStubProcess) Wait() error { return p.sb.wait }

type nopWriteCloser struct{ w io.Writer }

func (n nopWriteCloser) Write(p []byte) (int, error) { return n.w.Write(p) }

func (n nopWriteCloser) Close() error { return nil }

// newRunFixture builds an execution input where "the tool directory really exists on disk".
// ToolsRoot / Workspace both live in temp directories: the kernel os.Stat's the mount sources, and fake paths would mask the real
// errors.
func newRunFixture(t *testing.T, spec CustomToolSpec) (CustomToolRun, *runStubSandbox) {
	t.Helper()
	if spec.Name == "" {
		spec.Name = "demo"
	}
	if spec.Entry == "" {
		spec.Entry = "main.py"
	}
	root := t.TempDir()
	dir := filepath.Join(root, spec.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, spec.Entry), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb := &runStubSandbox{}
	return CustomToolRun{
		Spec:      spec,
		ToolsRoot: root,
		Workspace: t.TempDir(),
		Sandbox:   sb,
		MaxOutput: 1 << 16,
	}, sb
}

// The success path pins three things at once: result parsing, the stdin protocol and the isolation arguments.
// stdin is the only channel through which a script receives arguments and config, so any drift in its format breaks every custom
// tool at the same time.
func TestRunCustomToolSpecSuccess(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{
		Lang:    "python",
		Configs: map[string]any{"region": "cn"},
	})
	sb.out = `{"total": 3}`

	res, err := RunCustomToolSpec(context.Background(), run, map[string]any{"month": "2026-09"})
	if err != nil {
		t.Fatalf("should succeed, got %v", err)
	}
	m, ok := res.Result.(map[string]any)
	if !ok || m["total"] != float64(3) {
		t.Fatalf("result parsing failed: %#v", res.Result)
	}
	if res.ExitCode != 0 || res.Truncated || res.Masked {
		t.Errorf("the success path must not carry these flags: %+v", res)
	}
	if !strings.Contains(sb.stdin.String(), `"month":"2026-09"`) {
		t.Errorf("stdin did not carry the model arguments: %s", sb.stdin.String())
	}
	if !strings.Contains(sb.stdin.String(), `"region":"cn"`) {
		t.Errorf("stdin did not carry the tool config: %s", sb.stdin.String())
	}
	if sb.spec == nil || len(sb.spec.Mounts) != 2 {
		t.Fatalf("unexpected isolation arguments: %+v", sb.spec)
	}
	if sb.spec.Mounts[1].Guest != scriptGuestDir || !sb.spec.Mounts[1].ReadOnly {
		t.Errorf("the tool directory should be mounted read-only at %s: %+v", scriptGuestDir, sb.spec.Mounts[1])
	}
}

// Non-JSON stdout must fail, but the diagnostics have to be kept - the test run relies on them to show "what the script actually
// printed".
func TestRunCustomToolSpecNonJSONKeepsDiagnostics(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{Lang: "python"})
	sb.out = "debug line\n"

	res, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "non-JSON to stdout") {
		t.Fatalf("it should report that stdout is not JSON, got %v", err)
	}
	if res == nil || !strings.Contains(res.Stdout, "debug line") {
		t.Fatalf("stdout diagnostics must be returned on failure too: %+v", res)
	}
}

// A non-zero exit code: the error must carry the exit code and stderr, and the result object is still returned (for the test run to
// display).
//
// The regression this guards: a remote sandbox returns *sandbox.ExitError (remote.go FrameExit). Checking only *exec.ExitError
// degrades the whole failure into a bare "exit status 2" - the exit code becomes 0, stderr is lost, and the test run shows "it
// failed with no reason at all".
func TestRunCustomToolSpecNonZeroExit(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{Lang: "python"})
	sb.wait = &sandbox.ExitError{Code: 2}
	sb.out = `{"ignored":true}`
	sb.errText = "参数 principal 必填：本金金额（元）\n"

	res, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "exited with code 2") {
		t.Fatalf("it should report exit code 2, got %v", err)
	}
	if !strings.Contains(err.Error(), "参数 principal 必填") {
		t.Fatalf("the error should carry the verbatim stderr, got %v", err)
	}
	if res == nil || res.ExitCode != 2 {
		t.Fatalf("exit code 2 must be returned on failure: %+v", res)
	}
}

// Output over the limit fails outright (a truncated value is broken JSON, and silent truncation is far harder to diagnose than an
// error).
func TestRunCustomToolSpecTruncated(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{Lang: "python"})
	run.Spec.MaxOutput = 4
	sb.out = `{"long":"value"}`

	res, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("it should report exceeding the limit, got %v", err)
	}
	if res == nil || !res.Truncated {
		t.Fatalf("it should be flagged truncated: %+v", res)
	}
}

// Secret masking: a long enough config value appearing in the output is replaced, and the result is flagged _masked.
// This also holds "stdout in the diagnostics envelope is masked too" - the test run sends stdout to the browser verbatim.
func TestRunCustomToolSpecMasksSecrets(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{
		Lang:    "python",
		Configs: map[string]any{"api_key": "sk-1234567890"},
	})
	sb.out = `{"key":"sk-1234567890"}`

	res, err := RunCustomToolSpec(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("should succeed, got %v", err)
	}
	if !res.Masked {
		t.Fatal("it should be flagged as masked")
	}
	if strings.Contains(res.Stdout, "sk-1234567890") {
		t.Errorf("the stdout diagnostics still contain the plaintext secret: %s", res.Stdout)
	}
	m, _ := res.Result.(map[string]any)
	if m["_masked"] != true {
		t.Errorf("the result should carry the _masked hint: %#v", res.Result)
	}
}

// A missing required config is reported before execution, naming the key - this is the only place that can provide that
// information.
func TestRunCustomToolSpecMissingRequiredConfig(t *testing.T) {
	run, _ := newRunFixture(t, CustomToolSpec{
		Lang:            "python",
		RequiredConfigs: []string{"api_key"},
	})

	_, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("it should name the missing config, got %v", err)
	}
}

// An unreachable on-disk directory (missing mount source) must be reported with its path, not deferred to sandbox start as a bare
// ENOENT.
func TestRunCustomToolSpecMissingToolDir(t *testing.T) {
	run, _ := newRunFixture(t, CustomToolSpec{Lang: "python"})
	run.ToolsRoot = t.TempDir() // empty directory: no demo/

	_, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "not reachable on the host") {
		t.Fatalf("it should report the directory as unreachable, got %v", err)
	}
}

// Egress needs both layers: "declared by the tool AND enabled by the platform" - if either is off the network is closed.
func TestRunCustomToolSpecEgressNeedsBothLayers(t *testing.T) {
	cases := []struct {
		name   string
		egress bool
		caps   map[string]bool
		want   bool
	}{
		{"declared but not enabled by the platform", true, map[string]bool{capability.Exec: true}, false},
		{"declared and enabled by the platform", true, map[string]bool{capability.Exec: true, capability.Egress: true}, true},
		{"not declared even though the platform allows it", false, map[string]bool{capability.Exec: true, capability.Egress: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, sb := newRunFixture(t, CustomToolSpec{Lang: "python", Egress: tc.egress})
			run.Caps = tc.caps
			sb.out = `{}`
			if _, err := RunCustomToolSpec(context.Background(), run, nil); err != nil {
				t.Fatalf("execution should succeed: %v", err)
			}
			if sb.spec.Egress == nil || *sb.spec.Egress != tc.want {
				t.Fatalf("egress = %v, want %v", sb.spec.Egress, tc.want)
			}
		})
	}
}
