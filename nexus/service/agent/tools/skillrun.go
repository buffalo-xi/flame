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

// run_skill_script: runs a script bundled with a skill.
//
// Position: a **constrained exec**. exec can run any command (including scripts at any path), whereas this tool can only run
// scripts "inside a registered skill directory". That narrowing is deliberate - if this tool also accepted arbitrary paths it
// would be a widening rather than a narrowing, and would bypass the path gate in gate.go.
//
// It solves two problems:
//  1. shared skills are unreachable: exec mounts only /workspace and /agent, and the tenant-level skill directory
//     (tenants/{schema}/skills/) cannot be mounted into the sandbox, so its scripts cannot run;
//  2. copying code: without this tool the model can only read_skill the body, copy it into the workspace and run that, at
//     which point the body and the workspace copy diverge and the skill never accumulates.

// skillGuestDir is the read-only mount point of a skill directory inside the sandbox.
const skillGuestDir = "/skill"

// skillEntryScript is the default relative path of a skill's entry script (relative to the skill directory).
const skillEntryScript = "scripts/main.py"

// langByExt maps a script extension -> language name (the key of sandboxd's top-level langs).
//
// Only the language name is given here, never the command name: the interpreter entry point varies with mode (none uses the
// host PATH, nsjail the chroot, docker the image), and only sandboxd has that knowledge - it resolves it for the current mode
// (see langs.<lang>.command / commands in sandboxd's config.yaml).
// Note that the language name is not the command name: the js language runs as node in the image.
var langByExt = map[string]string{
	".py":  "python",
	".js":  "js",
	".lua": "lua",
}

// interpreterByExt maps an extension -> the interpreter command passed straight through (only for types sandboxd has no
// language definition for).
//
// Only shell is left: it is not a duplex language and has no mode-dependent entry name (it is sh in all three modes); giving
// it a language definition would make the mode=none startup validation fail on Windows (the host has no sh).
// So it keeps the argv pass-through, behaving exactly as before the refactor.
var interpreterByExt = map[string]string{
	".sh": "sh",
}

// skillRun holds the paths and command one "run skill script" call needs (the resolved result, for unit tests).
type skillRun struct {
	Skill       string // skill name
	Rel         string // the script's path relative to the skill directory
	StorageKey  string // the skill directory's storage-relative key (existence check, works for local / s3)
	HostDir     string // the skill directory's host absolute path (mount source)
	GuestScript string // the script's path inside the sandbox
	// Lang is the language name; non-empty -> the language path, the interpreter entry resolved by sandboxd per mode.
	Lang string
	// Interpreter is the interpreter command passed straight through; used only when Lang is empty (currently .sh only).
	Interpreter string
}

// resolveSkillScript resolves the skill and the script, producing the paths the run needs.
//
// The visibility criterion is deliberately tighter than read_skill's: only skills "injected into this agent" count
// (private ∪ bound, the same source as the resident system-prompt list). An agent with discovery on may read the body of an
// unbound shared skill, but must not be able to execute its code - being discoverable is a read right, not an execution right.
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
		// a private skill overrides a shared one of the same name (the same rule as at run time)
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
	// Two supported ways: a language definition (sandboxd resolves the interpreter entry), or an interpreter command passed
	// straight through.
	ext := strings.ToLower(path.Ext(rel))
	lang, isLang := langByExt[ext]
	interp, isPassthrough := interpreterByExt[ext]
	if !isLang && !isPassthrough {
		return nil, fmt.Errorf("unsupported script type %q (currently .py / .js / .lua / .sh)", path.Ext(rel))
	}

	if c.SkillRoot == "" || c.AgentRoot == "" {
		return nil, errors.New("skill root not initialized (definition root / mount source not set)")
	}
	// Which copy is read is decided by the run mode (draft / current version); the write path does not go through here.
	// A private skill takes "its owner" - a private skill in the injected list can only be its own.
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

	// The existence check goes through storage (works for local / nfs / s3); the mount source additionally needs a real host
	// path.
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

// runSkillCustomTool is the tool that runs scripts bundled with a skill (a constrained exec).
//
// It belongs to the exec tier rather than the script tier: the criterion is the channel and the process shape, not "whether
// what runs is a script file".
// It goes sb.Start -> sandboxd's /v1/exec (a one-shot process, no pool, no script_max_sec hard limit), the same single-shot
// channel as the exec tool; the code tier is the /v1/code channel and belongs to workflow code nodes only.
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
	// The same channel as exec, and the same admission check: when capabilities have never been returned successfully, do not
	// guess the environment - refuse outright.
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
	// Egress follows the tenant's permission automatically and **has no tool parameter**: the script comes from the skill
	// author and the model cannot change it, so there is no "open the network temporarily after a prompt injection" path -
	// unlike exec, which needs an explicit request from the model.
	// When Caps is nil (the caller does not apply platform policy) nothing is stated, and sandboxd's deployment default
	// applies.
	if c.Caps != nil {
		egress := c.Caps[capability.Egress]
		spec.Egress = &egress
	}
	// One of two paths (see the notes on sandbox.Spec):
	//   - a language definition (.py / .js / .lua): report only "language + script", and sandboxd resolves the interpreter
	//     entry for the current mode - nexus no longer holds the knowledge of "what it is called in the image";
	//   - no language definition (.sh): argv is passed straight through and the interpreter is supplied here.
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
		"skill":     run.Skill,
		"script":    run.Rel,
		"exit_code": exitCode,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"truncated": stdout.Truncated() || stderr.Truncated(),
	}, nil
}

// argStrings parses a string-array argument: array shapes only, and a single string is not split on commas
// - a comma inside argv is legitimate content, and splitting would silently change the arguments.
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
