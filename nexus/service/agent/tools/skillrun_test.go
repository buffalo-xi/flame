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

// stubSkillsInjected 只实现 ListInjected —— resolveSkillScript 的可见性口径就卡在它身上。
// 其余方法由嵌入接口兜底（被调用即 panic），避免用例悄悄依赖未桩行为。
type stubSkillsInjected struct {
	SkillsManager
	injected []SkillInfo
}

func (s stubSkillsInjected) ListInjected(context.Context) ([]SkillInfo, error) {
	return s.injected, nil
}

// stubSandbox 捕获 Spec 不真跑进程；wait 决定 Wait 的返回。
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

// newSkillRunCtx 造一个盘上真实存在的最小 Context：
// SkillRoot / AgentRoot 落在临时目录里，Storage 的根与之对齐 —— 存储 key 与宿主路径
// 必须指向同一个位置，否则「storage 说存在、os.Stat 说不存在」这类假阳性会被掩盖。
func newSkillRunCtx(t *testing.T, injected []SkillInfo) *Context {
	t.Helper()
	// Execute 的准入（ensureSandboxReady）要求至少成功拉到过一次能力快照，
	// 单测里没有 sandboxd，这里注入一份最小快照把准入放行。
	// 注意这是进程级全局状态（sandboxinfo 无「恢复未配置」的公开 API）：
	// 同包内不要再写断言 Ready() == false 的用例。
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

// 工具必须登记在册且归入 exec 分类 —— 分类是能力闸与授权目录的唯一判据。
//
// 判据是通道而非「跑的是不是脚本文件」：run_skill_script 走 sb.Start → /v1/exec，
// 与 exec 工具同一条单工通道（一次性进程、无池、无 script_max_sec 硬闸）；
// sandbox.code 是 /v1/code 通道，只归工作流 code 节点（代码 / handler / 后处理）。
// 两者都能执行任意代码，分类不是权限边界，只是按形态授权资源。
func TestRunSkillScriptRegisteredInExecCategory(t *testing.T) {
	tool, ok := Get("run_skill_script")
	if !ok {
		t.Fatal("run_skill_script 未登记")
	}
	if got := tool.Meta().Category; got != CategoryExec {
		t.Errorf("Category = %q, want %q", got, CategoryExec)
	}
}

// 租户级共享技能：目录在 skills/ 下，存储 key 与宿主路径都要对得上。
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
	// .py 有语言定义 → 走语言路径（解释器入口交给 sandboxd 按 mode 解析）
	if run.Lang != "python" {
		t.Errorf("Lang = %q, want python", run.Lang)
	}
	if run.Interpreter != "" {
		t.Errorf("语言路径下不该带直传解释器，Interpreter = %q", run.Interpreter)
	}
}

// shell 没有语言定义（也不是双工语言），必须退回 argv 直传 —— 行为与改造前一致。
func TestResolveSkillScriptShellPassthrough(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "sheller", Scope: SkillScopeTenant}})
	dir := filepath.Join(c.SkillRoot, "sheller")
	mkSkillScript(t, dir, "scripts/main.sh")

	run, err := resolveSkillScript(context.Background(), c, "sheller", "scripts/main.sh")
	if err != nil {
		t.Fatal(err)
	}
	if run.Lang != "" {
		t.Errorf("Lang = %q, want empty（.sh 无语言定义）", run.Lang)
	}
	if run.Interpreter != "sh" {
		t.Errorf("Interpreter = %q, want sh", run.Interpreter)
	}
}

// 语言名 ≠ 命令名：.js 交出去的是 "js"，命令名 node 由 sandboxd 决定。
func TestResolveSkillScriptJsLangNotCommand(t *testing.T) {
	c := newSkillRunCtx(t, []SkillInfo{{Name: "front", Scope: SkillScopeTenant}})
	dir := filepath.Join(c.SkillRoot, "front")
	mkSkillScript(t, dir, "scripts/main.js")

	run, err := resolveSkillScript(context.Background(), c, "front", "scripts/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if run.Lang != "js" {
		t.Errorf("Lang = %q, want js（语言名，不是命令名 node）", run.Lang)
	}
}

