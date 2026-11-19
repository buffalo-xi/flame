package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"flame/nexus/shared/capability"
	"flame/tools/sandbox"
)

// run_skill_script：跑技能自带的脚本。
//
// 定位：**受约束的 exec**。exec 能跑任意命令（含任意路径的脚本），本工具只能跑
// 「登记在册的技能目录内」的脚本。这条收窄是刻意的 —— 若本工具也接受任意路径，
// 它就不是收窄而是放宽，还会绕过 gate.go 的路径闸门。
//
// 它解掉两个问题：
//  1. 共享技能不可达：exec 只挂 /workspace 与 /agent，租户级技能目录
//     （tenants/{schema}/skills/）挂不进沙箱，脚本跑不了；
//  2. 抄代码：没有本工具时，模型只能 read_skill 把正文抄到工作区再跑，
//     正文与工作区副本就此分叉，技能沉淀不下来。

// skillGuestDir 技能目录在沙箱内的只读挂载点。
const skillGuestDir = "/skill"

// skillEntryScript 技能入口脚本的默认相对路径（相对技能目录）。
const skillEntryScript = "scripts/main.py"

// langByExt 脚本扩展名 → 语言名（sandboxd 顶层 langs 的 key）。
//
// 这里只给「语言名」，不给命令名：解释器入口名随 mode 而变（none 看宿主 PATH、
// nsjail 看 chroot、docker 看镜像内），而这份认知只有 sandboxd 有 —— 由它按当前
// mode 解析（见 sandboxd config.yaml 的 langs.<lang>.command / commands）。
// 注意语言名 ≠ 命令名：js 语言在镜像里的命令是 node。
var langByExt = map[string]string{
	".py":  "python",
	".js":  "js",
	".lua": "lua",
}

// interpreterByExt 扩展名 → 直传用的解释器命令（仅用于 sandboxd 没有语言定义的类型）。
//
// 目前只剩 shell：它不是双工语言，也没有需要按 mode 分辨的入口名（三种 mode 下都叫
// sh）；给它建语言定义反而会让 mode=none 的启动期校验在 Windows 上失败（宿主没有 sh）。
// 所以它维持 argv 直传，行为与改造前一致。
var interpreterByExt = map[string]string{
	".sh": "sh",
}

// skillRun 一次「跑技能脚本」所需的路径与命令（解析结果，便于单测）。
type skillRun struct {
	Skill       string // 技能名
	Rel         string // 脚本相对技能目录的路径
	StorageKey  string // 技能目录的存储相对 key（存活判定，local / s3 通用）
	HostDir     string // 技能目录的宿主绝对路径（挂载源）
	GuestScript string // 沙箱内脚本路径
	// Lang 语言名；非空 → 走语言路径，解释器入口由 sandboxd 按 mode 解析。
	Lang string
	// Interpreter 直传用的解释器命令；仅在 Lang 为空时使用（目前只有 .sh）。
	Interpreter string
}

// resolveSkillScript 解析技能与脚本，产出运行所需的路径。
//
// 可见性口径刻意比 read_skill 更紧：只认「注入给本 agent」的技能
// （私有 ∪ 已绑定，与 system prompt 常驻清单同源）。开 discovery 的 agent 能读到
// 未绑定的共享技能正文，但不该能执行它的代码 —— 可发现是读权，不是执行权。
func resolveSkillScript(ctx context.Context, c *Context, skillName, script string) (*skillRun, error) {
	name := strings.TrimSpace(skillName)
	if name == "" {
		return nil, errors.New("skill is required")
	}
	if c == nil || c.Skills == nil {
		return nil, errors.New("skill manager not initialized")
	}

	list, err := c.Skills.ListInjected(ctx)
	if err != nil {
		return nil, fmt.Errorf("list injected skills: %w", err)
	}
	var hit *SkillInfo
	for i := range list {
		if list[i].Name != name {
			continue
		}
		// 同名时私有覆盖共享（与运行时口径一致）
		if hit == nil || list[i].Scope == SkillScopeAgent {
			hit = &list[i]
		}
	}
	if hit == nil {
		return nil, fmt.Errorf("skill %q does not exist or is not bound to you; runnable set = your private skills plus skills bound to you", name)
	}

	rel := strings.TrimSpace(script)
	if rel == "" {
		rel = skillEntryScript
	}
	rel, err = safePath(rel)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, errors.New("script path is empty")
	}
	// 两种支持方式：有语言定义（交给 sandboxd 解析解释器入口），或直传解释器命令。
	ext := strings.ToLower(path.Ext(rel))
	lang, isLang := langByExt[ext]
	interp, isPassthrough := interpreterByExt[ext]
	if !isLang && !isPassthrough {
		return nil, fmt.Errorf("unsupported script type %q (currently .py / .js / .lua / .sh)", path.Ext(rel))
	}

	if c.SkillRoot == "" || c.AgentRoot == "" {
		return nil, errors.New("skill root not initialized (definition root / mount source not set)")
	}
	// 读哪一份由运行模式决定（草稿 / 当前版本），写路径不经过这里。
	// 私有技能取「归属者」—— 注入清单里的私有技能只可能是自己的。
	var ownerName string
	switch hit.Scope {
	case SkillScopeTenant:
	case SkillScopeAgent:
		ownerName = c.AgentName
	default:
		return nil, fmt.Errorf("unknown skill scope %q", hit.Scope)
	}
	key, hostDir, kerr := c.SkillReadDir(ownerName, hit.Name)
	if kerr != nil {
		return nil, kerr
	}

	// 存活判定走 storage（local / nfs / s3 通用）；挂载源另需宿主路径真实存在。
	if c.Storage != nil {
		exists, eerr := c.Storage.Exists(ctx, key)
		if eerr != nil {
			return nil, eerr
		}
		if !exists {
			return nil, fmt.Errorf("skill %q content directory does not exist (%s)", hit.Name, key)
		}
	}
	if st, serr := os.Stat(hostDir); serr != nil || !st.IsDir() {
		return nil, fmt.Errorf("skill directory is not reachable on the host: %s (sandbox mounts need file-system storage local / nfs; S3 is not supported)", hostDir)
	}

	return &skillRun{
		Skill:       hit.Name,
		Rel:         rel,
		StorageKey:  key,
		HostDir:     hostDir,
		GuestScript: path.Join(skillGuestDir, rel),
		Lang:        lang,
		Interpreter: interp,
	}, nil
}

