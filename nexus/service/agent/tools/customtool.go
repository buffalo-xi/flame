package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/models"
	"flame/tools/sandbox"
)

// 自定义工具（租户自建，表 {schema}.custom_tools）。
//
// 定位：**固定逻辑的函数**——模型给参数、脚本算结果、返回结构化 JSON。
//
// 沙箱内只挂两个目录：/tool（本工具目录，只读）与 /workspace（本次运行的工作区，可写）。
// 边界仍是「一个函数」，不是另一个 shell：
//   - 工作区只当临时落地，跨调用不保证还在（见 SKILL.md 的「可以依赖 / 不要依赖」）；
//   - 要读写用户文件请用文件工具（read_file / write_file），它们有路径闸门；
//   - 要跑任意命令请用 exec（它有能力闸 + 出网开关）。
//
// 与插件的差别：插件是磁盘发现的实现（一份代码服务所有租户），自定义工具是
// 租户自己的代码（只有这个租户有）。两者的授权 ref 也不同（plugin: / custom:）。
const (
	// CustomToolPrefix 对外工具名前缀。加前缀是为了不和插件工具（无前缀）、
	// 内置工具撞名 —— 工具名在模型上下文里是全局的。
	CustomToolPrefix = "custom__"
	// scriptGuestDir 工具目录在沙箱内的只读挂载点。
	scriptGuestDir = "/tool"
)

// CustomToolSpec 一次执行所需的定义（由上层从 DB 组装，tools 包不碰库）。
type CustomToolSpec struct {
	Name string
	// Status 编辑态（models.StatusXxx）。执行路径不看它（来源已按 status=1 过滤），
	// 只有元工具 list_custom_tools 需要：超级智能体得知道哪些工具被锁了、不能改。
	Status      int
	Description string
	Parameters  map[string]any
	Output      map[string]any
	Lang        string
	Entry       string
	TimeoutMS   int
	MaxOutput   int
	Egress      bool
	Configs     map[string]any
	// RequiredConfigs 必填配置键（来自 config_fields 的 required）。
	//
	// 不在目录层拦：缺 key 是**配置问题**，不是权限问题 —— 工具照常出现在
	// 授权目录里，调用时才给一句明确的「缺配置」，比在目录里藏起来好排查。
	RequiredConfigs []string
}

// CustomToolSource 自定义工具的清单与配置来源（由 service/agent 注入）。
//
// 抽成接口而不是直连仓储：tools 包只该知道「有哪些工具、怎么跑」，
// 不该知道表结构、租户解析、打码规则。
type CustomToolSource interface {
	// List 列出本租户可用的自定义工具（status=1）。
	List(ctx context.Context) ([]CustomToolSpec, error)
	// Get 按**短名**取一个；不存在返回 (nil, nil)。
	Get(ctx context.Context, name string) (*CustomToolSpec, error)
	// Status 返回某工具的编辑态（文件闸用它判「实体可不可写」）。
	// 不存在返回 (0,false,nil) —— 「不存在」与「存在但锁定」必须能分开。
	Status(ctx context.Context, name string) (int, bool, error)
}

// IsCustomToolName 判断某工具名是否属于自定义工具命名空间。
func IsCustomToolName(name string) bool { return strings.HasPrefix(name, CustomToolPrefix) }

// CustomShortName 去掉前缀；非自定义工具名原样返回。
func CustomShortName(name string) string { return strings.TrimPrefix(name, CustomToolPrefix) }

// CustomToolSpecFromModel 把表行翻成执行视图。
//
// 集中一处而不是各调用方各写一份：漏搬一个字段（比如 RequiredConfigs）在线上表现为
// 「填了配置却没生效」或「缺配置不报错」，排查成本远高于多一层转换。
// service/agent 的运行时来源与管理端的「试跑」共用它 —— 两条路径的 spec 必须同形。
func CustomToolSpecFromModel(t *models.CustomTool) CustomToolSpec {
	var required []string
	for _, f := range t.ConfigFieldList() {
		if f.Required {
			required = append(required, f.Key)
		}
	}
	return CustomToolSpec{
		Name:            t.Name,
		Status:          t.Status,
		Description:     t.Description,
		Parameters:      t.ParamSchema(),
		Output:          t.OutputSchema(),
		Lang:            t.Lang,
		Entry:           t.Entry,
		TimeoutMS:       t.TimeoutMS,
		MaxOutput:       t.MaxOutputBytes,
		Egress:          t.Egress,
		Configs:         t.ConfigMap(),
		RequiredConfigs: required,
	}
}

