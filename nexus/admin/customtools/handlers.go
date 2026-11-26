// Package customtools is the management API for tenant-defined custom tools.
//
// The difference from plugins (which is why this is CRUD rather than "configuration"):
//   - a plugin is discovered on disk, and the admin side can only choose which implementation is enabled and what key it uses;
//   - a custom tool is an entity **the tenant creates**: its definition, parameter schema and code all live here.
//
// Three deliberate trade-offs:
//  1. name is immutable after creation - it is already written into agent_tool_grants.ref (custom:<name>),
//     and renaming would silently break other people's grants;
//  2. secret fields are masked on the way out, and sending the mask back means "do not change" (the same convention as admin/plugins);
//  3. deleting also removes the code directory on disk - an orphan directory only confuses the next create.
package customtools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/sandboxinfo"
	"flame/nexus/shared/toolfile"
	"flame/nexus/shared/versioning"
	"flame/nexus/shared/versions"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// secretMask is the same as in admin/plugins: masked on the way out, and returning the mask means unchanged.
const secretMask = "••••"

// storeFor builds the merged view per request and schema (the directory decides, the table supplies status/configs).
// No global singleton: schema is request state, and caching one instance would read another tenant's data.
func storeFor(schema string) *customtoolstore.Store {
	return customtoolstore.New(config.Cfg.Storage.RootDir, schema)
}

func mustAuth(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	return info, true
}

// customToolView is a list item: the row + masked config values + the contract source + the current version.
type customToolView struct {
	models.CustomTool
	Configs map[string]any `json:"configs"` // shadows the embedded field: secrets are masked
	// Contract says whether the contract came from TOOL.json or fell back to the table.
	// The UI uses it to flag "not filed / file is broken", so the panel and the executor never show two definitions unnoticed.
	Contract toolfile.Contract `json:"contract"`
	// CurrentVersion is the released version (0 = not released yet). The card shows vN or "not released".
	CurrentVersion int `json:"current_version"`
}

func buildView(t *models.CustomTool, c toolfile.Contract) customToolView {
	return customToolView{CustomTool: *t, Configs: maskConfigs(t), Contract: c}
}

// maskConfigs masks values declared secret by config_fields.
func maskConfigs(t *models.CustomTool) map[string]any {
	cfg := t.ConfigMap()
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range t.ConfigFieldList() {
		if !f.Secret {
			continue
		}
		if s, ok := out[f.Key].(string); ok && s != "" {
			out[f.Key] = maskValue(s)
		}
	}
	return out
}

func maskValue(s string) string {
	if len(s) <= 4 {
		return secretMask
	}
	return secretMask + s[len(s)-4:]
}

// mergeConfigs starts from the stored config and overlays the request values (masked strings are skipped).
func mergeConfigs(stored map[string]any, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok && strings.HasPrefix(s, secretMask) {
			continue // the mask came back = unchanged
		}
		out[k] = v
	}
	return out
}

// ListCustomTools GET /customtools/
func ListCustomTools(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	list, issues, err := storeFor(info.Schema).ListWithIssues()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read custom tools: "+err.Error(), "")
		return
	}
	// Opening the list completes the backfill: a row without TOOL.json gets one written (idempotent; failures are only logged,
	// and the tool keeps working from its table definition). It sits on the admin side rather than the read path: a read that writes a file is too hidden a side effect.
	if written, errs := toolfile.EnsureAll(config.Cfg.Storage.RootDir, info.Schema, list); written > 0 || len(errs) > 0 {
		xlog.Info("custom tool contracts backfilled schema=%s written=%d errors=%d", info.Schema, written, len(errs))
		for _, e := range errs {
			xlog.Error("failed to backfill TOOL.json schema=%s: %s", info.Schema, e)
		}
	}

	// Current version numbers: one batch query (the cards show vN); a read failure does not affect the list
	curVers, _ := versioning.CurrentVersions(info.Schema, "tool_versions")

	out := make([]customToolView, 0, len(list))
	for i := range list {
		c := toolfile.Load(config.Cfg.Storage.RootDir, info.Schema, &list[i])
		v := buildView(&list[i], c)
		v.CurrentVersion = curVers[list[i].ID]
		out = append(out, v)
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"tools": out,
		// Problems at the directory level (missing TOOL.json / a broken contract / a leftover record).
		// The "directory on disk, invisible to the platform" trap is at least visible as a warning now.
		"issues": issues,
		// Sandbox facts: which languages this deployment can run. The UI annotates the language dropdown from it,
		// instead of letting a user pick lua and only then discover the image lacks it (the error surfaces on the first call).
		// It reads the sandboxd capability snapshot (an in-process cache): no DB query, no extra request.
		"sandbox": sandboxView(),
	}, "")
}

