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

// fakeRepo 只实现视图用到的方法；其余走嵌入的 nil 接口（用到即 panic，便于发现漏实现）。
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

// writeTool 在工具目录里放一份合法契约（可选实现文件）。
func writeTool(t *testing.T, s *Store, name string, f *models.ToolFile, withEntry bool) {
	t.Helper()
	if err := toolfile.Write(s.rootDir, s.schema, name, f); err != nil {
		t.Fatalf("写契约失败: %v", err)
	}
	if withEntry {
		dir := toolfile.Dir(s.rootDir, s.schema, name)
		if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// 目录即实体：盘上有合法契约、表里没行 → 工具存在，缺省正常态、无配置。
func TestDirOnlyToolIsVisible(t *testing.T) {
	s, _ := newStore(t)
	writeTool(t, s, "calc", &models.ToolFile{Description: "算"}, true)

	list, issues, err := s.ListWithIssues()
	if err != nil {
		t.Fatalf("ListWithIssues = %v", err)
	}
	if len(list) != 1 || list[0].Name != "calc" || list[0].Status != models.StatusNormal {
		t.Fatalf("目录里的工具应可见且为正常态: %+v", list)
	}
	if len(issues) != 0 {
		t.Errorf("不该有问题: %+v", issues)
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

// 表只提供策略：status=锁定 时 ListEnabled 不产出，但 List 里还在（管理端要能看见）。
func TestRowProvidesPolicy(t *testing.T) {
	s, _ := newStore(t, models.CustomTool{Name: "calc", Status: models.StatusLocked})
	writeTool(t, s, "calc", &models.ToolFile{Description: "算"}, true)

	all, err := s.List("")
	if err != nil || len(all) != 1 || all[0].Status != models.StatusLocked {
		t.Fatalf("List 应保留锁定态: %+v %v", all, err)
	}
	enabled, err := s.ListEnabled("")
	if err != nil || len(enabled) != 0 {
		t.Fatalf("ListEnabled 不该产出锁定工具: %+v %v", enabled, err)
	}
}

// 契约字段以文件为准：表里的旧描述不该覆盖文件里的新描述。
func TestFileWinsOverRow(t *testing.T) {
	s, _ := newStore(t, models.CustomTool{
		Name:        "calc",
		Description: "表里的旧描述",
		Parameters:  xsql.JSONB{Data: map[string]any{"type": "object"}},
	})
	writeTool(t, s, "calc", &models.ToolFile{Description: "文件里的新描述"}, true)

	got, err := s.Get("", "calc")
	if err != nil || got == nil {
		t.Fatalf("Get = %v", err)
	}
	if got.Description != "文件里的新描述" {
		t.Fatalf("描述应来自文件，实际 %q", got.Description)
	}
}

// 目录在、契约缺、表有行 → 先用表定义并告警（兼容期，不静默消失）。
func TestMissingContractFallsBackToRow(t *testing.T) {
	s, _ := newStore(t, models.CustomTool{Name: "calc", Description: "表里的描述", Status: models.StatusNormal})
	// 只建目录，不放 TOOL.json
	dir := toolfile.Dir(s.rootDir, s.schema, "calc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	list, issues, err := s.ListWithIssues()
	if err != nil || len(list) != 1 {
		t.Fatalf("应退回表定义: %+v %v", list, err)
	}
	if list[0].Description != "表里的描述" {
		t.Errorf("未用到表定义: %+v", list[0])
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Error, "自动补写") {
		t.Fatalf("应提示缺契约且可自动补写: %+v", issues)
	}
}

// 表有行、目录已删 → 不复活（否则会造出一个没有代码的假工具），只报残留。
func TestRowWithoutDirIsReportedNotResurrected(t *testing.T) {
	s, _ := newStore(t, models.CustomTool{Name: "ghost", Status: models.StatusNormal})

	list, issues, err := s.ListWithIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("目录不存在不该产出工具: %+v", list)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Error, "已删除") {
		t.Fatalf("应报残留记录: %+v", issues)
	}
	if _, serr := os.Stat(toolfile.Dir(s.rootDir, s.schema, "ghost")); !os.IsNotExist(serr) {
		t.Error("不该为残留记录创建目录")
	}
}

// 缺行时更新状态/配置要能落库：Upsert 在 Update 返回 ErrNoRows 时转 Create。
func TestUpsertCreatesWhenRowMissing(t *testing.T) {
	s, repo := newStore(t)
	repo.updateErr = sql.ErrNoRows

	tool := &models.CustomTool{Name: "calc", Status: models.StatusLocked}
	if err := s.Upsert(tool); err != nil {
		t.Fatalf("Upsert = %v", err)
	}
	if repo.created == nil || repo.created.Name != "calc" {
		t.Fatalf("缺行时应插入: %+v", repo.created)
	}
}

// 版本视图的目录 key：{root}/tenants/{schema}/versions/tools/<name>/v<n>
func TestVersionDirLayout(t *testing.T) {
	s := New(filepath.Join("root", "agents"), "t0")
	got := s.versionDir("calc", 3)
	want := filepath.Join("root", "agents", "tenants", "t0", "versions", "tools", "calc", "v3")
	if got != want {
		t.Fatalf("versionDir = %s, want %s", got, want)
	}
}

// 正式运行读版本目录里的契约；缺契约必须报错（不能回退草稿 / 表行，那等于发布冻结失效）。
func TestReadReleasedContract(t *testing.T) {
	s, _ := newStore(t)
	dir := s.versionDir("calc", 2)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := toolfile.WriteAt(dir, &models.ToolFile{
		Description: "v2 的描述", Lang: "python", Entry: "main.py",
	}); err != nil {
		t.Fatal(err)
	}
	f, err := s.readReleasedContract(releasedRow{Name: "calc", Version: 2})
	if err != nil || f == nil || f.Description != "v2 的描述" {
		t.Fatalf("readReleasedContract = %+v, %v", f, err)
	}
	if _, err := s.readReleasedContract(releasedRow{Name: "calc", Version: 9}); err == nil ||
		!strings.Contains(err.Error(), "缺少契约") {
		t.Fatalf("缺契约应报错: %v", err)
	}
}

// 名字与目录名不一致的 TOOL.json 不能被当成工具（否则授权 ref 会对不上）。
func TestScanRejectsNameMismatch(t *testing.T) {
	s, _ := newStore(t)
	writeTool(t, s, "calc", &models.ToolFile{Name: "other", Description: "x"}, true)

	list, issues, err := s.ListWithIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("名字不一致不该产出: %+v", list)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Error, "不一致") {
		t.Fatalf("应报名字不一致: %+v", issues)
	}
}