// SkeletonForLang 新建工具时的最小可跑骨架。
//
// 骨架是执行协议的一部分（stdin / stdout 约定），放在执行层旁边才不会出现
// 「协议改了、生成器还在生成旧约定」。管理端与元工具都从这里取。
func SkeletonForLang(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "js", "node":
		return "// stdin: {\"args\": {...}, \"config\": {...}}\n// stdout: a JSON object (the result itself); write logs to stderr\nprocess.stdin.resume();\n"
	case "lua":
		return "-- stdin: {\"args\": {...}, \"config\": {...}}\n-- stdout: a JSON object (the result itself); write logs to stderr\n"
	case "sh", "shell":
		return "#!/bin/sh\n# stdin: {\"args\": {...}, \"config\": {...}}\n# stdout: a JSON object (the result itself); write logs to stderr\n"
	}
	return "# stdin: {\"args\": {...}, \"config\": {...}}\n# stdout: a JSON object (the result itself); write logs to stderr\nimport sys\n\n\ndef main():\n    raise SystemExit(\"TODO: implement main()\")\n\n\nif __name__ == \"__main__\":\n    main()\n"
}

// CustomToolRun 一次执行的输入（与 Context 解耦）。
//
// 为什么单独一个结构：管理端的「试跑」与模型调用必须走**同一条**执行内核 ——
// 两套执行路径迟早会在错误文案、隔离参数、遮蔽规则上分叉，那时「试跑通过」
// 就不再等于「线上能跑」，试跑反而制造虚假的信心。
type CustomToolRun struct {
	Spec      CustomToolSpec
	ToolsRoot string // {root}/tenants/{schema}/tools（沙箱只读挂 /tool 的宿主源）
	// ToolDir 直接指定该工具的宿主目录（优先于 ToolsRoot/<name>）。
	// 正式运行指向 versions/tools/<name>/v<n>：版本目录不是 ToolsRoot 的子目录，
	// 拼不出来，只能显式给。
	ToolDir   string
	Workspace string            // 本次运行的工作区（沙箱可写挂 /workspace，同时是 cwd）
	Sandbox   sandbox.Sandbox   // nil = 沙箱未初始化，直接报错
	Env       map[string]string // 附加环境变量
	// Timeout 本次运行上限；与工具 timeout_ms 取小（0 = 不额外限制）。
	Timeout time.Duration
	// MaxOutput 兜底输出上限（工具没声明时用；0 = DefaultMaxOutput）。
	MaxOutput int
	// Caps 平台给本租户开通的能力（nil = 调用方没有租户语义，不按能力拦）。
	// 出网取「工具声明 ∧ 平台开通」两层，与目录/执行层同源。
	Caps map[string]bool
}

// CustomToolRunResult 一次执行的全部产出。
//
// 线上路径只用 Result；管理端试跑要把 Stdout / Stderr / ExitCode 原样展示 ——
// 模型看结果、排查的人看诊断，两类信息本来就该在同一处采集，而不是各跑一遍。
type CustomToolRunResult struct {
	Result     any    // 解析并校验过的结果（stdout 已按密钥遮蔽）
	Stdout     string // 原始输出（已遮蔽密钥）
	Stderr     string // 原始 stderr（已遮蔽密钥）
	ExitCode   int
	Truncated  bool
	Masked     bool // 输出里疑似密钥的内容是否被替换
	DurationMS int64
}

// executeCustomTool 执行一次自定义工具调用（模型路径）。
//
// 协议（固定，不提供配置项）：
//
//	stdin  : {"args": {...模型传的参数...}, "config": {...工具配置...}}
//	stdout : 一段 JSON（结果本体），日志/调试一律走 stderr
func executeCustomTool(ctx context.Context, c *Context, name string, args map[string]any) (any, error) {
	if c.CustomTools == nil {
		return nil, fmt.Errorf("%w: %s (custom tools are not enabled)", ErrToolNotFound, name)
	}
	if err := requireExecCapability(c.Caps, name); err != nil {
		return nil, err
	}

	short := CustomShortName(name)
	spec, err := c.CustomTools.Get(ctx, short)
	if err != nil {
		return nil, err
	}
	if spec == nil {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}

	// 草稿模式 = ToolsRoot/<name>；正式运行 = versions/tools/<name>/v<n>
	// （没有已发布版本时这里直接返回 ErrNoReleasedVersion，不会退回草稿）。
	toolDir, terr := c.ToolDir(spec.Name)
	if terr != nil {
		return nil, terr
	}

	res, err := RunCustomToolSpec(ctx, CustomToolRun{
		Spec:      *spec,
		ToolsRoot: c.ToolsRoot,
		ToolDir:   toolDir,
		Workspace: c.Workspace,
		Sandbox:   c.Sandbox,
		Env:       c.Env,
		Timeout:   c.Timeout,
		MaxOutput: c.MaxOutput,
		Caps:      c.Caps,
	}, args)
	if err != nil {
		return nil, err
	}
	return res.Result, nil
}