// sandboxView reports sandbox language availability (for the admin UI).
//
// ready=false means capabilities were never fetched successfully: the environment must not be guessed then,
// and the UI should honestly show "availability unknown" rather than treat all four languages as usable.
func sandboxView() gin.H {
	info := sandboxinfo.Get()
	langs := info.Langs
	if langs == nil {
		langs = []string{}
	}
	return gin.H{
		"ready": sandboxinfo.Ready(),
		"mode":  info.Mode,
		"langs": langs,
	}
}

// errToolNotFound reports a missing custom tool (code tool.not_found).
//
// The name always comes from the route parameter rather than being passed around: these handlers all route with :name,
// and the odd branch that ends up with only the entity row (a row locked by id that turns out missing) is on the same route and can still read it.
// msg is the plain fallback text; front-end behaviour is unchanged.
func errToolNotFound(c *gin.Context) {
	xhttp.ErrCode(c, "tool.not_found", map[string]any{"name": c.Param("name")}, "custom tool not found", "")
}

// GetCustomTool GET /customtools/:name
func GetCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	t, err := storeFor(info.Schema).Get(info.Schema, c.Param("name"))
	if err != nil {
		errToolNotFound(c)
		return
	}
	// Merge the contract before reading the entry file: entry may have been changed only in TOOL.json (the table still holds the old value).
	contract := toolfile.Load(config.Cfg.Storage.RootDir, info.Schema, t)
	body := gin.H{"tool": buildView(t, contract)}
	if content, cerr := readEntry(info.Schema, t); cerr == nil {
		body["script"] = content
	}
	// Version state: the editor header must distinguish "not released / released with changes / matches production" (same as flows).
	body["current_version"], body["dirty"] = 0, false
	if st := toolVerStore(); st != nil {
		v, dirty := versioning.ToolVersionState(context.Background(), st, info.Schema, t.Name)
		body["current_version"], body["dirty"] = v, dirty
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// saveReq is the create / update body. Fields map one-to-one onto the table, so nothing is "in the form but not in the database".
//
// String fields are pointers to tell two kinds of "empty" apart: nil = leave alone (on update), "" = clear.
// A value type is not used because "delete the whole description" is a legitimate action while a value type can only say "not sent",
// which shows up as "saved, but the value did not change". Same as the existing Status / Egress / Script.
type saveReq struct {
	Name           string           `json:"name"` // create only; ignored on update (a rename breaks grant refs)
	DisplayName    *string          `json:"display_name"`
	Description    *string          `json:"description"`
	Parameters     map[string]any   `json:"parameters"`
	Output         map[string]any   `json:"output"`
	Lang           *string          `json:"lang"`
	Entry          *string          `json:"entry"`
	TimeoutMS      int              `json:"timeout_ms"`
	MaxOutputBytes int              `json:"max_output_bytes"`
	Egress         *bool            `json:"egress"`
	ConfigFields   []map[string]any `json:"config_fields"`
	Configs        map[string]any   `json:"configs"`
	Status         *int             `json:"status"`
	Script         *string          `json:"script"`
}

// derefStr dereferences a pointer, treating nil as an empty string (used on the create path so "not sent" and "sent empty" reach the same default).
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// CreateCustomTool POST /customtools/create
func CreateCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	var body saveReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	if !tools.ValidName(name) {
		// It matches the real rule of tools.ValidName (paths.ValidName): no underscores.
		// The old copy said "underscores and hyphens", which contradicted the validator and wasted the user's time.
		xhttp.ErrCode(c, "tool.invalid_name", gin.H{"name": name}, "a tool name may contain letters, digits and hyphens only, and cannot start with a hyphen", "")
		return
	}
	if strings.Contains(name, "__") {
		xhttp.ErrCode(c, "tool.invalid_name", gin.H{"name": name}, "a tool name cannot contain consecutive underscores (the public name is custom__<name>, and they would split the prefix apart)", "")
		return
	}
	if _, err := storeFor(info.Schema).Get(info.Schema, name); err == nil {
		xhttp.ErrCode(c, "tool.name_taken", gin.H{"name": name}, "a custom tool with this name already exists", "")
		return
	}

	// Recreating the same name: clear the version directory the previous entity left. No row in the DB does not mean no directory on disk,
	// and keeping it would let the new tool inherit the old history (release numbering and packing would both be off).
	PurgeToolVersions(info.Schema, name)

	t := &models.CustomTool{
		Name:           name,
		DisplayName:    derefStr(body.DisplayName),
		Description:    strings.TrimSpace(derefStr(body.Description)),
		Parameters:     xsql.JSONB{Data: body.Parameters},
		Output:         xsql.JSONB{Data: body.Output},
		Lang:           defaultLang(derefStr(body.Lang)),
		Entry:          defaultEntry(derefStr(body.Entry), derefStr(body.Lang)),
		TimeoutMS:      body.TimeoutMS,
		MaxOutputBytes: body.MaxOutputBytes,
		Egress:         body.Egress != nil && *body.Egress,
		ConfigFields:   xsql.JSONB{Data: body.ConfigFields},
		Configs:        xsql.JSONB{Data: body.Configs},
		Status:         models.StatusNormal,
		CreatedBy:      info.Name,
	}
	if body.Status != nil {
		t.Status = *body.Status
	}
	if err := storeFor(info.Schema).Create(info.Schema, t); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to create the custom tool: "+err.Error(), "")
		return
	}

	// DB first, files second: if the directory cannot be created the row is rolled back, avoiding a "row without files" half-built tool.
	if err := toolfile.WriteOf(config.Cfg.Storage.RootDir, info.Schema, t); err != nil {
		_ = storeFor(info.Schema).Delete(info.Schema, name)
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the contract file: "+err.Error(), "")
		return
	}
	if err := writeEntry(info.Schema, t, entryContent(body.Script, t)); err != nil {
		_ = storeFor(info.Schema).Delete(info.Schema, name)
		_ = os.RemoveAll(toolDir(info.Schema, name))
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the implementation file: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"tool": buildView(t, toolfile.Contract{Source: "file"})}, "")
}

