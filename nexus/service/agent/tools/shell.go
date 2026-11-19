package tools

import (
	"context"
	"errors"
	"os"
	"strings"

	"flame/tools/sandbox"
)

// shellTool 交互式 shell 会话：同一 session/run 内多次调用复用同一进程，
// cd / export / 变量 / 后台任务等状态跨调用保留。
//
// 与 exec 的分工：exec 每条命令新起进程（无状态）；shell 复用长驻进程（有状态）。
type shellTool struct{}

func (t *shellTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "shell",
		Category: CategoryShell, // 独立成组：常驻进程与一次性命令的资源形态不同
		Description: "Run commands in a persistent shell session: cwd, environment variables and background jobs survive across calls. " +
			"Command syntax depends on the sandbox (see the sandbox facts in the system prompt): bash/ls on Linux, cmd/dir on Windows — pass the shell explicitly on the first call (bash/sh/cmd/powershell). " +
			"Calls with a session reuse the process across conversations; calls without one reuse it for this task only and destroy it when the task ends. Close it with kill. " +
			"This is a long-lived process: use it only when state has to survive between commands (cd / export / background jobs / interactive REPL); " +
			"for one-shot commands use exec (exits when done, no resident process). Close it with kill when it is no longer needed — with a session it stays alive across conversations.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Command to run"},
				"shell":   map[string]any{"type": "string", "description": "Shell used when the session is created: bash/sh/cmd/powershell (defaults by operating system)"},
				"kill":    map[string]any{"type": "boolean", "description": "true = close the session (command is ignored)"},
			},
			"required": []any{"command"},
		},
	}
}

func (t *shellTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if err := ensureSandboxReady("shell"); err != nil {
		return nil, err
	}
	client := CurrentInteractive()
	if client == nil {
		return nil, errors.New("shell disabled: interactive sandbox not initialized")
	}

	// 会话身份：SessionID 优先，RunID 兜底（RunID 恒非空，见 agent_impl.go 的 Context 构造）。
	// Key 贯穿 sandboxg 的粘性路由与 sandboxd 的进程复用。
	sess := sandbox.InteractiveSession{Key: c.SessionID}
	if sess.Key == "" {
		sess.Key = c.RunID
		sess.Ephemeral = true // 无 session = 临时：短超时兜底 + run 结束显式销毁
	}

	// kill：显式关闭会话
	if argBool(args, "kill") {
		if err := client.Kill(ctx, sess, "shell"); err != nil {
			return nil, err
		}
		return map[string]any{"alive": false}, nil
	}

	command := strings.TrimSpace(argString(args, "command"))
	if command == "" {
		return nil, errors.New("command is required")
	}

	// 宿主侧工作区必须存在（bind mount 前提）
	hostRoot := c.Workspace
	if hostRoot == "" {
		hostRoot = "."
	}
	if err := os.MkdirAll(hostRoot, 0o755); err != nil {
		return nil, err
	}

	// 挂载与 exec 完全一致：工作区（rw、cwd）+ 本 agent 定义域（ro）
	mounts := []sandbox.Mount{
		{Host: hostRoot, Guest: workspaceGuestDir, ReadOnly: false},
	}
	if c.AgentRoot != "" {
		mounts = append(mounts, sandbox.Mount{Host: c.AgentRoot, Guest: agentGuestDir, ReadOnly: true})
	}

	argv, err := pickShellArgv(argString(args, "shell"))
	if err != nil {
		return nil, err
	}
	res, err := client.Exec(ctx, sess, "shell", argv, command, mounts, workspaceGuestDir)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"stdout":    res.Stdout,
		"exit_code": res.ExitCode,
		"alive":     true,
	}
	if res.Stderr != "" {
		out["stderr"] = res.Stderr
	}
	if !res.Completed {
		// 没读到哨兵：命令可能尚未结束（已按空闲超时返回部分输出）
		out["completed"] = false
	}
	return out, nil
}

// pickShellArgv 交互式 shell 的 argv（长驻，不带 -c；命令经 stdin 喂入）。
// 与 exec 的 pickShell 不同：那个是 `sh -c "cmd"` 一次性。
//
// shell 为空时按 capabilities 探测到的沙箱 OS 选默认值；OS 未知则报错 ——
// 不拿 nexus 宿主的 runtime.GOOS 去猜。
func pickShellArgv(shell string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "cmd":
		return []string{"cmd"}, nil
	case "powershell", "pwsh":
		return []string{"powershell", "-NoLogo"}, nil
	case "sh":
		return []string{"/bin/sh"}, nil
	case "bash":
		return []string{"/bin/bash"}, nil
	default:
		switch sandboxShellOS() {
		case "windows":
			return []string{"cmd"}, nil
		case "linux":
			return []string{"/bin/bash"}, nil
		default:
			return nil, errors.New("shell: cannot determine sandbox OS from capabilities; pass the shell parameter explicitly (bash/sh/cmd/powershell)")
		}
	}
}

func init() {
	Register(&shellTool{})
}