// requireExecCapability 自定义工具的通道闸。
//
// 归 **exec 档**而不是 code 档：判据是**通道**而不是「跑的是不是脚本文件」——
// 自定义工具走 sb.Start → /v1/exec（一次性进程、无池），与 run_skill_script 同口径
// （见 capability.go 的档位注释与 tool.go 的 CapabilityOf）。
// sandbox.code 是 /v1/code 通道（工作流 code 节点）的闸，与这里无关：自定义工具
// 没有池、没有 script_max_sec，归到那一档只会让管理员关错开关。
//
// 管理端试跑与模型调用共用本函数：试跑不是绕过平台策略的后门。
func requireExecCapability(caps map[string]bool, name string) error {
	if caps == nil || caps[capability.Exec] {
		return nil
	}
	return fmt.Errorf("tool %s requires capability %s (%s), which the platform has not granted this tenant",
		name, capability.Exec, capability.ShortLabel(capability.Exec))
}

// RunCustomToolSpec 执行一次自定义工具（线上调用与管理端试跑共用）。
//
// 失败时同时返回诊断信息（stdout / stderr / 退出码）：调用方要展示就把 res 一起用，
// 只要错误就丢掉 res —— 关键在于**不在两条路径上各写一份执行逻辑**。
func RunCustomToolSpec(ctx context.Context, run CustomToolRun, args map[string]any) (*CustomToolRunResult, error) {
	spec := run.Spec
	short := spec.Name
	// ToolDir 优先：正式运行读的是版本目录（versions/tools/<name>/v<n>），
	// 它不是 ToolsRoot 的子目录，靠 Join 拼不出来。
	hostDir := strings.TrimSpace(run.ToolDir)
	if hostDir == "" {
		if strings.TrimSpace(run.ToolsRoot) == "" {
			return nil, errors.New("custom tool root is not initialized (tools dir)")
		}
		hostDir = filepath.Join(run.ToolsRoot, short)
	}
	// 必填配置：这里是唯一能给出「缺哪个」的地方（脚本自己只会拿到空值然后莫名失败）。
	for _, key := range spec.RequiredConfigs {
		if v, ok := spec.Configs[key]; !ok || strings.TrimSpace(fmt.Sprint(v)) == "" {
			return nil, fmt.Errorf("custom tool %s is missing the required config %s (fill it in on the admin console custom tool page)", short, key)
		}
	}

	if st, serr := os.Stat(hostDir); serr != nil || !st.IsDir() {
		return nil, fmt.Errorf("custom tool directory is not reachable on the host: %s", hostDir)
	}
	entry := strings.TrimSpace(spec.Entry)
	if entry == "" {
		entry = "main.py"
	}
	entry, err := safePath(entry)
	if err != nil {
		return nil, err
	}

	sb := run.Sandbox
	if sb == nil {
		return nil, errors.New("sandbox not initialized: custom tools are unavailable")
	}

	// 超时：工具自身上限与本次运行上限取小。
	execCtx := ctx
	timeout := time.Duration(spec.TimeoutMS) * time.Millisecond
	if run.Timeout > 0 && (timeout <= 0 || run.Timeout < timeout) {
		timeout = run.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	limit := spec.MaxOutput
	if limit <= 0 {
		limit = run.MaxOutput
	}
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	stdout := newLimitedBuffer(limit)
	stderr := newLimitedBuffer(64 * 1024) // stderr 只用于报错，同样限长

	payload, err := json.Marshal(map[string]any{"args": args, "config": spec.Configs})
	if err != nil {
		return nil, fmt.Errorf("failed to serialize arguments: %w", err)
	}

	workspace := run.Workspace
	if workspace == "" {
		workspace = "."
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, err
	}

	specIn := &sandbox.Spec{
		Dir:    workspaceGuestDir,
		Env:    buildEnv(run.Env, nil),
		Stderr: stderr,
		Mounts: []sandbox.Mount{
			{Host: workspace, Guest: workspaceGuestDir, ReadOnly: false},
			{Host: hostDir, Guest: scriptGuestDir, ReadOnly: true},
		},
	}
	// 出网按工具声明 + 租户能力两层：工具没声明要出网，一律断网
	// （脚本是租户自己写的，但仍不该拿到「偷偷出网」的能力）。
	if run.Caps != nil {
		egress := spec.Egress && run.Caps[capability.Egress]
		specIn.Egress = &egress
	}

	guestScript := path.Join(scriptGuestDir, entry)
	switch spec.Lang {
	case "", "sh":
		specIn.Argv = []string{"sh", guestScript}
	default:
		specIn.Lang = spec.Lang
		specIn.Script = guestScript
	}

	start := time.Now()
	proc, err := sb.Start(execCtx, specIn)
	if err != nil {
		return nil, err
	}

	// 写 stdin 后立刻关闭：脚本按「读到 EOF 就开跑」的约定读参数。
	// 写入失败不直接返回 —— 进程可能已经退出，真正的错误在 Wait 里更清楚。
	if stdin := proc.Stdin(); stdin != nil {
		_, _ = stdin.Write(payload)
		_ = stdin.Close()
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(stdout, proc.Stdout())
	}()

	waitErr := proc.Wait()
	<-readDone
	elapsed := time.Since(start)

	exitCode := 0
	if waitErr != nil {
		// 只判 *exec.ExitError 会漏掉远端沙箱返回的 *sandbox.ExitError——
		// 那样每个非 0 退出都变成裸的 "exit status N"，试跑看到的就是「没有原因」的失败。
		if code, ok := exitCodeOf(waitErr); ok {
			exitCode = code
		} else if execCtx.Err() == nil {
			return nil, waitErr
		}
	}

	// 诊断先落地，再判失败：试跑要拿得到「为什么失败」的原始输出。
	// stdout / stderr 都过遮蔽 —— 密钥进浏览器与进模型上下文一样不该发生。
	out, maskedOut := maskSecrets(stdout.String(), spec.Configs)
	errText, maskedErr := maskSecrets(strings.TrimSpace(stderr.String()), spec.Configs)
	res := &CustomToolRunResult{
		Stdout:     out,
		Stderr:     errText,
		ExitCode:   exitCode,
		Truncated:  stdout.Truncated(),
		Masked:     maskedOut || maskedErr,
		DurationMS: elapsed.Milliseconds(),
	}

	// 超时 / 取消：给一句能区分「脚本自己失败」与「被掐断」的原因
	if execCtx.Err() != nil {
		return res, fmt.Errorf("custom tool %s timed out or was interrupted: %w", short, execCtx.Err())
	}

	// 结构化输出截断后就是坏 JSON，静默失败比报错难查 —— 直接判失败。
	if stdout.Truncated() {
		return res, fmt.Errorf("custom tool %s output exceeds the %d byte limit and was aborted (paginate inside the script or return a summary)", short, limit)
	}

	if exitCode != 0 {
		return res, fmt.Errorf("custom tool %s exited with code %d: %s", short, exitCode, errText)
	}

	var result any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		snippet := out
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return res, fmt.Errorf("custom tool %s wrote non-JSON to stdout (convention: result on stdout, logs on stderr): %s",
			short, strings.TrimSpace(snippet))
	}
	res.Result = result

	if spec.Output != nil {
		if err := validateScriptOutput(short, spec.Output, result); err != nil {
			return res, err
		}
	}
	if res.Masked {
		if m, ok := result.(map[string]any); ok {
			m["_masked"] = true // 告诉模型：输出里疑似密钥的内容已被遮蔽
		}
	}
	return res, nil
}