// UpdateCustomTool PUT /customtools/:name
func UpdateCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	cur, err := storeFor(info.Schema).Get(info.Schema, name)
	if err != nil {
		errToolNotFound(c)
		return
	}
	// The **contract in the file** is the baseline this change is applied to: the read path prefers the file,
	// and starting from the table row would quietly overwrite a hand-edited contract with the old value on one save.
	toolfile.Load(config.Cfg.Storage.RootDir, info.Schema, cur)
	var body saveReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Renaming is not allowed: the ref is already in the grant table, and a rename would silently break other people's grants.
	if body.Lang != nil && strings.TrimSpace(*body.Lang) != "" {
		cur.Lang = defaultLang(*body.Lang)
	}
	// The entry file cannot be cleared: an empty string falls back to the default entry for the language, or the tool would point at a missing file.
	if body.Entry != nil {
		if e := strings.TrimSpace(*body.Entry); e != "" {
			cur.Entry = e
		} else {
			cur.Entry = defaultEntry("", cur.Lang)
		}
	}
	if body.DisplayName != nil {
		cur.DisplayName = strings.TrimSpace(*body.DisplayName)
	}
	if body.Description != nil {
		cur.Description = strings.TrimSpace(*body.Description)
	}
	if body.Parameters != nil {
		cur.Parameters = xsql.JSONB{Data: body.Parameters}
	}
	if body.Output != nil {
		cur.Output = xsql.JSONB{Data: body.Output}
	}
	if body.TimeoutMS > 0 {
		cur.TimeoutMS = body.TimeoutMS
	}
	if body.MaxOutputBytes > 0 {
		cur.MaxOutputBytes = body.MaxOutputBytes
	}
	if body.Egress != nil {
		cur.Egress = *body.Egress
	}
	if body.ConfigFields != nil {
		cur.ConfigFields = xsql.JSONB{Data: body.ConfigFields}
	}
	if body.Configs != nil {
		// a masked string keeps the old value; an explicit empty string clears it
		cur.Configs = xsql.JSONB{Data: mergeConfigs(cur.ConfigMap(), body.Configs)}
	}
	if body.Status != nil {
		cur.Status = *body.Status
	}
	// Upsert: a tool with a directory but no row must still be editable (status and config), because a missing row should not fail an edit.
	if err := storeFor(info.Schema).Upsert(cur); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to update the custom tool: "+err.Error(), "")
		return
	}
	if err := toolfile.WriteOf(config.Cfg.Storage.RootDir, info.Schema, cur); err != nil {
		// On failure the file is deleted: the read path prefers the file, so keeping a stale one would undo this save on the next read.
		_ = os.Remove(toolfile.Path(config.Cfg.Storage.RootDir, info.Schema, cur.Name))
		xhttp.ErrCode(c, "tool.contract_write_failed", gin.H{"name": cur.Name},
			"the settings were saved, but writing the contract file failed (the tool falls back to its table definition): "+err.Error(), "")
		return
	}
	if body.Script != nil {
		if err := writeEntry(info.Schema, cur, *body.Script); err != nil {
			xhttp.ErrCode(c, "tool.entry_write_failed", gin.H{"name": cur.Name},
				"the settings were saved, but writing the implementation file failed: "+err.Error(), "")
			return
		}
	} else if _, serr := os.Stat(entryPath(info.Schema, cur)); serr != nil {
		// The entry changed and the new file does not exist: write a skeleton.
		// Doing nothing means "saved fine, then a run reports a missing entry" - code is written from the file panel,
		// the form no longer carries code, so this fallback has to stay on the backend.
		if werr := writeEntry(info.Schema, cur, tools.SkeletonForLang(cur.Lang)); werr != nil {
			xhttp.ErrCode(c, "tool.entry_write_failed", gin.H{"name": cur.Name},
				"the settings were saved, but the entry file is missing and the skeleton could not be written: "+werr.Error(), "")
			return
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"tool": buildView(cur, toolfile.Contract{Source: "file"})}, "")
}

