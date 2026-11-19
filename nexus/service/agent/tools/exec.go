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

// Mount points inside the sandbox (logical paths, not host paths).
const (
	// workspaceGuestDir is this run's working ground (rw, and the command's cwd as well).
	workspaceGuestDir = "/workspace"
	// agentGuestDir is this agent's definition domain (ro): AGENT.md, private skills.
	agentGuestDir = "/agent"
)

// execTool is the cross-platform command execution tool.
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

	// Working directory: the workspace root by default; a relative path may be passed in (validated by safePath first, to
	// block traversal)
	rel := ""
	if v := strings.TrimSpace(argString(args, "cwd")); v != "" {
		r, err := safePath(v)
		if err != nil {
			return nil, err
		}
		rel = r
	}

	// Host-side directory: the mount source must really exist (a precondition for the bind mount); cwd lies inside the mount
	// point, so it has to be created up front too
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

	// Sandbox-side directory: the workspace is mapped to /workspace uniformly (the none implementation translates it back to
	// a host path)
	guestCwd := workspaceGuestDir
	if rel != "" {
		guestCwd = path.Join(workspaceGuestDir, rel)
	}

	// Timeout: wrap an extra layer only when timeout is passed explicitly, otherwise rely on ExecuteTool's Context.Timeout
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

	// Sandbox: it must be injected (nexus does no local isolation; commands are always delegated to sandboxd)
	sb := c.Sandbox
	if sb == nil {
		return nil, errors.New("sandbox not initialized: command execution is disabled")
	}
	// Egress: an explicit request plus the tenant's permission; the default is no network (fail-closed) - egress means data
	// leaves the sandbox, so the model must not get it by default. Caps being nil means the caller does not apply platform
	// policy (unrestricted, the same rule as the tool-level capability gate).
	egress := argBool(args, "egress")
	if egress && c.Caps != nil && !c.Caps[capability.Egress] {
		// The execution layer reports the error with ShortLabel (describing the capability itself), the same rule as the
		// tool-level gate.
		return nil, fmt.Errorf("the exec tool requires capability %s (%s), which this tenant does not have",
			capability.Egress, capability.ShortLabel(capability.Egress))
	}

	// Mount table: the workspace (rw, cwd) + this agent's definition domain (ro)
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
		// Stated explicitly (not nil): sandboxd decides by the deployment limit, and with network=on-request this boolean
		// decides whether the container comes up as none or bridge.
		Egress: &egress,
	}
	proc, err := sb.Start(execCtx, spec)
	if err != nil {
		return nil, err
	}

	// Read stdout concurrently (stderr is written straight into limitedBuffer by the implementation), so that a full pipe
	// does not block
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

// exitCodeOf normalises "the process exited non-zero".
//
// Key fact: the sandbox's remote implementation (FrameExit in remote.go) returns *sandbox.ExitError, and the local
// implementation is normalised to it as well by convention; *exec.ExitError only appears when a process is started bare.
// Checking only the latter has a very concrete consequence: every non-zero exit of a remote execution degrades to a bare
// "exit status 2", losing the exit code and stderr - the only two pieces of information that help during triage.
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

// shellSpec is a shell executable plus its argument prefix.
type shellSpec struct {
	bin  string
	args []string
}

// ensureSandboxReady is the shared admission check for sandbox-backed tools: refuse service when capabilities have never been
// returned successfully.
//
// "No guessing": an empty Mode means nexus does not know what environment the sandbox is (isolation kind, OS, available
// languages). Running a command then could only be a guess, so an honest "unavailable" is better than executing in an unknown
// environment.
func ensureSandboxReady(tool string) error {
	if !sandboxinfo.Ready() {
		return fmt.Errorf("%s disabled: sandbox capabilities unavailable (sandboxd unreachable or probe failed)", tool)
	}
	return nil
}

// pickShell chooses the shell by name or by the sandbox environment.
//
// An empty shell picks the default from the sandbox OS reported by capabilities; an unknown OS is an error -
// we do not guess from the nexus host's runtime.GOOS (the sandbox may be on another platform).
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

// sandboxShellOS is the sandbox's operating system (from the OS line of capabilities); empty when unknown.
//
// runtime.GOOS is not used: that is the nexus host's OS and has nothing to do with the sandbox.
func sandboxShellOS() string {
	return sandboxinfo.OS()
}

// buildEnv inherits the system environment variables and layers Context.Env and args.env on top.
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
