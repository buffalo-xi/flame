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

// Custom tools (built by the tenant, table {schema}.custom_tools).
//
// Position: **a function with fixed logic** - the model passes arguments, the script computes a result, a structured JSON comes
// back.
//
// Only two directories are mounted inside the sandbox: /tool (this tool's directory, read-only) and /workspace (this run's
// workspace, writable).
// The boundary is still "one function", not another shell:
//   - the workspace is scratch only; nothing is guaranteed to survive across calls (see "what you may rely on / must not rely
//     on" in SKILL.md);
//   - to read or write user files use the file tools (read_file / write_file), which have the path gate;
//   - to run arbitrary commands use exec (it has the capability gate and the egress switch).
//
// The difference from a plugin: a plugin is a disk-discovered implementation (one codebase serving every tenant), while a
// custom tool is the tenant's own code (only that tenant has it). Their grant refs differ too (plugin: / custom:).
const (
	// CustomToolPrefix is the outward tool-name prefix. The prefix keeps names from colliding with plugin tools (no prefix)
	// and built-in tools - tool names are global in the model context.
	CustomToolPrefix = "custom__"
	// scriptGuestDir is the read-only mount point of the tool directory inside the sandbox.
	scriptGuestDir = "/tool"
)

// CustomToolSpec is the definition one execution needs (assembled from the database by the layer above; the tools package
// never touches the database).
type CustomToolSpec struct {
	Name string
	// Status is the edit state (models.StatusXxx). The execution path does not look at it (the source is already filtered to
	// status=1); only the meta tool list_custom_tools needs it: the super agent has to know which tools are locked and cannot
	// be changed.
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
	// RequiredConfigs are the required config keys (required in config_fields).
	//
	// They are not enforced at the catalog layer: a missing key is a **configuration problem**, not a permission problem - the
	// tool still shows up in the grant catalog, and the call reports an explicit "missing config", which is easier to triage
	// than hiding it from the catalog.
	RequiredConfigs []string
}

// CustomToolSource is the manifest and config source of custom tools (injected by service/agent).
//
// It is an interface rather than a direct repository: the tools package should only know "which tools exist, how to run them",
// not the table layout, tenant resolution or masking rules.
type CustomToolSource interface {
	// List lists the custom tools available to this tenant (status=1).
	List(ctx context.Context) ([]CustomToolSpec, error)
	// Get fetches one by **short name**; missing returns (nil, nil).
	Get(ctx context.Context, name string) (*CustomToolSpec, error)
	// Status returns a tool's edit state (the file gate uses it to decide "is this entity writable").
	// Missing returns (0,false,nil) - "does not exist" and "exists but locked" must stay distinguishable.
	Status(ctx context.Context, name string) (int, bool, error)
}

// IsCustomToolName reports whether a tool name belongs to the custom tool namespace.
func IsCustomToolName(name string) bool { return strings.HasPrefix(name, CustomToolPrefix) }

// CustomShortName strips the prefix; a non-custom tool name is returned unchanged.
func CustomShortName(name string) string { return strings.TrimPrefix(name, CustomToolPrefix) }

// CustomToolSpecFromModel turns a table row into the execution view.
//
// It is centralised rather than written out at each call site: forgetting to carry one field (RequiredConfigs, say) shows up in
// production as "the config was filled in but has no effect" or "a missing config raises no error", which costs far more to
// triage than one extra conversion layer.
// The runtime source in service/agent and the admin-side "trial run" share it - the spec must have the same shape on both
// paths.
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

// SkeletonForLang is the minimal runnable skeleton for a new tool.
//
// The skeleton is part of the execution protocol (the stdin / stdout convention), so keeping it next to the execution layer
// prevents "the protocol changed but the generator still emits the old convention". Both the admin side and the meta tools
// take it from here.
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

// CustomToolRun is the input of one execution (decoupled from Context).
//
// Why a separate struct: the admin-side "trial run" and the model call must go through **the same** execution kernel - two
// execution paths would sooner or later diverge on error wording, isolation parameters and masking rules, and then "the trial
// run passed" would no longer mean "it runs in production"; the trial run would only create false confidence.
type CustomToolRun struct {
	Spec      CustomToolSpec
	ToolsRoot string // {root}/tenants/{schema}/tools (the host source mounted read-only at /tool)
	// ToolDir pins the tool's host directory (takes precedence over ToolsRoot/<name>).
	// A production run points at versions/tools/<name>/v<n>: a version directory is not a child of ToolsRoot, so it cannot be
	// joined together and has to be passed explicitly.
	ToolDir   string
	Workspace string            // this run's workspace (mounted writable at /workspace, and the cwd)
	Sandbox   sandbox.Sandbox   // nil = the sandbox is not initialised, report an error
	Env       map[string]string // extra environment variables
	// Timeout is this run's limit; the smaller of it and the tool's timeout_ms wins (0 = no extra limit).
	Timeout time.Duration
	// MaxOutput is the fallback output cap (used when the tool declares none; 0 = DefaultMaxOutput).
	MaxOutput int
	// Caps are the capabilities the platform enabled for this tenant (nil = the caller has no tenant semantics, so nothing is
	// gated by capability). Egress takes the two layers "declared by the tool AND enabled by the platform", the same source as
	// the catalog / execution layer.
	Caps map[string]bool
}

