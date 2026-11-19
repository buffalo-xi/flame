package customtoolstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/customtools"
	"flame/nexus/shared/toolfile"
	"flame/tools/xsql"
)

// fakeRepo implements only the methods the view uses; the rest go through the embedded nil interface (a call panics, which surfaces a missed
// implementation).
type fakeRepo struct {
	customtools.Repository
	rows      []models.CustomTool
	updateErr error
	created   *models.CustomTool
}

func (f *fakeRepo) List(string) ([]models.CustomTool, error) { return f.rows, nil }

func (f *fakeRepo) Get(_, name string) (*models.CustomTool, error) {
	for i := range f.rows {
		if f.rows[i].Name == name {
			return &f.rows[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (f *fakeRepo) Create(_ string, t *models.CustomTool) error {
	f.created = t
	return nil
}

func (f *fakeRepo) Update(string, *models.CustomTool) error { return f.updateErr }

func newStore(t *testing.T, rows ...models.CustomTool) (*Store, *fakeRepo) {
	t.Helper()
	s := New(t.TempDir(), "t0")
	repo := &fakeRepo{rows: rows}
	s.Repository = repo
	return s, repo
}

// writeTool puts a valid contract (and optionally the implementation file) in a tool directory.
func writeTool(t *testing.T, s *Store, name string, f *models.ToolFile, withEntry bool) {
	t.Helper()
	if err := toolfile.Write(s.rootDir, s.schema, name, f); err != nil {
		t.Fatalf("failed to write the contract: %v", err)
	}
	if withEntry {
		dir := toolfile.Dir(s.rootDir, s.schema, name)
		if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The directory is the entity: a valid contract on disk with no row in the table means the tool exists, in the default normal state with no
// configuration.
func TestDirOnlyToolIsVisible(t *testing.T) {
	s, _ := newStore(t)
	writeTool(t, s, "calc", &models.ToolFile{Description: "算"}, true)

	list, issues, err := s.ListWithIssues()
	if err != nil {
		t.Fatalf("ListWithIssues = %v", err)
	}
	if len(list) != 1 || list[0].Name != "calc" || list[0].Status != models.StatusNormal {
		t.Fatalf("a tool on disk should be visible and in the normal state: %+v", list)
	}
	if len(issues) != 0 {
		t.Errorf("there should be no issues: %+v", issues)
	}

	got, err := s.Get("", "calc")
	if err != nil || got == nil || got.Description != "算" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	status, exists, err := s.Status("calc")
	if err != nil || !exists || status != models.StatusNormal {
		t.Fatalf("Status = %d, %v, %v", status, exists, err)
	}
}

// The table supplies policy only: with status=locked, ListEnabled produces nothing while List still shows it (the admin side must see it).
func TestRowProvidesPolicy(t *testing.T) {
	s, _ := newStore(t, models.CustomTool{Name: "calc", Status: models.StatusLocked})
	writeTool(t, s, "calc", &models.ToolFile{Description: "算"}, true)

	all, err := s.List("")
	if err != nil || len(all) != 1 || all[0].Status != models.StatusLocked {
		t.Fatalf("List should keep the locked state: %+v %v", all, err)
	}
	enabled, err := s.ListEnabled("")
	if err != nil || len(enabled) != 0 {
		t.Fatalf("ListEnabled must not produce a locked tool: %+v %v", enabled, err)
	}
}

// Contract fields follow the file: an old description in the table must not override the new one in the file.
func TestFileWinsOverRow(t *testing.T) {
	s, _ := newStore(t, models.CustomTool{
		Name:        "calc",
		Description: "old description in the table",
		Parameters:  xsql.JSONB{Data: map[string]any{"type": "object"}},
	})
	writeTool(t, s, "calc", &models.ToolFile{Description: "new description in the file"}, true)

	got, err := s.Get("", "calc")
	if err != nil || got == nil {
		t.Fatalf("Get = %v", err)
	}
	if got.Description != "new description in the file" {
		t.Fatalf("the description should come from the file, got %q", got.Description)
	}
}

// The directory exists, the contract is missing, the table has a row: use the table definition and warn (transition period, never silently
// disappear).
func TestMissingContractFallsBackToRow(t *testing.T) {
	s, _ := newStore(t, models.CustomTool{Name: "calc", Description: "description in the table", Status: models.StatusNormal})
	// Create the directory only, without a TOOL.json
	dir := toolfile.Dir(s.rootDir, s.schema, "calc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	list, issues, err := s.ListWithIssues()
	if err != nil || len(list) != 1 {
		t.Fatalf("it should fall back to the table definition: %+v %v", list, err)
	}
	if list[0].Description != "description in the table" {
		t.Errorf("the table definition was not used: %+v", list[0])
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Error, "rewrites it automatically") {
		t.Fatalf("it should report the missing contract and the automatic rewrite: %+v", issues)
	}
}

// The table has a row but the directory was deleted: do not resurrect it (that would fabricate a code-less tool), only report the leftover.
func TestRowWithoutDirIsReportedNotResurrected(t *testing.T) {
	s, _ := newStore(t, models.CustomTool{Name: "ghost", Status: models.StatusNormal})

	list, issues, err := s.ListWithIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("a missing directory must not produce a tool: %+v", list)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Error, "has been deleted") {
		t.Fatalf("it should report the leftover row: %+v", issues)
	}
	if _, serr := os.Stat(toolfile.Dir(s.rootDir, s.schema, "ghost")); !os.IsNotExist(serr) {
		t.Error("it must not create a directory for a leftover row")
	}
}

// Updating status / configuration with no row must still persist: Upsert turns Create when Update returns ErrNoRows.
func TestUpsertCreatesWhenRowMissing(t *testing.T) {
	s, repo := newStore(t)
	repo.updateErr = sql.ErrNoRows

	tool := &models.CustomTool{Name: "calc", Status: models.StatusLocked}
	if err := s.Upsert(tool); err != nil {
		t.Fatalf("Upsert = %v", err)
	}
	if repo.created == nil || repo.created.Name != "calc" {
		t.Fatalf("it should insert when the row is missing: %+v", repo.created)
	}
}

// The version view's directory key: {root}/tenants/{schema}/versions/tools/<name>/v<n>
func TestVersionDirLayout(t *testing.T) {
	s := New(filepath.Join("root", "agents"), "t0")
	got := s.versionDir("calc", 3)
	want := filepath.Join("root", "agents", "tenants", "t0", "versions", "tools", "calc", "v3")
	if got != want {
		t.Fatalf("versionDir = %s, want %s", got, want)
	}
}

// A production run reads the contract in the version directory; a missing contract must error (never fall back to the draft / the table row,
// which would make the release freeze meaningless).
func TestReadReleasedContract(t *testing.T) {
	s, _ := newStore(t)
	dir := s.versionDir("calc", 2)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := toolfile.WriteAt(dir, &models.ToolFile{
		Description: "v2 description", Lang: "python", Entry: "main.py",
	}); err != nil {
		t.Fatal(err)
	}
	f, err := s.readReleasedContract(releasedRow{Name: "calc", Version: 2})
	if err != nil || f == nil || f.Description != "v2 description" {
		t.Fatalf("readReleasedContract = %+v, %v", f, err)
	}
	if _, err := s.readReleasedContract(releasedRow{Name: "calc", Version: 9}); err == nil ||
		!strings.Contains(err.Error(), "missing the contract file") {
		t.Fatalf("a missing contract should error: %v", err)
	}
}

// A TOOL.json whose name does not match the directory must not count as a tool (otherwise the grant ref cannot match up).
func TestScanRejectsNameMismatch(t *testing.T) {
	s, _ := newStore(t)
	writeTool(t, s, "calc", &models.ToolFile{Name: "other", Description: "x"}, true)

	list, issues, err := s.ListWithIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("a name mismatch must not produce a tool: %+v", list)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Error, "does not match the directory name") {
		t.Fatalf("it should report the name mismatch: %+v", issues)
	}
}
