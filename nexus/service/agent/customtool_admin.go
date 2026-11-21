package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	toolgrantsrepo "flame/nexus/shared/repository/toolgrants"
	"flame/nexus/shared/toolfile"
	"flame/tools/xsql"
)

// customToolAdmin implements tools.CustomToolAdmin: create / update / delete custom tools (used only by the super agent's meta tools).
//
// Why it lives in the service layer rather than the tools package: that package should only know "which tools exist and how to run them", and
// touching the table structure would force it to import xsql / repositories / the config root directory - the same reasoning as the extra
// layer in customToolSource.
//
// Writing to disk only uses the local filesystem and does not go through the storage abstraction: script execution itself needs the host path
// to be reachable (it is the sandbox mount source), so the chain is unusable with S3 storage anyway; admin/customtools makes the same choice.
type customToolAdmin struct {
	schema string
	// store is the merged view: Get follows the catalog (a tool dropped on disk by hand can be updated / deleted too), while
	// Create/Update/Delete pass straight through to the repository.
	store  *customtoolstore.Store
	grants toolgrantsrepo.Repository
}

func newCustomToolAdmin(schema string) *customToolAdmin {
	return &customToolAdmin{
		schema: schema,
		store:  customtoolstore.New(config.Cfg.Storage.RootDir, schema),
		grants: toolgrantsrepo.NewRepository(),
	}
}

// toolDir is the host absolute path of the tool directory: {root}/tenants/{schema}/tools/{name}.
func (a *customToolAdmin) toolDir(name string) string {
	return filepath.Join(paths.ResolveToolsDir(config.Cfg.Storage.RootDir, a.schema), name)
}