// DeleteCustomTool DELETE /customtools/:name
func DeleteCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	if err := storeFor(info.Schema).Delete(info.Schema, name); err != nil {
		// A released version is what production runs: the delete is refused and the answer names the version.
		var released *versions.ErrHasReleasedVersion
		if errors.As(err, &released) {
			xhttp.ErrCode(c, "tool.has_release", gin.H{"version": released.Version}, released.Error(), "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete the custom tool: "+err.Error(), "")
		return
	}
	// The code directory goes too: an orphan directory confuses the next create with the same name (and takes disk).
	dir := toolDir(info.Schema, name)
	if err := os.RemoveAll(dir); err != nil {
		xlog.Error("failed to delete the custom tool directory %s: %v", dir, err)
	}
	// The version area is keyed by name too and must be cleared with it (a DB cascade cannot delete disk).
	PurgeToolVersions(info.Schema, name)
	// Grant rows are left alone: a ref pointing at a missing tool resolves to nothing;
	// keeping them lets "delete by mistake, then recreate" restore the grants directly.
	xhttp.OkJSON(c, "Success", gin.H{"name": name}, "")
}

// runReq is the trial-run input.
//
// TimeoutMS defaults (0) to the tool's own timeout_ms; when given it can only tighten or relax up to the cap,
// and the real cap is still the smaller of "the tool's declaration" and "this run".
type runReq struct {
	Args      map[string]any `json:"args"`
	TimeoutMS int            `json:"timeout_ms"`
}

// maxTryRunTimeoutMS caps a trial run (the same 600s as the create_custom_tool meta tool).
const maxTryRunTimeoutMS = 600_000

