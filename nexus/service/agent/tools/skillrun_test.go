package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"flame/nexus/shared/sandboxinfo"
	"flame/nexus/shared/storage"
	"flame/tools/sandbox"
)

// stubSkillsInjected implements ListInjected only - that is where resolveSkillScript's visibility criterion sits.
// The remaining methods fall back to the embedded interface (a call panics), so a case cannot quietly rely on unstubbed behaviour.
type stubSkillsInjected struct {
	SkillsManager
	injected []SkillInfo
}

func (s stubSkillsInjected) ListInjected(context.Context) ([]SkillInfo, error) {
	return s.injected, nil
}

// stubSandbox captures the Spec without running a real process; wait decides what Wait returns.
type stubSandbox struct {
	sandbox.Sandbox
	spec *sandbox.Spec
	wait error
}

func (s *stubSandbox) Name() string { return "stub" }

func (s *stubSandbox) Start(_ context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	s.spec = spec
	return stubProcess{wait: s.wait}, nil
}

type stubProcess struct {
	sandbox.Process
	wait error
}

func (p stubProcess) Stdout() io.ReadCloser {
	return io.NopCloser(strings.NewReader("hello"))
}

func (p stubProcess) Wait() error { return p.wait }

// newSkillRunCtx builds the smallest Context that exists on disk:
// SkillRoot / AgentRoot live in a temp directory and Storage's root aligns with it - a storage key and a host path must point at
// the same place, otherwise false positives such as "storage says it exists, os.Stat says it does not" hide real bugs.
func newSkillRunCtx(t *testing.T, injected []SkillInfo) *Context {
	t.Helper()
	// Execute's admission (ensureSandboxReady) requires that a capability snapshot has been fetched successfully at least once,
	// and unit tests have no sandboxd; injecting a minimal snapshot lets the admission through.
	// Note that this is process-level global state (sandboxinfo has no public API to restore "unconfigured"):
	// do not add cases in this package that assert Ready() == false.
	sandboxinfo.Set(sandboxinfo.Info{Mode: "none"})

	root := t.TempDir()
	return &Context{
		Schema:    "t0",
		AgentName: "poet",
		AgentID:   "poet-id",
		UserID:    "u1",
		WorkKey:   "tenants/t0/users/u1/sessions/s1",
		Workspace: filepath.Join(root, "tenants", "t0", "users", "u1", "sessions", "s1"),
		AgentRoot: filepath.Join(root, "tenants", "t0", "agents", "poet"),
		SkillRoot: filepath.Join(root, "tenants", "t0", "skills"),
		Storage:   storage.NewFilesystemStorage(root),
		Skills:    stubSkillsInjected{injected: injected},
	}
}

func mkSkillScript(t *testing.T, dir, rel string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("# stub"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The tool must be registered and filed under the exec category - the category is the only criterion for the capability gate and
// the grant catalog.
//
// The criterion is the channel, not "is a script file being run": run_skill_script goes sb.Start -> /v1/exec, the same
// single-worker channel as the exec tool (one-shot process, no pool, no script_max_sec hard limit);
// sandbox.code is the /v1/code channel and belongs only to workflow code nodes (code / handler / post-processing).
// Both can execute arbitrary code; the category is not a permission boundary, it only grants resources by shape.
func TestRunSkillScriptRegisteredInExecCategory(t *testing.T) {
	tool, ok := Get("run_skill_script")
	if !ok {
		t.Fatal("run_skill_script is not registered")
	}
	if got := tool.Meta().Category; got != CategoryExec {
		t.Errorf("Category = %q, want %q", got, CategoryExec)
	}
}

// A tenant-level shared skill: the directory sits under skills/, and both the storage key and the host path must line up.
func TestResolveSkillScriptTenantSkill(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "xlsx-report", Scope: SkillScopeTenant}})
	dir := filepath.Join(c.SkillRoot, "xlsx-report")
	mkSkillScript(t, dir, "scripts/main.py")

	run, err := resolveSkillScript(context.Background(), c, "xlsx-report", "")
	if err != nil {
		t.Fatal(err)
	}
	if run.HostDir != dir {
		t.Errorf("HostDir = %q, want %q", run.HostDir, dir)
	}
	if run.StorageKey != "tenants/t0/skills/xlsx-report" {
		t.Errorf("StorageKey = %q", run.StorageKey)
	}
	if run.GuestScript != "/skill/scripts/main.py" {
		t.Errorf("GuestScript = %q", run.GuestScript)
	}
	// .py has a language definition -> the language path is used (the interpreter entry is resolved by sandboxd per mode)
	if run.Lang != "python" {
		t.Errorf("Lang = %q, want python", run.Lang)
	}
	if run.Interpreter != "" {
		t.Errorf("the language path must not carry a direct interpreter, Interpreter = %q", run.Interpreter)
	}
}

