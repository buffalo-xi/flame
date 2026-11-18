//go:build e2e

// 端到端验证：在真实 Postgres 里注册技能 + 通过真实 sandboxd 执行技能脚本。
//
// 需要外部依赖，所以用 e2e 构建标签隔离（常规 go test ./... 不会编译它）：
//   - Postgres（nexus/config.yaml 的 database 段）
//   - sandboxd 监听在 sandbox.url（mode=none 时直接用宿主解释器）
//   - 宿主 PATH 有 python，且装了 openpyxl
//
// 运行：
//
//	cd nexus && go test -tags e2e ./service/agent/ -run TestE2ERunSkillScript -v
//
// 刻意**不经过 LLM**：验证对象是 run_skill_script 这条链路（注册 → 可见性收口 →
// 路径解析 → 沙箱挂载 → 真的起进程 → 结果回传），模型怎么决策不在范围内。
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

// e2eSkillName 带后缀，避免撞上真实的 xlsx-report 技能。
const e2eSkillName = "xlsx-report-e2e"

// genSample 造一份最小输入表（用宿主 python，与沙箱内是同一个解释器）。
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

// dumpRows 回读产物的每一行，作为「真的写对了」的证据。
const dumpRows = `import sys
from openpyxl import load_workbook
ws = load_workbook(sys.argv[1]).active
print([r for r in ws.iter_rows(values_only=True)])
`

