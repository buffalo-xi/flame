package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/sandboxinfo"
	"flame/tools/sandbox"
)

// 沙箱内挂载点（逻辑路径，非宿主路径）。
const (
	// workspaceGuestDir 本次运行的工作场地（rw，同时是命令 cwd）。
	workspaceGuestDir = "/workspace"
	// agentGuestDir 本 agent 的定义域（ro）：AGENT.md、私有 skills。
	agentGuestDir = "/agent"
)

// execTool 跨平台命令执行工具。
type execTool struct{}

func (t *execTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "exec",
		Category:    CategoryExec,
		Description: "Run a shell command in the workspace (cross-platform). Returns stdout, stderr and exit_code.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Command to run"},
				"shell":   map[string]any{"type": "string", "description": "Shell type: cmd / powershell / sh / bash (defaults by operating system)"},
				"cwd":     map[string]any{"type": "string", "description": "Working directory (relative to the workspace; defaults to the workspace root)"},
				"timeout": map[string]any{"type": "number", "description": "Timeout in seconds (defaults to Context.Timeout)"},
				"egress": map[string]any{
					"type":        "boolean",
					"description": "Whether to allow outbound network access (default: no). A platform admin must have enabled egress for this tenant first, otherwise the call is rejected.",
				},
			},
			"required": []any{"command"},
		},
	}
}

func (t *execTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if err := ensureSandboxReady("exec"); err != nil {
		return nil, err
	}
	command := strings.TrimSpace(argString(args, "command"))
	if command == "" {
		return nil, errors.New("command is required")
	}

	// 工作目录：默认工作区根，可传相对路径（先经 safePath 校验，防穿越）
	rel := ""
	if v := strings.TrimSpace(argString(args, "cwd")); v != "" {
		r, err := safePath(v)
		if err != nil {
			return nil, err
		}
		rel = r
	}

	// 宿主侧目录：挂载源必须真实存在（bind mount 前提）；cwd 落在挂载点内部，也要预先建好
	hostRoot := c.Workspace
	if hostRoot == "" {
		hostRoot = "."
	}
	if err := os.MkdirAll(hostRoot, 0o755); err != nil {
		return nil, err
	}
	if rel != "" {
		if err := os.MkdirAll(filepath.Join(hostRoot, filepath.FromSlash(rel)), 0o755); err != nil {
			return nil, err
		}
	}

	// 沙箱侧目录：工作区统一映射到 /workspace（none 实现会翻译回宿主路径）
	guestCwd := workspaceGuestDir
	if rel != "" {
		guestCwd = path.Join(workspaceGuestDir, rel)
	}

	// 超时：仅当显式传 timeout 时额外包一层，否则依赖 ExecuteTool 的 Context.Timeout
	execCtx := ctx
	if sec := argInt(args, "timeout"); sec > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(sec)*time.Second)
		defer cancel()
	}

	shell, err := pickShell(argString(args, "shell"))
	if err != nil {
		return nil, err
	}

	limit := c.MaxOutputLimit()
	stdout := newLimitedBuffer(limit)
	stderr := newLimitedBuffer(limit)

	// 沙箱：必须已注入（nexus 不做本地隔离，命令一律委托给 sandboxd）
	sb := c.Sandbox
	if sb == nil {
		return nil, errors.New("sandbox not initialized: command execution is disabled")
	}
	// 出网：显式请求 + 租户许可，缺省断网（fail-closed）—— 出网意味着数据离开沙箱，
	// 不能由模型默认获得。Caps 为 nil 表示调用方没接平台策略（不限制，与工具级能力闸同口径）。
	egress := argBool(args, "egress")
	if egress && c.Caps != nil && !c.Caps[capability.Egress] {
		// 执行层报错用 ShortLabel（只描述能力本身），与工具级闸门同口径。
		return nil, fmt.Errorf("the exec tool requires capability %s (%s), which this tenant does not have",
			capability.Egress, capability.ShortLabel(capability.Egress))
	}

	// 挂载表：工作区（rw、cwd）+ 本 agent 定义域（ro）
	mounts := []sandbox.Mount{
		{Host: hostRoot, Guest: workspaceGuestDir, ReadOnly: false},
	}
	if c.AgentRoot != "" {
		mounts = append(mounts, sandbox.Mount{Host: c.AgentRoot, Guest: agentGuestDir, ReadOnly: true})
	}
	spec := &sandbox.Spec{
		Argv:   append([]string{shell.bin}, append(shell.args, command)...),
		Dir:    guestCwd,
		Env:    buildEnv(c.Env, args),
		Stderr: stderr,
		Mounts: mounts,
		// 明确表态（不是 nil）：sandboxd 会按部署上限裁决，network=on-request 时
		// 靠这个布尔决定容器起成 none 还是 bridge。
		Egress: &egress,
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
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
		"truncated": stdout.Truncated() || stderr.Truncated(),
	}, nil
}