// shell has no language definition (and is not a duplex language), so it must fall back to passing argv directly - the same
// behaviour as before the change.
func TestResolveSkillScriptShellPassthrough(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "sheller", Scope: SkillScopeTenant}})
	dir := filepath.Join(c.SkillRoot, "sheller")
	mkSkillScript(t, dir, "scripts/main.sh")

	run, err := resolveSkillScript(context.Background(), c, "sheller", "scripts/main.sh")
	if err != nil {
		t.Fatal(err)
	}
	if run.Lang != "" {
		t.Errorf("Lang = %q, want empty (.sh has no language definition)", run.Lang)
	}
	if run.Interpreter != "sh" {
		t.Errorf("Interpreter = %q, want sh", run.Interpreter)
	}
}

// A language name is not a command name: for .js what is handed over is "js", while the command name node is decided by
// sandboxd.
func TestResolveSkillScriptJsLangNotCommand(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "front", Scope: SkillScopeTenant}})
	dir := filepath.Join(c.SkillRoot, "front")
	mkSkillScript(t, dir, "scripts/main.js")

	run, err := resolveSkillScript(context.Background(), c, "front", "scripts/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if run.Lang != "js" {
		t.Errorf("Lang = %q, want js (the language name, not the command name node)", run.Lang)
	}
}

// An agent's private skill: the directory hangs under AgentRoot/skills (inside exec's /agent mount).
func TestResolveSkillScriptPrivateSkill(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "helper", Scope: SkillScopeAgent}})
	dir := filepath.Join(c.AgentRoot, "skills", "helper")
	mkSkillScript(t, dir, "scripts/main.py")

	run, err := resolveSkillScript(context.Background(), c, "helper", "scripts/main.py")
	if err != nil {
		t.Fatal(err)
	}
	if run.HostDir != dir {
		t.Errorf("HostDir = %q, want %q", run.HostDir, dir)
	}
	if run.StorageKey != "tenants/t0/agents/poet/skills/helper" {
		t.Errorf("StorageKey = %q", run.StorageKey)
	}
}

// On a name clash the private skill wins over the shared one (the same rule as at run time).
func TestResolveSkillScriptPrefersPrivateOverShared(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{
		{Name: "dup", Scope: SkillScopeTenant},
		{Name: "dup", Scope: SkillScopeAgent},
	})
	mkSkillScript(t, filepath.Join(c.SkillRoot, "dup"), "scripts/main.py")
	mkSkillScript(t, filepath.Join(c.AgentRoot, "skills", "dup"), "scripts/main.py")

	run, err := resolveSkillScript(context.Background(), c, "dup", "")
	if err != nil {
		t.Fatal(err)
	}
	if run.StorageKey != "tenants/t0/agents/poet/skills/dup" {
		t.Errorf("StorageKey = %q; on a name clash the private one should be taken", run.StorageKey)
	}
}

// The refusal surface: not injected / escaping path / absolute path / unsupported extension / skill directory missing.
func TestResolveSkillScriptRejects(t *testing.T) {
	ok := []SkillInfo{{Name: "xlsx-report", Scope: SkillScopeTenant}}
	cases := []struct {
		name    string
		skill   string
		script  string
		created bool
	}{
		{"skill not injected", "other", "", false},
		{"escaping path", "xlsx-report", "../../etc/passwd.py", true},
		{"absolute path", "xlsx-report", "/etc/x.py", true},
		{"unsupported extension", "xlsx-report", "scripts/main.rb", true},
		{"skill directory missing", "xlsx-report", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newSkillRunCtx(t, ok)
			if tc.created {
				mkSkillScript(t, filepath.Join(c.SkillRoot, "xlsx-report"), "scripts/main.py")
			}
			if _, err := resolveSkillScript(context.Background(), c, tc.skill, tc.script); err == nil {
				t.Fatal("should be refused")
			}
		})
	}
}