// Create creates the entity: table row + directory + a minimal runnable skeleton.
//
// The row is written before the disk, and a disk failure rolls the row back - the same order as admin creation (avoiding "in the table, not on
// disk"). The skeleton is not an empty file: a tool must be **callable** before the super agent writes its first version, otherwise a call
// hits "file not found" and looks like a broken tool.
func (a *customToolAdmin) Create(ctx context.Context, in tools.CustomToolInput) (*tools.CustomToolSpec, error) {
	cur, err := a.store.Get(a.schema, in.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if cur != nil {
		return nil, fmt.Errorf("custom tool %q already exists (renaming is not supported: the grant ref is custom:<name>)", in.Name)
	}
	// When the directory exists but its contract is unusable (missing TOOL.json / broken JSON), Get returns ErrNoRows. That must not be
	// treated as "missing" and created: writeEntryFile would overwrite the user's implementation code.
	if st, serr := os.Stat(a.toolDir(in.Name)); serr == nil && st.IsDir() {
		return nil, fmt.Errorf("the directory of tool %q exists but has no usable TOOL.json; repair or delete that directory first", in.Name)
	}

	t := &models.CustomTool{
		Name:           in.Name,
		DisplayName:    strings.TrimSpace(in.DisplayName),
		Description:    strings.TrimSpace(in.Description),
		Parameters:     xsql.JSONB{Data: in.Parameters},
		Output:         xsql.JSONB{Data: in.Output},
		Lang:           defaultScriptLang(in.Lang),
		Entry:          strings.TrimSpace(in.Entry),
		TimeoutMS:      in.TimeoutMS,
		MaxOutputBytes: in.MaxOutputBytes,
		Egress:         in.Egress,
		ConfigFields:   xsql.JSONB{Data: in.ConfigFields},
		Configs:        xsql.JSONB{Data: in.Configs},
		Status:         models.StatusNormal,
	}
	if t.Entry == "" {
		t.Entry = defaultScriptEntry(t.Lang)
	}
	// Validate the entry once more in the service layer: the tool layer's check only protects the meta-tool path, and a future caller (the
	// admin side reusing this implementation, say) cannot be assumed to run it first.
	if err := models.ValidateToolEntry(t.Entry); err != nil {
		return nil, err
	}

	if err := a.store.Create(a.schema, t); err != nil {
		return nil, fmt.Errorf("failed to create custom tool: %w", err)
	}
	// The contract hits the disk before the code: when triaging, first look at "what this tool declares", and a missing code file is easier
	// to spot than a missing contract.
	if err := a.writeContract(t); err != nil {
		_ = a.store.Delete(a.schema, t.Name)
		return nil, fmt.Errorf("failed to write the contract file (table row rolled back): %w", err)
	}
	if err := writeEntryFile(a.toolDir(t.Name), t.Entry, tools.SkeletonForLang(t.Lang)); err != nil {
		_ = a.store.Delete(a.schema, t.Name)
		_ = os.RemoveAll(a.toolDir(t.Name)) // do not leave an orphan "directory without row"
		return nil, fmt.Errorf("failed to write the implementation skeleton (table row rolled back): %w", err)
	}
	spec := tools.CustomToolSpecFromModel(t)
	return &spec, nil
}

// Update changes metadata only. name and status are not arguments: renaming breaks grants, and only the admin side flips the status.
func (a *customToolAdmin) Update(ctx context.Context, name string, in tools.CustomToolUpdate) (*tools.CustomToolSpec, error) {
	cur, err := a.store.Get(a.schema, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("custom tool %q does not exist", name)
		}
		return nil, err
	}
	// The same gate as agents / skills: a locked (2) / deprecated (3) tool cannot be changed.
	if !models.IsModifiable(cur.Status) {
		return nil, fmt.Errorf("custom tool %q is locked or deprecated; an administrator must switch it back to normal first", name)
	}
	// Start from the contract in the file and layer this change on top (the read path prefers the file):
	// starting from the table row would silently overwrite a hand-edited contract in TOOL.json back to the old value.
	toolfile.Load(config.Cfg.Storage.RootDir, a.schema, cur)

	if in.DisplayName != nil {
		cur.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Description != nil {
		cur.Description = strings.TrimSpace(*in.Description)
	}
	if in.Lang != nil && strings.TrimSpace(*in.Lang) != "" {
		cur.Lang = defaultScriptLang(*in.Lang)
	}
	if in.Entry != nil && strings.TrimSpace(*in.Entry) != "" {
		cur.Entry = strings.TrimSpace(*in.Entry)
		if err := models.ValidateToolEntry(cur.Entry); err != nil {
			return nil, err
		}
	}
	if in.Parameters != nil {
		cur.Parameters = xsql.JSONB{Data: in.Parameters}
	}
	if in.Output != nil {
		cur.Output = xsql.JSONB{Data: in.Output}
	}
	if in.TimeoutMS != nil {
		cur.TimeoutMS = *in.TimeoutMS
	}
	if in.MaxOutputBytes != nil {
		cur.MaxOutputBytes = *in.MaxOutputBytes
	}
	if in.Egress != nil {
		cur.Egress = *in.Egress
	}
	if in.ConfigFields != nil {
		cur.ConfigFields = xsql.JSONB{Data: in.ConfigFields}
	}
	if in.Configs != nil {
		// Merge by key instead of replacing wholesale: the model cannot see existing secret values, and a wholesale replacement would wipe
		// them. An explicit empty string can still clear one entry.
		merged := cur.ConfigMap()
		if merged == nil {
			merged = make(map[string]any, len(in.Configs))
		}
		for k, v := range in.Configs {
			merged[k] = v
		}
		cur.Configs = xsql.JSONB{Data: merged}
	}

	// Upsert rather than Update: a tool with a directory on disk and no row in the table (dropped in by hand / pulled from the version store)
	// must still allow status and config changes - a missing row must not make "edit" report a record that cannot be found.
	if err := a.store.Upsert(cur); err != nil {
		return nil, fmt.Errorf("failed to update custom tool: %w", err)
	}
	if err := a.writeContract(cur); err != nil {
		// When the file was not written it must be removed: the read path prefers the file, and keeping the old one would let the next read
		// overwrite this update with the old contract (the table changed, behaviour did not).
		_ = os.Remove(toolfile.Path(config.Cfg.Storage.RootDir, a.schema, cur.Name))
		return nil, fmt.Errorf("settings saved, but writing the contract file failed (fell back to the table definition): %w", err)
	}
	spec := tools.CustomToolSpecFromModel(cur)
	return &spec, nil
}

// writeContract writes the table row out as TOOL.json - the single write entry point for the contract file.
//
// No diffing here: the file is a few hundred bytes, and an unconditional rewrite has one error path fewer than "read, compare, decide", while
// also keeping the format stable (one serialisation function).
func (a *customToolAdmin) writeContract(t *models.CustomTool) error {
	return toolfile.WriteOf(config.Cfg.Storage.RootDir, a.schema, t)
}

// Delete removes the table row and the directory.
//
// With force=false and references still present it returns (refs, nil): the tool layer turns that into "refuse and list the referencing
// parties". Reference rows are left alone - the same as the admin side, so recreating a tool with the same name restores the grants.
func (a *customToolAdmin) Delete(ctx context.Context, name string, force bool) (*tools.CustomToolRefs, error) {
	if _, err := a.store.Get(a.schema, name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("custom tool %q does not exist", name)
		}
		return nil, err
	}

	refs, err := a.usages(name)
	if err != nil {
		return nil, err
	}
	if refs.Total() > 0 && !force {
		return refs, nil
	}
	if err := a.store.Delete(a.schema, name); err != nil {
		return refs, fmt.Errorf("failed to delete custom tool: %w", err)
	}
	if dir := a.toolDir(name); dir != "" {
		if rerr := os.RemoveAll(dir); rerr != nil {
			// The row is gone; a leftover directory only costs disk. Do not swallow the error but do not roll back either (a rollback would
			// leave a weirder half state).
			return refs, fmt.Errorf("%q was deleted, but cleaning up its directory failed: %w", name, rerr)
		}
	}
	// The version domain is keyed by name too: leaving it would make "delete then recreate with the same name" hit the old directory when
	// releasing v1 (the EnsureAbsent precondition reports version exists, an error that points nowhere near here).
	ver := filepath.Join(config.Cfg.Storage.RootDir,
		filepath.FromSlash(paths.VersionEntityKey(a.schema, paths.VersionTools, name)))
	if rerr := os.RemoveAll(ver); rerr != nil {
		return refs, fmt.Errorf("%q was deleted, but cleaning up its version directory failed: %w", name, rerr)
	}
	return refs, nil
}

