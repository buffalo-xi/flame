package tools

import (
	"context"
	"errors"
	"os"
	"strings"

	"flame/tools/sandbox"
)

// shellTool is an interactive shell session: several calls within the same session/run reuse one process, so state such as
// cd / export / variables / background jobs survives across calls.
//
// The split with exec: exec starts a new process per command (stateless); shell reuses a long-lived process (stateful).
type shellTool struct{}

func (t *shellTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "shell",
		Category: CategoryShell, // its own group: a resident process and a one-shot command have different resource shapes
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

	// Session identity: SessionID first, RunID as the fallback (RunID is never empty; see the Context construction in
	// agent_impl.go).
	// Key runs through sandboxg's sticky routing and sandboxd's process reuse.
	sess := sandbox.InteractiveSession{Key: c.SessionID}
	if sess.Key == "" {
		sess.Key = c.RunID
		sess.Ephemeral = true // no session = temporary: a short timeout as a backstop plus explicit destruction when the run ends
	}

	// kill: close the session explicitly
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

	// The host-side workspace must exist (a precondition for the bind mount)
	hostRoot := c.Workspace
	if hostRoot == "" {
		hostRoot = "."
	}
	if err := os.MkdirAll(hostRoot, 0o755); err != nil {
		return nil, err
	}

	// Mounts identical to exec: the workspace (rw, cwd) + this agent's definition domain (ro)
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

	// The client hands the output back as a string built from raw bytes, so the same console code page decode applies here
	// (see decodeConsoleOutput): a resident cmd session writes dir output in the OEM code page just like a one-shot command.
	out := map[string]any{
		"stdout":    decodeConsoleOutput([]byte(res.Stdout)),
		"exit_code": res.ExitCode,
		"alive":     true,
	}
	if res.Stderr != "" {
		out["stderr"] = decodeConsoleOutput([]byte(res.Stderr))
	}
	if !res.Completed {
		// No sentinel was read: the command may not have finished (partial output already returned on idle timeout)
		out["completed"] = false
	}
	return out, nil
}

// pickShellArgv is the argv of an interactive shell (long-lived, no -c; the command is fed through stdin).
// Unlike exec's pickShell, which is a one-shot `sh -c "cmd"`.
//
// An empty shell picks the default from the sandbox OS reported by capabilities; an unknown OS is an error -
// we do not guess from the nexus host's runtime.GOOS.
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