// Mounts and startup intent are the core contract of slice 2: the skill directory is mounted read-only at /skill, the workspace is
// mounted writable at /workspace and used as cwd, args are forwarded verbatim, and the interpreter entry is not decided on this
// side (see the assertions below).
func TestRunSkillScriptSpecAndResult(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "xlsx-report", Scope: SkillScopeTenant}})
	dir := filepath.Join(c.SkillRoot, "xlsx-report")
	mkSkillScript(t, dir, "scripts/main.py")

	sb := &stubSandbox{}
	c.Sandbox = sb

	out, err := (&runSkillCustomTool{}).Execute(context.Background(), c, map[string]any{
		"skill": "xlsx-report",
		"args":  []any{"--input", "in.xlsx", "--min-amount", "1000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sb.spec == nil {
		t.Fatal("sandbox Start was not called")
	}

	// Language path: this side reports only "language + script + arguments", and the interpreter entry (python / node / lua) is
	// resolved by sandboxd per the current mode - assembling argv here would hardcode "what it is called inside the image", which
	// is knowledge that should live only in sandboxd.
	if sb.spec.Lang != "python" {
		t.Errorf("Lang = %q, want python", sb.spec.Lang)
	}
	if sb.spec.Script != "/skill/scripts/main.py" {
		t.Errorf("Script = %q, want /skill/scripts/main.py", sb.spec.Script)
	}
	wantArgs := []string{"--input", "in.xlsx", "--min-amount", "1000"}
	if !reflect.DeepEqual(sb.spec.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", sb.spec.Args, wantArgs)
	}
	if len(sb.spec.Argv) != 0 {
		t.Errorf("Argv = %v, it must be empty on the language path (mutually exclusive with Lang)", sb.spec.Argv)
	}
	if sb.spec.Dir != "/workspace" {
		t.Errorf("Dir = %q, want /workspace", sb.spec.Dir)
	}
	if len(sb.spec.Mounts) != 2 {
		t.Fatalf("Mounts = %+v, want 2 entries", sb.spec.Mounts)
	}
	if sb.spec.Mounts[0].Guest != "/workspace" || sb.spec.Mounts[0].ReadOnly {
		t.Errorf("entry 1 should be a writable /workspace: %+v", sb.spec.Mounts[0])
	}
	if sb.spec.Mounts[1].Guest != "/skill" || !sb.spec.Mounts[1].ReadOnly || sb.spec.Mounts[1].Host != dir {
		t.Errorf("entry 2 should be the read-only skill directory: %+v", sb.spec.Mounts[1])
	}

	m := out.(map[string]any)
	if m["exit_code"] != 0 {
		t.Errorf("exit_code = %v, want 0", m["exit_code"])
	}
	if m["stdout"] != "hello" {
		t.Errorf("stdout = %v, want hello", m["stdout"])
	}
	if m["skill"] != "xlsx-report" || m["script"] != "scripts/main.py" {
		t.Errorf("unexpected echo: %+v", m)
	}
}

// .sh has no language definition (it is absent from sandboxd's top-level langs), so the argv passthrough path must be used: the
// interpreter comes from this side, the script path sits under /skill, and args are appended last.
// This path doubles as the regression slot for "behaviour identical before and after the change".
func TestRunSkillScriptShellPassthroughSpec(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "sheller", Scope: SkillScopeTenant}})
	mkSkillScript(t, filepath.Join(c.SkillRoot, "sheller"), "scripts/main.sh")

	sb := &stubSandbox{}
	c.Sandbox = sb

	if _, err := (&runSkillCustomTool{}).Execute(context.Background(), c, map[string]any{
		"skill":  "sheller",
		"script": "scripts/main.sh",
		"args":   []any{"a", "b"},
	}); err != nil {
		t.Fatal(err)
	}

	if sb.spec.Lang != "" {
		t.Errorf("Lang = %q; .sh has no language definition and it should be empty", sb.spec.Lang)
	}
	wantArgv := []string{"sh", "/skill/scripts/main.sh", "a", "b"}
	if !reflect.DeepEqual(sb.spec.Argv, wantArgv) {
		t.Errorf("Argv = %v, want %v", sb.spec.Argv, wantArgv)
	}
}

// When Wait returns something other than an ExitError it must propagate unchanged (not be swallowed as exit 0).
func TestRunSkillScriptPropagatesWaitError(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "xlsx-report", Scope: SkillScopeTenant}})
	mkSkillScript(t, filepath.Join(c.SkillRoot, "xlsx-report"), "scripts/main.py")
	boom := errors.New("boom")
	c.Sandbox = &stubSandbox{wait: boom}

	if _, err := (&runSkillCustomTool{}).Execute(context.Background(), c,
		map[string]any{"skill": "xlsx-report"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
