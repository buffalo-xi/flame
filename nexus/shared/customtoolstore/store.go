// Package customtoolstore is the merged view of custom tools: **the on-disk directory is the entity, the table is the policy**.
//
// The split (this is where "the directory is the entity" lands):
//   - which tools exist   -> directories under tools/ with a valid TOOL.json (toolfile.Scan)
//   - what a tool is      -> TOOL.json (description / parameters / language / entry / configuration declaration)
//   - tenant policy and runtime state -> the custom_tools table (status / configs / timestamps)
//
// All three used to live in the table, so "a directory exists on disk but the platform cannot see it" could only be fixed by remembering to
// insert a row. Now it is the other way round: a directory means it exists, and a missing row means normal state with no configuration.
//
// Two transitional degradations (both recorded as an issue, never silent):
//   - the directory exists, TOOL.json is missing or broken, and the table has a row -> use the table definition first (opening the admin
//     page rewrites the file automatically)
//   - the table has a row but the directory is gone -> do not resurrect it (writing a contract back would fabricate a fake tool with no
//     code), just report an issue
package customtoolstore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/customtools"
	"flame/nexus/shared/toolfile"
)

// Store is one tenant's tool view. It embeds Repository: Create / Update / Delete pass straight through, and only the three "read" methods
// (List / ListEnabled / Get) are overridden - the write path still belongs explicitly to the caller, which decides when to write the file.
type Store struct {
	customtools.Repository
	rootDir string
	schema  string
}

// New builds the view. rootDir is the storage root (config.Cfg.Storage.RootDir).
func New(rootDir, schema string) *Store {
	return &Store{Repository: customtools.NewRepository(), rootDir: rootDir, schema: schema}
}

// List returns every tool (locked / deprecated included), sorted by name ascending.
//
// The schema argument only exists to match customtools.Repository's method signature; the schema from construction is used: the view is
// tenant-scoped, and letting a caller pass another schema would only read a mismatched data set.
func (s *Store) List(string) ([]models.CustomTool, error) {
	tools, _, err := s.ListWithIssues()
	return tools, err
}

// ListEnabled lists status=1 only (for execution and the grant catalog).
func (s *Store) ListEnabled(string) ([]models.CustomTool, error) {
	tools, _, err := s.ListWithIssues()
	if err != nil {
		return nil, err
	}
	out := make([]models.CustomTool, 0, len(tools))
	for i := range tools {
		if tools[i].Status == models.StatusNormal {
			out = append(out, tools[i])
		}
	}
	return out, nil
}

