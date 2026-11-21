//go:build e2e

// End-to-end check: register a skill in a real Postgres and execute its scripts through a real sandboxd.
//
// It needs external dependencies, so it is isolated behind the e2e build tag (a plain go test ./... does not compile it):
//   - Postgres (the database section of nexus/config.yaml)
//   - sandboxd listening on sandbox.url (with mode=none the host interpreter is used directly)
//   - python on the host PATH, with openpyxl installed
//
// Run it with:
//
//	cd nexus && go test -tags e2e ./service/agent/ -run TestE2ERunSkillScript -v
//
// It deliberately **bypasses the LLM**: what is verified is the run_skill_script chain (register -> visibility narrowing -> path resolution ->
// sandbox mount -> a real process -> result coming back); how the model decides is out of scope.
package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/paths"
	"flame/tools/sandbox"
	"flame/tools/xlog"
)

// e2eSkillName carries a suffix so it does not collide with the real xlsx-report skill.
const e2eSkillName = "xlsx-report-e2e"

// genSample builds a minimal input sheet (with the host python, which is the same interpreter the sandbox uses).
const genSample = `import sys
from openpyxl import Workbook
wb = Workbook()
ws = wb.active
ws.append(["订单号", "客户", "金额"])
for r in [["O001", "甲", 1200], ["O002", "乙", 300], ["O003", "丙", 5000], ["O004", "丁", 800], ["O005", "戊", "待定"]]:
    ws.append(r)
wb.save(sys.argv[1])
print("sample written")
`

// dumpRows reads every row of the artifact back, as evidence that it was really written correctly.
const dumpRows = `import sys
from openpyxl import load_workbook
ws = load_workbook(sys.argv[1]).active
print([r for r in ws.iter_rows(values_only=True)])
`