// runSkillCustomTool 跑技能自带脚本的工具（受约束的 exec）。
//
// 归 exec 档而不是 script 档：判据是通道与进程形态，不是「跑的是不是脚本文件」。
// 它走 sb.Start → sandboxd 的 /v1/exec（一次性进程、无池、无 script_max_sec 硬闸），
// 与 exec 工具同一条单工通道；code 档是 /v1/code 通道，只归工作流 code 节点。
type runSkillCustomTool struct{}

func (t *runSkillCustomTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "run_skill_script",
		Category: CategoryExec,
		Description: "Run a script bundled with one of your bound skills (the script must live inside that skill's directory). Returns stdout, stderr and exit_code. " +
			"The skill directory is mounted read-only at /skill; the workspace is mounted read-write at /workspace (also the cwd).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill":   map[string]any{"type": "string", "description": "Skill name (must be your own private skill, or a skill bound to you)"},
				"script":  map[string]any{"type": "string", "description": "Script path relative to the skill directory; defaults to scripts/main.py"},
				"args":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Arguments passed to the script (argv)"},
				"timeout": map[string]any{"type": "number", "description": "Timeout in seconds (still bounded by Context.Timeout)"},
			},
			"required": []any{"skill"},
		},
	}
}

func (t *runSkillCustomTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	// 与 exec 同通道，准入也共用：capabilities 从未成功返回时不猜环境、直接拒绝。
	if err := ensureSandboxReady("run_skill_script"); err != nil {
		return nil, err
	}

	run, err := resolveSkillScript(ctx, c, argString(args, "skill"), argString(args, "script"))
	if err != nil {
		return nil, err
	}

	sb := c.Sandbox
	if sb == nil {
		return nil, errors.New("sandbox not initialized: command execution is disabled")
	}

	hostRoot := c.Workspace
	if hostRoot == "" {
		hostRoot = "."
	}
	if err := os.MkdirAll(hostRoot, 0o755); err != nil {
		return nil, err
	}

	execCtx := ctx
	if sec := argInt(args, "timeout"); sec > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(sec)*time.Second)
		defer cancel()
	}

	limit := c.MaxOutputLimit()
	stdout := newLimitedBuffer(limit)
	stderr := newLimitedBuffer(limit)

	scriptArgs := argStrings(args, "args")

	spec := &sandbox.Spec{
		Dir:    workspaceGuestDir,
		Env:    buildEnv(c.Env, args),
		Stderr: stderr,
		Mounts: []sandbox.Mount{
			{Host: hostRoot, Guest: workspaceGuestDir, ReadOnly: false},
			{Host: run.HostDir, Guest: skillGuestDir, ReadOnly: true},
		},
	}
	// 出网按租户许可自动决定，**不设工具参数**：脚本由技能作者提供，模型改不了，
	// 也就没有「被提示注入后临时开网」这条路径 —— 与 exec 需要模型显式请求不同。
	// Caps 为 nil（调用方没接平台策略）时不表态，交给 sandboxd 按部署默认。
	if c.Caps != nil {
		egress := c.Caps[capability.Egress]
		spec.Egress = &egress
	}
	// 两条路径二选一（见 sandbox.Spec 的说明）：
	//   - 有语言定义（.py / .js / .lua）：只报「语言 + 脚本」，解释器入口由 sandboxd
	//     按当前 mode 解析 —— nexus 不再持有「镜像里叫什么」这份认知；
	//   - 无语言定义（.sh）：argv 直传，解释器由本侧给出。
	if run.Lang != "" {
		spec.Lang = run.Lang
		spec.Script = run.GuestScript
		spec.Args = scriptArgs
	} else {
		spec.Argv = append([]string{run.Interpreter, run.GuestScript}, scriptArgs...)
	}

	proc, err := sb.Start(execCtx, spec)
	if err != nil {
		return nil, err
	}

	// 并发读 stdout（stderr 由实现直写 limitedBuffer），避免管道写满阻塞
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(stdout, proc.Stdout())
	}()

	waitErr := proc.Wait()
	<-readDone

	exitCode := 0
	if waitErr != nil {
		code, ok := exitCodeOf(waitErr)
		if !ok {
			return nil, waitErr
		}
		exitCode = code
	}

	return map[string]any{
		"skill":     run.Skill,
		"script":    run.Rel,
		"exit_code": exitCode,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"truncated": stdout.Truncated() || stderr.Truncated(),
	}, nil
}

// argStrings 解析字符串数组参数：只认数组形态，不把单个字符串按逗号切开
// —— argv 里的逗号是合法内容，误切会静默改参数。
func argStrings(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			if s, sok := it.(string); sok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func init() {
	Register(&runSkillCustomTool{})
}