// CustomToolRunResult is everything one execution produced.
//
// The production path only uses Result; the admin trial run has to show Stdout / Stderr / ExitCode as they are - the model
// reads the result, the person triaging reads the diagnostics, and both kinds of information should be collected in one place
// instead of running the tool twice.
type CustomToolRunResult struct {
	Result     any    // the parsed and validated result (stdout with secrets masked)
	Stdout     string // raw output (secrets masked)
	Stderr     string // raw stderr (secrets masked)
	ExitCode   int
	Truncated  bool
	Masked     bool // whether anything secret-looking in the output was replaced
	DurationMS int64
}

// executeCustomTool runs one custom tool call (the model path).
//
// Protocol (fixed, no configuration option):
//
//	stdin  : {"args": {...the arguments the model passed...}, "config": {...tool config...}}
//	stdout : one JSON document (the result itself); logs / debugging always go to stderr
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

	// Draft mode = ToolsRoot/<name>; production run = versions/tools/<name>/v<n>
	// (with no released version this returns ErrNoReleasedVersion, it never falls back to the draft).
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

// requireExecCapability is the custom tool's channel gate.
//
// It belongs to the **exec tier** rather than the code tier: the criterion is the **channel**, not "whether what runs is a
// script file" - a custom tool goes sb.Start -> /v1/exec (a one-shot process, no pool), the same rule as run_skill_script
// (see the tier notes in capability.go and CapabilityOf in tool.go).
// sandbox.code gates the /v1/code channel (workflow code nodes) and has nothing to do with this: custom tools have no pool and
// no script_max_sec, so putting them in that tier would only make administrators flip the wrong switch.
//
// The admin trial run and the model call share this function: a trial run is not a back door around platform policy.
func requireExecCapability(caps map[string]bool, name string) error {
	if caps == nil || caps[capability.Exec] {
		return nil
	}
	return fmt.Errorf("tool %s requires capability %s (%s), which the platform has not granted this tenant",
		name, capability.Exec, capability.ShortLabel(capability.Exec))
}

// RunCustomToolSpec runs one custom tool (shared by production calls and the admin trial run).
//
// On failure it returns the diagnostics too (stdout / stderr / exit code): a caller that wants to show them keeps res, a caller
// that only wants the error drops res - the point is to **not write the execution logic twice on two paths**.
func RunCustomToolSpec(ctx context.Context, run CustomToolRun, args map[string]any) (*CustomToolRunResult, error) {
	spec := run.Spec
	short := spec.Name
	// ToolDir wins: a production run reads the version directory (versions/tools/<name>/v<n>),
	// which is not a child of ToolsRoot and cannot be joined together.
	hostDir := strings.TrimSpace(run.ToolDir)
	if hostDir == "" {
		if strings.TrimSpace(run.ToolsRoot) == "" {
			return nil, errors.New("custom tool root is not initialized (tools dir)")
		}
		hostDir = filepath.Join(run.ToolsRoot, short)
	}
	// Required config: this is the only place that can say "which one is missing" (the script itself would just receive an
	// empty value and fail for no visible reason).
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

	// Timeout: the smaller of the tool's own limit and this run's limit wins.
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
	stderr := newLimitedBuffer(64 * 1024) // stderr is only used for errors, and is capped as well

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
	// Egress takes two layers, "declared by the tool" AND "enabled for the tenant": if the tool did not ask for egress, the
	// network stays off (the script is written by the tenant itself, but it still should not acquire a "sneaky egress"
	// capability).
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

	// Write stdin and close it immediately: the script reads its arguments under the "start once you hit EOF" convention.
	// A failed write is not returned directly - the process may already have exited, and the real error is clearer from Wait.
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
		// Checking only *exec.ExitError would miss the *sandbox.ExitError returned by the remote sandbox -
		// then every non-zero exit becomes a bare "exit status N", and the trial run shows a failure "with no reason".
		if code, ok := exitCodeOf(waitErr); ok {
			exitCode = code
		} else if execCtx.Err() == nil {
			return nil, waitErr
		}
	}

	// Diagnostics are collected first, then failure is decided: a trial run has to get the raw output showing "why it failed".
	// Both stdout and stderr are masked - a secret reaching the browser is as bad as it reaching the model context.
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

	// Timeout / cancellation: give a reason that distinguishes "the script failed by itself" from "it was cut off"
	if execCtx.Err() != nil {
		return res, fmt.Errorf("custom tool %s timed out or was interrupted: %w", short, execCtx.Err())
	}

	// A truncated structured output is broken JSON, and failing silently is harder to diagnose than an error - fail outright.
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
			m["_masked"] = true // tell the model that secret-looking content in the output was masked
		}
	}
	return res, nil
}

// validateScriptOutput does a **shallow** validation against the output schema: top-level required keys + basic types.
//
// It does not recurse: deep validation brings little (the model only reads one level) at the cost of a full walk on every
// call; and failing because a script returned one extra field would be a false alarm (forward compatibility wins).
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
			continue // a missing non-required field is not an error
		}
		if !typeMatches(want, val) {
			return fmt.Errorf("custom tool %s output field %s has the wrong type: want %s, got %T", name, key, want, val)
		}
	}
	return nil
}

// typeMatches does the basic JSON Schema type check (covering only the kinds a script produces).
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
	return true // unknown types are not blocked
}

// maskSecrets replaces config values (length >= 8) in the output with a mask.
//
// Only values long enough are masked: a short value (such as "cn") would damage ordinary text.
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