func TestE2ERunSkillScript(t *testing.T) {
	ctx := context.Background()

	// ---- 1. 进程级全局：与 cmd/main.go 的启动顺序一致（config → db → storage → sandbox）----
	cfg, cerr := config.LoadConfig("../../config.yaml")
	if cerr != nil {
		t.Fatalf("加载 config.yaml 失败: %v", cerr)
	}
	config.Cfg = cfg

	// xlog 同样是进程级单例：不初始化会在 db 的日志调用处 nil panic
	if lerr := xlog.Init(t.TempDir(), "nexus"); lerr != nil {
		t.Fatalf("初始化日志失败: %v", lerr)
	}
	defer xlog.Close()
	if err := db.InitDatabase(); err != nil {
		t.Fatalf("连接 Postgres 失败: %v", err)
	}
	if err := InitStorage(); err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	sb, err := sandbox.NewRemote(sandbox.RemoteOptions{
		Endpoint: config.Cfg.Sandbox.ExecEndpoint(),
		Token:    config.Cfg.Sandbox.Token,
	})
	if err != nil {
		t.Fatalf("构造远端沙箱客户端失败: %v", err)
	}
	tools.SetSandbox(sb)

	schema := "t0"
	rootDir := config.Cfg.Agent.RootDir
	// userID 会写进 skills 表的 uuid 列（创建者），必须是合法 UUID；取一个明显合成的。
	userID := "00000000-0000-4000-8000-000000000001"

	// ---- 2. 取真实的 main agent 行（repository 只有「按 id 取」，这里手上只有名字）----
	var mainID string
	if err := db.DB.QueryRow(`SELECT id FROM `+schema+`.agents WHERE name = $1`, "main").Scan(&mainID); err != nil {
		t.Fatalf("查 main agent 失败（租户 %s 是否已开通？）: %v", schema, err)
	}
	t.Logf("main agent id = %s", mainID)

	// ---- 3. 真的注册技能：走产品自己的 skillManager，而不是直接 INSERT ----
	mgr := NewSkillManager(schema, mainID, "main", userID, agentStorage, false, false, tools.ModeDraft)
	if derr := mgr.Delete(ctx, tools.SkillRef{Name: e2eSkillName}); derr != nil {
		t.Logf("清理上次残留（首次运行会报错，可忽略）: %v", derr)
	}

	draft := filepath.Join("..", "..", "docs", "drafts", "xlsx-report")
	skillMD, err := os.ReadFile(filepath.Join(draft, "SKILL.md"))
	if err != nil {
		t.Fatalf("读草稿 SKILL.md 失败（本用例直接复用切片 1 的产物）: %v", err)
	}
	if _, cerr := mgr.Create(ctx, tools.SkillInput{
		Name:        e2eSkillName,
		Description: "端到端验证用：Excel 报表处理",
		Content:     string(skillMD),
		Scope:       tools.SkillScopeTenant,
	}); cerr != nil {
		t.Fatalf("注册技能失败: %v", cerr)
	}
	defer func() { _ = mgr.Delete(context.Background(), tools.SkillRef{Name: e2eSkillName}) }()

	// 附属脚本：复用草稿里那两份（它们已在本机验证过逻辑，这里验证的是「能被沙箱跑到」）
	skillKey := path.Dir(tools.SkillPath(schema, e2eSkillName))
	for _, rel := range []string{"scripts/probe.py", "scripts/main.py"} {
		b, rerr := os.ReadFile(filepath.Join(draft, filepath.FromSlash(rel)))
		if rerr != nil {
			t.Fatalf("读草稿 %s 失败: %v", rel, rerr)
		}
		if werr := agentStorage.WriteFile(ctx, skillKey+"/"+rel, b); werr != nil {
			t.Fatalf("写技能脚本 %s 失败: %v", rel, werr)
		}
	}

	// 绑定给 main：run_skill_script 的口径是「注入给本 agent」（私有 ∪ 已绑定）
	if berr := mgr.BindAgentSkill(ctx, mainID, e2eSkillName); berr != nil {
		t.Fatalf("绑定技能失败: %v", berr)
	}
	injected, err := mgr.ListInjected(ctx)
	if err != nil {
		t.Fatalf("ListInjected 失败: %v", err)
	}
	found := false
	for i := range injected {
		if injected[i].Name == e2eSkillName {
			found = true
			t.Logf("ListInjected 命中: name=%s scope=%s id=%s", injected[i].Name, injected[i].Scope, injected[i].ID)
		}
	}
	if !found {
		t.Fatalf("技能 %s 不在注入清单里，run_skill_script 会拒绝", e2eSkillName)
	}

	// ---- 4. 组装真实 Context：照 agent_impl.go 的构造，去掉 LLM 那一段 ----
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
		t.Fatal("沙箱未注入：sandbox.url 是否配好、sandboxd 是否在跑？")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 别在开发用的 data 目录里留垃圾：workDir 是 users/{uid}/runs/{runID}，往上两级一并清掉
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(filepath.Dir(workDir))) })

	t.Logf("工作区: %s", workDir)

	// ---- 5. 第一跑 probe.py：它会把挂载点列出来，直接证明 /skill 真的挂进去了 ----
	runSkill(t, c, "probe.py", map[string]any{
		"skill":  e2eSkillName,
		"script": "scripts/probe.py",
	})

	// ---- 6. 第二跑 main.py：真实数据通路（读 xlsx → 处理 → 写 out/ → 自检）----
	inPath := filepath.Join(workDir, "in.xlsx")
	genSampleXlsx(t, inPath)

	res := runSkill(t, c, "main.py", map[string]any{
		"skill": e2eSkillName,
		"args":  []any{"--input", "in.xlsx", "--min-amount", "1000"},
	})
	if res["exit_code"] != 0 {
		t.Fatalf("main.py 非零退出: exit_code=%v stderr=%s", res["exit_code"], res["stderr"])
	}

	outXlsx := filepath.Join(workDir, "out", "out.xlsx")
	st, serr := os.Stat(outXlsx)
	if serr != nil || st.Size() == 0 {
		t.Fatalf("产物未生成或为空: %v", serr)
	}
	t.Logf("产物: %s (%d bytes)", outXlsx, st.Size())
	t.Logf("产物内容: %s", dumpXlsx(t, outXlsx))
}

// runSkill 走真实调度入口 ExecuteTool —— 与 agent 循环内部调工具是同一个口子
// （含身份硬闸、必填参数校验、超时包装）。
func runSkill(t *testing.T, c *tools.Context, label string, args map[string]any) map[string]any {
	t.Helper()
	out, err := tools.ExecuteTool(context.Background(), c, "run_skill_script", args)
	if err != nil {
		t.Fatalf("%s: ExecuteTool 失败: %v", label, err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("%s: 返回类型不符: %T", label, out)
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
		t.Fatalf("造样本 xlsx 失败: %v\n%s", err, out)
	}
}

func dumpXlsx(t *testing.T, p string) string {
	t.Helper()
	out, err := exec.Command("python", "-c", dumpRows, p).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(读取失败: %v %s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}