// agent 私有技能：目录挂在 AgentRoot/skills 下（exec 的 /agent 挂载点之内）。
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

// 同名时私有覆盖共享（与运行时口径一致）。
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
		t.Errorf("StorageKey = %q，同名应取私有那条", run.StorageKey)
	}
}

// 拒绝面：未注入 / 越权路径 / 绝对路径 / 不支持的扩展名 / 技能目录不存在。
func TestResolveSkillScriptRejects(t *testing.T) {
	ok := []SkillInfo{{Name: "xlsx-report", Scope: SkillScopeTenant}}
	cases := []struct {
		name    string
		skill   string
		script  string
		created bool
	}{
		{"未注入的技能", "other", "", false},
		{"越权路径", "xlsx-report", "../../etc/passwd.py", true},
		{"绝对路径", "xlsx-report", "/etc/x.py", true},
		{"不支持的扩展名", "xlsx-report", "scripts/main.rb", true},
		{"技能目录不存在", "xlsx-report", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newSkillRunCtx(t, ok)
			if tc.created {
				mkSkillScript(t, filepath.Join(c.SkillRoot, "xlsx-report"), "scripts/main.py")
			}
			if _, err := resolveSkillScript(context.Background(), c, tc.skill, tc.script); err == nil {
				t.Fatal("应拒绝")
			}
		})
	}
}

// 挂载与启动意图是切片 2 的核心契约：技能目录只读挂进 /skill、工作区可写挂进 /workspace
// 且作为 cwd，args 原样透传，解释器入口不由本侧决定（见下方断言）。
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
		t.Fatal("sandbox Start 未被调用")
	}

	// 语言路径：本侧只报「语言 + 脚本 + 参数」，解释器入口（python / node / lua）
	// 由 sandboxd 按当前 mode 解析 —— 一旦在这里拼出 argv，就等于硬编码了
	// 「镜像里叫什么」这份只应存在于 sandboxd 的认知。
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
		t.Errorf("Argv = %v，语言路径下必须为空（与 Lang 互斥）", sb.spec.Argv)
	}
	if sb.spec.Dir != "/workspace" {
		t.Errorf("Dir = %q, want /workspace", sb.spec.Dir)
	}
	if len(sb.spec.Mounts) != 2 {
		t.Fatalf("Mounts = %+v, 期望 2 条", sb.spec.Mounts)
	}
	if sb.spec.Mounts[0].Guest != "/workspace" || sb.spec.Mounts[0].ReadOnly {
		t.Errorf("第 1 条应为可写的 /workspace: %+v", sb.spec.Mounts[0])
	}
	if sb.spec.Mounts[1].Guest != "/skill" || !sb.spec.Mounts[1].ReadOnly || sb.spec.Mounts[1].Host != dir {
		t.Errorf("第 2 条应为只读的技能目录: %+v", sb.spec.Mounts[1])
	}

	m := out.(map[string]any)
	if m["exit_code"] != 0 {
		t.Errorf("exit_code = %v, want 0", m["exit_code"])
	}
	if m["stdout"] != "hello" {
		t.Errorf("stdout = %v, want hello", m["stdout"])
	}
	if m["skill"] != "xlsx-report" || m["script"] != "scripts/main.py" {
		t.Errorf("回显不符：%+v", m)
	}
}

// .sh 没有语言定义（sandboxd 的顶层 langs 里没有它），必须退回 argv 直传路径：
// 解释器由本侧给出、脚本路径在 /skill 下、args 追加在最后。
// 这条路径同时是「改造前后行为一致」的回归位。
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
		t.Errorf("Lang = %q，.sh 无语言定义，应为空", sb.spec.Lang)
	}
	wantArgv := []string{"sh", "/skill/scripts/main.sh", "a", "b"}
	if !reflect.DeepEqual(sb.spec.Argv, wantArgv) {
		t.Errorf("Argv = %v, want %v", sb.spec.Argv, wantArgv)
	}
}

// Wait 返回非 ExitError 时应原样上抛（不是吞掉当 exit 0）。
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