// RunCustomTool POST /customtools/:name/run
//
// "Trial run": execute once with exactly the production core, returning the result, stdout, stderr, exit code and duration to the admin.
//
// Three deliberate trade-offs:
//  1. it runs the **saved** code (the UI disables the button while the form has unsaved changes) - running unsaved content from a temp file
//     would show results from a different code than production runs, creating new confusion;
//  2. every capability gate and admission check stays (exec / egress / sandboxd ready): a trial run is not a back door around platform policy;
//  3. an execution failure returns 200 + ok=false rather than 4xx - in a 4xx branch the diagnostics (stderr, exit code)
//     would be dropped by the UI as "the request was wrong", yet that is exactly what diagnosis needs.
func RunCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	t, err := storeFor(info.Schema).Get(info.Schema, name)
	if err != nil {
		errToolNotFound(c)
		return
	}

	var body runReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	args := body.Args
	if args == nil {
		args = map[string]any{}
	}

	// Capability gate: the same direction as requireExecCapability in the executor, plus one sentence about where to enable it.
	// Without it the admin side could bypass platform policy - the last bypass anyone should have.
	caps := capabilitypolicy.Snapshot(info.Schema)
	if !caps[capability.Exec] {
		xhttp.ErrCode(c, "tool.capability_denied", gin.H{"capability": capability.Exec},
			fmt.Sprintf("this tenant has not enabled %s, so custom tools cannot run (enable it under tool capabilities)", capability.Exec), "")
		return
	}
	if t.Egress && !caps[capability.Egress] {
		xhttp.ErrCode(c, "tool.capability_denied", gin.H{"capability": capability.Egress},
			fmt.Sprintf("this tool declares egress but this tenant has not enabled %s", capability.Egress), "")
		return
	}

	// Sandbox admission: the same "never guess the environment" rule as exec / run_skill_script.
	sb := tools.CurrentSandbox()
	if sb == nil {
		xhttp.ErrCode(c, "sandbox.not_ready", nil, "the sandbox is not initialised (check sandbox.url and sandboxd)", "")
		return
	}
	if !sandboxinfo.Ready() {
		xhttp.ErrCode(c, "sandbox.not_ready", nil, "sandbox capabilities are unknown (sandboxd not ready): the environment is never guessed, so nothing runs", "")
		return
	}

	rootDir := config.Cfg.Storage.RootDir
	// The workspace lives under the tenant-level runs/: inside root_dir (allowed by the sandboxd mount root),
	// and a directory left behind by an abnormal exit is swept at startup (see service/agent/janitor.go).
	workspace := paths.ResolveRunsDir(rootDir, info.Schema, fmt.Sprintf("tryrun-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to create the workspace: "+err.Error(), "")
		return
	}
	defer os.RemoveAll(workspace)

	var timeout time.Duration
	if body.TimeoutMS > 0 {
		ms := body.TimeoutMS
		if ms > maxTryRunTimeoutMS {
			ms = maxTryRunTimeoutMS
		}
		timeout = time.Duration(ms) * time.Millisecond
	}

	warnings := make([]string, 0, 1)
	if t.Status != models.StatusNormal {
		warnings = append(warnings, "this tool is not in the normal state, so production calls are refused; this only validates the code itself")
	}

	res, runErr := tools.RunCustomToolSpec(c.Request.Context(), tools.CustomToolRun{
		Spec:      tools.CustomToolSpecFromModel(t),
		ToolsRoot: paths.ResolveToolsDir(rootDir, info.Schema),
		Workspace: workspace,
		Sandbox:   sb,
		Timeout:   timeout,
		MaxOutput: tools.DefaultMaxOutput,
		Caps:      caps,
	}, args)

	durationMS := int64(0)
	if res != nil {
		durationMS = res.DurationMS
	}
	out := gin.H{"ok": runErr == nil, "duration_ms": durationMS}
	if res != nil {
		out["result"] = res.Result
		out["stdout"] = res.Stdout
		out["stderr"] = res.Stderr
		out["exit_code"] = res.ExitCode
		out["truncated"] = res.Truncated
		out["masked"] = res.Masked
	}
	if runErr != nil {
		out["error"] = runErr.Error()
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	xlog.Info("custom tool trial run schema=%s tool=%s ok=%v elapsed=%dms", info.Schema, name, runErr == nil, durationMS)
	xhttp.OkJSON(c, "Success", out, "")
}

// The helpers below touch the local filesystem only and never go through the storage abstraction:
// script execution itself needs a reachable host path (the sandbox mount source), so this path never worked on S3 anyway.

func toolDir(schema, name string) string {
	return filepath.Join(paths.ResolveToolsDir(config.Cfg.Storage.RootDir, schema), name)
}

func entryPath(schema string, t *models.CustomTool) string {
	return filepath.Join(toolDir(schema, t.Name), t.Entry)
}

func readEntry(schema string, t *models.CustomTool) (string, error) {
	b, err := os.ReadFile(entryPath(schema, t))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func writeEntry(schema string, t *models.CustomTool, content string) error {
	p := entryPath(schema, t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// entryContent is the implementation file content at creation: when none is given, the executor's skeleton is used (its single source is the tools package).
func entryContent(p *string, t *models.CustomTool) string {
	if p != nil && strings.TrimSpace(*p) != "" {
		return *p
	}
	return tools.SkeletonForLang(t.Lang)
}

func defaultLang(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "js", "node":
		return "js"
	case "lua":
		return "lua"
	case "sh", "shell":
		return "sh"
	default:
		return "python"
	}
}

func defaultEntry(entry, lang string) string {
	if e := strings.TrimSpace(entry); e != "" {
		return e
	}
	switch defaultLang(lang) {
	case "js":
		return "main.js"
	case "lua":
		return "main.lua"
	case "sh":
		return "main.sh"
	}
	return "main.py"
}