// validateScriptOutput 按 output schema 做**浅校验**：顶层 required 键 + 基础类型。
//
// 不递归：深校验的收益低（模型只读一层），成本是给每次调用加一次遍历；
// 而且脚本多返回一个字段就报错会误伤（前向兼容优先）。
func validateScriptOutput(name string, schema map[string]any, result any) error {
	obj, ok := result.(map[string]any)
	if !ok {
		return fmt.Errorf("custom tool %s output must be a JSON object, got %T", name, result)
	}

	if required, ok := schema["required"].([]any); ok {
		var missing []string
		for _, r := range required {
			key, _ := r.(string)
			if key == "" {
				continue
			}
			if _, exists := obj[key]; !exists {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("custom tool %s output is missing fields %v", name, missing)
		}
	}

	props, _ := schema["properties"].(map[string]any)
	for key, raw := range props {
		prop, _ := raw.(map[string]any)
		want, _ := prop["type"].(string)
		if want == "" {
			continue
		}
		val, exists := obj[key]
		if !exists {
			continue // 缺的非必填字段不报错
		}
		if !typeMatches(want, val) {
			return fmt.Errorf("custom tool %s output field %s has the wrong type: want %s, got %T", name, key, want, val)
		}
	}
	return nil
}

// typeMatches JSON Schema 基础类型判定（只覆盖脚本会产出的几种）。
func typeMatches(want string, v any) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "null":
		return v == nil
	}
	return true // 未知类型不拦
}

// maskSecrets 把配置值（长度 ≥ 8）在输出里替换成遮蔽串。
//
// 只遮蔽够长的值：太短的值（如 "cn"）会误伤正常文本。
func maskSecrets(out string, configs map[string]any) (string, bool) {
	if len(configs) == 0 || out == "" {
		return out, false
	}
	masked := false
	for _, raw := range configs {
		s, ok := raw.(string)
		if !ok || len(s) < 8 {
			continue
		}
		if strings.Contains(out, s) {
			out = strings.ReplaceAll(out, s, "••••")
			masked = true
		}
	}
	return out, masked
}