func TestE2ERunSkillScript(t *testing.T) {
	ctx := context.Background()

	// ---- 1. Process-wide globals: the same startup order as cmd/main.go (config -> db -> storage -> sandbox) ----
	cfg, cerr := config.LoadConfig("../../config.yaml")
	if cerr != nil {
		t.Fatalf("failed to load config.yaml: %v", cerr)
	}
	config.Cfg = cfg

	// xlog is a process-wide singleton too: without init, the db logging calls panic on nil
	if lerr := xlog.Init(t.TempDir(), "nexus"); lerr != nil {
		t.Fatalf("failed to initialise logging: %v", lerr)
	}
	defer xlog.Close()
	if err := db.InitDatabase(); err != nil {
		t.Fatalf("failed to connect to Postgres: %v", err)
	}
	if err := InitStorage(); err != nil {
		t.Fatalf("failed to initialise storage: %v", err)
	}
	sb, err := sandbox.NewRemote(sandbox.RemoteOptions{
		Endpoint: config.Cfg.Sandbox.ExecEndpoint(),
		Token:    config.Cfg.Sandbox.Token,
	})
	if err != nil {
		t.Fatalf("failed to build the remote sandbox client: %v", err)
	}
	tools.SetSandbox(sb)

	schema := "t0"
	rootDir := config.Cfg.Storage.RootDir
	// userID goes into a uuid column of the skills table (the creator), so it must be a valid UUID; use an obviously synthetic one.
	userID := "00000000-0000-4000-8000-000000000001"

	// ---- 2. Fetch the real main agent row (the repository only supports "get by id", and all we have here is the name) ----
	var mainID string
	if err := db.DB.QueryRow(`SELECT id FROM `+schema+`.agents WHERE name = $1`, "main").Scan(&mainID); err != nil {
		t.Fatalf("failed to read the main agent (is tenant %s provisioned?): %v", schema, err)
	}
	t.Logf("main agent id = %s", mainID)

	// ---- 3. Register the skill for real: through the product's own skillManager, not a direct INSERT ----
	mgr := NewSkillManager(schema, mainID, "main", userID, agentStorage, false, false, tools.ModeDraft)
	if derr := mgr.Delete(ctx, tools.SkillRef{Name: e2eSkillName}); derr != nil {
		t.Logf("cleaning up the previous leftover (an error on the first run is expected and can be ignored): %v", derr)
	}

	draft := filepath.Join("..", "..", "docs", "drafts", "xlsx-report")
	skillMD, err := os.ReadFile(filepath.Join(draft, "SKILL.md"))
	if err != nil {
		t.Fatalf("failed to read the draft SKILL.md (this case reuses the artifact of slice 1): %v", err)
	}
	if _, cerr := mgr.Create(ctx, tools.SkillInput{
		Name:        e2eSkillName,
		Description: "端到端验证用：Excel 报表处理",
		Content:     string(skillMD),
		Scope:       tools.SkillScopeTenant,
	}); cerr != nil {
		t.Fatalf("failed to register the skill: %v", cerr)
	}
	defer func() { _ = mgr.Delete(context.Background(), tools.SkillRef{Name: e2eSkillName}) }()

	// Auxiliary scripts: reuse the two from the draft (their logic is already verified on this machine; what is verified here is "the sandbox can
	// actually run them")
	skillKey := path.Dir(tools.SkillPath(schema, e2eSkillName))
	for _, rel := range []string{"scripts/probe.py", "scripts/main.py"} {
		b, rerr := os.ReadFile(filepath.Join(draft, filepath.FromSlash(rel)))
		if rerr != nil {
			t.Fatalf("failed to read draft %s: %v", rel, rerr)
		}
		if werr := agentStorage.WriteFile(ctx, skillKey+"/"+rel, b); werr != nil {
			t.Fatalf("failed to write skill script %s: %v", rel, werr)
		}
	}

	// Bind it to main: run_skill_script's criterion is "injected into this agent" (private ∪ bound)
	if berr := mgr.BindAgentSkill(ctx, mainID, e2eSkillName); berr != nil {
		t.Fatalf("failed to bind the skill: %v", berr)
	}
	injected, err := mgr.ListInjected(ctx)
	if err != nil {
		t.Fatalf("ListInjected failed: %v", err)
	}
	found := false
	for i := range injected {
		if injected[i].Name == e2eSkillName {
			found = true
			t.Logf("ListInjected hit: name=%s scope=%s id=%s", injected[i].Name, injected[i].Scope, injected[i].ID)
		}
	}
	if !found {
		t.Fatalf("skill %s is not in the injected list, run_skill_script would refuse it", e2eSkillName)
	}

	// ---- 4. Assemble a real Context: the construction from agent_impl.go, minus the LLM part ----
	runID := "e2e-" + time.Now().Format("150405")
	workKey := paths.UserRunsKey(schema, userID, runID)
	workDir := paths.ResolveUserRunsDir(rootDir, schema, userID, runID)
	c := &tools.Context{
		Schema:    schema,
		AgentName: "main",
		AgentID:   mainID,
		UserID:    userID,
		RunID:     runID,
		WorkKey:   workKey,
		Workspace: workDir,
		AgentRoot: paths.ResolveAgentDir(rootDir, schema, "main"),
		SkillRoot: paths.ResolveSkillsDir(rootDir, schema),
		Storage:   agentStorage,
		Skills:    mgr,
		Timeout:   60 * time.Second,
		MaxOutput: tools.DefaultMaxOutput,
		Sandbox:   tools.CurrentSandbox(),
	}
	if c.Sandbox == nil {
		t.Fatal("sandbox not injected: is sandbox.url configured and sandboxd running?")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Do not leave garbage in the development data directory: workDir is users/{uid}/runs/{runID}, so clean up two levels up as well
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(filepath.Dir(workDir))) })

	t.Logf("workspace: %s", workDir)

	// ---- 5. First run of probe.py: it lists the mount points, which directly proves /skill is really mounted ----
	runSkill(t, c, "probe.py", map[string]any{
		"skill":  e2eSkillName,
		"script": "scripts/probe.py",
	})

	// ---- 6. Second run of main.py: the real data path (read xlsx -> process -> write out/ -> self-check) ----
	inPath := filepath.Join(workDir, "in.xlsx")
	genSampleXlsx(t, inPath)

	res := runSkill(t, c, "main.py", map[string]any{
		"skill": e2eSkillName,
		"args":  []any{"--input", "in.xlsx", "--min-amount", "1000"},
	})
	if res["exit_code"] != 0 {
		t.Fatalf("main.py exited non-zero: exit_code=%v stderr=%s", res["exit_code"], res["stderr"])
	}

	outXlsx := filepath.Join(workDir, "out", "out.xlsx")
	st, serr := os.Stat(outXlsx)
	if serr != nil || st.Size() == 0 {
		t.Fatalf("the artifact was not produced or is empty: %v", serr)
	}
	t.Logf("artifact: %s (%d bytes)", outXlsx, st.Size())
	t.Logf("artifact content: %s", dumpXlsx(t, outXlsx))
}

// runSkill goes through the real dispatch entry ExecuteTool - the very door the agent loop uses to call a tool
// (identity hard gate, required-argument validation and timeout wrapping included).
func runSkill(t *testing.T, c *tools.Context, label string, args map[string]any) map[string]any {
	t.Helper()
	out, err := tools.ExecuteTool(context.Background(), c, "run_skill_script", args)
	if err != nil {
		t.Fatalf("%s: ExecuteTool failed: %v", label, err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("%s: unexpected return type: %T", label, out)
	}
	t.Logf("=== %s === skill=%v script=%v exit_code=%v truncated=%v",
		label, m["skill"], m["script"], m["exit_code"], m["truncated"])
	t.Logf("stdout:\n%s", m["stdout"])
	t.Logf("stderr:\n%s", m["stderr"])
	return m
}

func genSampleXlsx(t *testing.T, dst string) {
	t.Helper()
	if out, err := exec.Command("python", "-c", genSample, dst).CombinedOutput(); err != nil {
		t.Fatalf("failed to build the sample xlsx: %v\n%s", err, out)
	}
}

func dumpXlsx(t *testing.T, p string) string {
	t.Helper()
	out, err := exec.Command("python", "-c", dumpRows, p).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(read failed: %v %s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}