// ListWithIssues returns the tool list plus the per-directory issues (for the admin UI).
func (s *Store) ListWithIssues() ([]models.CustomTool, []toolfile.ScanIssue, error) {
	rows, err := s.Repository.List(s.schema)
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]*models.CustomTool, len(rows))
	for i := range rows {
		byName[rows[i].Name] = &rows[i]
	}

	entries, issues := toolfile.Scan(s.rootDir, s.schema)
	seen := make(map[string]bool, len(entries))
	out := make([]models.CustomTool, 0, len(entries)+len(rows))

	for _, e := range entries {
		seen[e.Name] = true
		out = append(out, merge(e.File, byName[e.Name], e.Name))
	}

	// The directory exists but the contract is missing or broken: if the table has a row, use the table definition (transition period) and
	// reword the issue as "using the database definition for now" - otherwise the user reads "TOOL.json missing" and concludes the tool is
	// unusable.
	issueIdx := make(map[string]int, len(issues))
	for i := range issues {
		issueIdx[issues[i].Path] = i
	}
	for name, row := range byName {
		if seen[name] {
			continue
		}
		if st, serr := os.Stat(toolfile.Dir(s.rootDir, s.schema, name)); serr == nil && st.IsDir() {
			t := *row
			out = append(out, t)
			if i, ok := issueIdx[name]; ok {
				issues[i].Error = "TOOL.json is missing or invalid; using the database definition for now (opening the custom tools page rewrites it automatically)"
			}
			continue
		}
		issues = append(issues, toolfile.ScanIssue{
			Path:  name,
			Error: "the row exists but the tool directory has been deleted (the row can be removed)",
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Slice(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
	return out, issues, nil
}

// Get fetches by short name.
//
// Existence follows the directory; when the directory exists but the contract is missing / broken, it falls back to the table if there is a
// row (transition period), and only when neither is available does it return sql.ErrNoRows - the same error semantics as the repository, so
// the caller need not tell the sources apart.
func (s *Store) Get(_, name string) (*models.CustomTool, error) {
	f, ferr := toolfile.Read(s.rootDir, s.schema, name)
	row, rerr := s.Repository.Get(s.schema, name)
	if rerr != nil && !errors.Is(rerr, sql.ErrNoRows) {
		return nil, rerr
	}

	if f != nil && ferr == nil {
		if verr := f.Validate(name); verr == nil {
			t := merge(f, row, name)
			return &t, nil
		}
	}
	// The contract is unusable: use the row if there is one, otherwise it does not exist (or the syntax error has to surface).
	if row != nil {
		t := *row
		return &t, nil
	}
	if ferr != nil {
		return nil, ferr
	}
	return nil, sql.ErrNoRows
}

// ===== Production view: the definition comes from the current version, the policy from the table =====
//
// The draft view (List / Get) answers "what is being edited"; a production run must answer "what was released in this version".
// Mixing the two is not a display problem: reading the script from the version directory while taking the contract from the draft would let
// "changing the description / entry / timeout after release" take effect in production immediately (renaming the entry can even make it fail
// to start).

// releasedRow is a tool that has a current version (is_current).
type releasedRow struct {
	ID      string
	Name    string
	Version int
}

// versionDir returns the host absolute path of a tool's version n directory:
// {root}/tenants/{schema}/versions/tools/<name>/v<n>.
func (s *Store) versionDir(name string, version int) string {
	key := paths.VersionKey(s.schema, paths.VersionTools, name, version)
	return filepath.Join(s.rootDir, filepath.FromSlash(key))
}

// releasedRows reads the tools that have a current version (optionally narrowed by name).
// A version row's existence follows tool_versions: a draft tool with no version must not appear in production runs.
func (s *Store) releasedRows(name string) ([]releasedRow, error) {
	if db.DB == nil {
		return nil, fmt.Errorf("database not initialised: cannot read custom tool versions")
	}
	q := `SELECT v.tool_id::text, t.name, v.version
		FROM ` + s.schema + `.tool_versions v
		JOIN ` + s.schema + `.custom_tools t ON t.id = v.tool_id
		WHERE v.is_current`
	var args []any
	if name != "" {
		q += ` AND t.name = $1`
		args = append(args, name)
	}
	q += ` ORDER BY t.name ASC`

	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]releasedRow, 0, 8)
	for rows.Next() {
		var r releasedRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Version); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// readReleasedContract reads and validates the contract in a version directory.
// A missing / illegal one both return an error: the caller decides whether to skip the entry (a list) or fail (a single fetch).
func (s *Store) readReleasedContract(r releasedRow) (*models.ToolFile, error) {
	dir := s.versionDir(r.Name, r.Version)
	f, err := toolfile.ReadAt(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read the v%d contract of tool %q: %w", r.Version, r.Name, err)
	}
	if f == nil {
		return nil, fmt.Errorf("tool %q v%d is missing the contract file %s (version directory: %s)", r.Name, r.Version, models.ToolFileName, dir)
	}
	if verr := f.Validate(r.Name); verr != nil {
		return nil, fmt.Errorf("the v%d contract of tool %q is invalid: %w", r.Version, r.Name, verr)
	}
	return f, nil
}

// Released is the production view (single tool):
//   - the definition (description / parameters / language / entry / timeout / egress / configuration declaration) comes from the **current
//     version directory's** TOOL.json;
//   - the policy (id / status / configs) still comes from the table - grants and configuration are not frozen, and a lock / revoke must take
//     effect immediately.
//
// With no current version it returns sql.ErrNoRows: a production run does not recognise a draft that was never released (the same criterion
// as agent / skill).
func (s *Store) Released(name string) (*models.CustomTool, error) {
	rows, err := s.releasedRows(name)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	f, err := s.readReleasedContract(rows[0])
	if err != nil {
		return nil, err
	}
	row, rerr := s.Repository.Get(s.schema, name)
	if rerr != nil && !errors.Is(rerr, sql.ErrNoRows) {
		return nil, rerr
	}
	t := merge(f, row, name)
	return &t, nil
}

// ListReleased is the production list: only tools that have a current version and a normal status.
//
// Entries whose contract is missing / invalid **do not enter the list**: better to be absent than to fall back to the draft definition, which
// would make the release freeze meaningless.
// A single fetch (Released) still gives the concrete reason, which helps investigate "why is this tool not in production".
func (s *Store) ListReleased() ([]models.CustomTool, error) {
	rows, err := s.releasedRows("")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	policy, err := s.Repository.List(s.schema)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*models.CustomTool, len(policy))
	for i := range policy {
		byName[policy[i].Name] = &policy[i]
	}

	out := make([]models.CustomTool, 0, len(rows))
	for _, r := range rows {
		f, ferr := s.readReleasedContract(r)
		if ferr != nil {
			continue
		}
		t := merge(f, byName[r.Name], r.Name)
		if t.Status != models.StatusNormal {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// Status returns the editing state only (for the file gate; the locked state has to be judged too, so the "normal only" filter cannot be
// reused).
func (s *Store) Status(name string) (int, bool, error) {
	t, err := s.Get("", name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return t.Status, true, nil
}

// Upsert writes the policy row: update when one exists, insert when none does.
//
// Once "the directory is the entity" holds, "a tool on disk with no row in the table" necessarily appears (a project directory placed by hand,
// a tool package pulled from a repository), and updating its status / configuration must not fail just because the row is missing.
func (s *Store) Upsert(t *models.CustomTool) error {
	err := s.Repository.Update(s.schema, t)
	if errors.Is(err, sql.ErrNoRows) {
		return s.Repository.Create(s.schema, t)
	}
	return err
}

// merge lays the contract over the policy row: the row supplies id / status / configs / timestamps, the file everything else.
func merge(f *models.ToolFile, row *models.CustomTool, name string) models.CustomTool {
	t := models.CustomTool{Name: name, Status: models.StatusNormal}
	if row != nil {
		t = *row
	}
	f.Apply(&t)
	return t
}