// exitCodeOf 归一化「进程非 0 退出」。
//
// 关键事实：沙箱的远端实现（remote.go 的 FrameExit）返回的是 *sandbox.ExitError，
// 本地实现也按注释约定归一化成它；*exec.ExitError 只在裸起进程时出现。
// 只判后者的后果非常具体：远端执行的每一个非 0 退出都退化成裸的 "exit status 2"，
// 退出码、stderr 全丢 —— 而这两样正是排查时唯一有用的信息。
func exitCodeOf(err error) (int, bool) {
	var se *sandbox.ExitError
	if errors.As(err, &se) {
		return se.Code, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}

// shellSpec shell 可执行文件 + 参数前缀。
type shellSpec struct {
	bin  string
	args []string
}

// ensureSandboxReady 沙箱类工具的统一准入：capabilities 从未成功返回时拒绝服务。
//
// 「不猜」：Mode 为空意味着 nexus 不知道沙箱是什么环境（隔离方式、OS、可执行语言）。
// 此时让命令跑起来只能靠猜，宁可如实报「不可用」，也不要在未知环境里执行。
func ensureSandboxReady(tool string) error {
	if !sandboxinfo.Ready() {
		return fmt.Errorf("%s disabled: sandbox capabilities unavailable (sandboxd unreachable or probe failed)", tool)
	}
	return nil
}

// pickShell 按名称或沙箱环境选择 shell。
//
// shell 为空时按 capabilities 探测到的沙箱 OS 选默认值；OS 未知则报错 ——
// 不拿 nexus 宿主的 runtime.GOOS 去猜（沙箱可能在另一个平台）。
func pickShell(shell string) (shellSpec, error) {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "cmd":
		return shellSpec{bin: "cmd", args: []string{"/S", "/C"}}, nil
	case "powershell", "pwsh":
		return shellSpec{bin: "powershell", args: []string{"-NoProfile", "-Command"}}, nil
	case "sh":
		return shellSpec{bin: "/bin/sh", args: []string{"-c"}}, nil
	case "bash":
		return shellSpec{bin: "/bin/bash", args: []string{"-c"}}, nil
	default:
		switch sandboxShellOS() {
		case "windows":
			return shellSpec{bin: "cmd", args: []string{"/S", "/C"}}, nil
		case "linux":
			return shellSpec{bin: "/bin/sh", args: []string{"-c"}}, nil
		default:
			return shellSpec{}, errors.New("exec: cannot determine sandbox OS from capabilities; pass the shell parameter explicitly (bash/sh/cmd/powershell)")
		}
	}
}

// sandboxShellOS 沙箱的操作系统（来自 capabilities 的 `系统:` 行）；未知返回空串。
//
// 不使用 runtime.GOOS：那是 nexus 宿主的 OS，与沙箱无关。
func sandboxShellOS() string {
	return sandboxinfo.OS()
}

// buildEnv 继承系统环境变量，叠加 Context.Env 与 args.env。
func buildEnv(base map[string]string, args map[string]any) []string {
	env := os.Environ()
	for k, v := range base {
		env = append(env, k+"="+v)
	}
	if raw, ok := args["env"].(map[string]any); ok {
		for k, v := range raw {
			env = append(env, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return env
}

func init() {
	Register(&execTool{})
}