// ListAll lists every tool (locked / deprecated ones included).
func (a *customToolAdmin) ListAll(ctx context.Context) ([]tools.CustomToolSpec, error) {
	list, err := a.store.List(a.schema)
	if err != nil {
		return nil, err
	}
	out := make([]tools.CustomToolSpec, 0, len(list))
	for i := range list {
		out = append(out, tools.CustomToolSpecFromModel(&list[i]))
	}
	return out, nil
}

// customToolRefScope is the namespace prefix of the grant ref (custom:<name>).
// The same value as toolgrant.ScopeCustom: the query string is built here without importing toolgrant, to avoid a package cycle.
const customToolRefScope = "custom:"

// usages finds who references custom:<name> (agent grants / skill dependencies / custom groups).
func (a *customToolAdmin) usages(name string) (*tools.CustomToolRefs, error) {
	u, err := a.grants.ListRefUsages(a.schema, customToolRefScope+name, false)
	if err != nil {
		return nil, err
	}
	out := &tools.CustomToolRefs{}
	for _, ag := range u.Agents {
		out.Agents = append(out.Agents, ag.Name)
	}
	for _, sk := range u.Skills {
		out.Skills = append(out.Skills, sk.Name)
	}
	for _, g := range u.Groups {
		out.Groups = append(out.Groups, g.Name)
	}
	return out, nil
}

// writeEntryFile writes the implementation entry file (creating parent directories, so nested paths like tools/<name>/lib/x.py work).
func writeEntryFile(dir, entry, content string) error {
	p := filepath.Join(dir, filepath.FromSlash(entry))
	// Confirm once more after Join that the path is still inside the tool directory: filepath.Join cleans the path, but the
	// entryInsideToolDir check above only looked at the relative shape, so this is the final backstop.
	if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		return fmt.Errorf("entry %q resolves outside the tool directory", entry)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

func defaultScriptLang(lang string) string {
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

func defaultScriptEntry(lang string) string {
	switch defaultScriptLang(lang) {
	case "js":
		return "main.js"
	case "lua":
		return "main.lua"
	case "sh":
		return "main.sh"
	}
	return "main.py"
}
