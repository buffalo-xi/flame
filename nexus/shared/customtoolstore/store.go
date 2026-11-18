// Package customtoolstore 自定义工具的合并视图：**磁盘目录是实体，表是策略**。
//
// 分工（这是「目录即实体」的落点）：
//   - 有哪些工具      → tools/ 下的目录 + 合法的 TOOL.json（toolfile.Scan）
//   - 工具是什么      → TOOL.json（描述 / 参数 / 语言 / 入口 / 配置声明）
//   - 租户策略与运行态 → custom_tools 表（status / configs / 时间戳）
//
// 之前三者都在表里，于是「盘上有目录、平台看不见」只能靠人记得再建一行。
// 现在反过来：目录在就算存在，缺表行 = 正常态、无配置。
//
// 兼容期两条降级（都记 issue，不静默）：
//   - 目录在、TOOL.json 缺或坏、且表有行 → 先用表定义（打开管理页会自动补写文件）
//   - 表有行、目录已不在 → 不复活（补写契约会造出一个没有代码的假工具），只报 issue
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

// Store 某租户的工具视图。嵌入 Repository：Create / Update / Delete 直接透传，
// 只覆盖「读」的三个方法（List / ListEnabled / Get），写路径仍由调用方显式决定写文件。
type Store struct {
	customtools.Repository
	rootDir string
	schema  string
}

// New 构造视图。rootDir 是存储根（config.Cfg.Agent.RootDir）。
func New(rootDir, schema string) *Store {
	return &Store{Repository: customtools.NewRepository(), rootDir: rootDir, schema: schema}
}

// List 全部工具（含锁定 / 废弃），按 name 升序。
//
// schema 入参只为匹配 customtools.Repository 的方法签名，实际用构造时的 schema：
// 视图是租户维度的，允许调用方传别的 schema 只会读出一份对不上的数据。
func (s *Store) List(string) ([]models.CustomTool, error) {
	tools, _, err := s.ListWithIssues()
	return tools, err
}

// ListEnabled 只列 status=1（执行 / 授权目录用）。
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

// ListWithIssues 工具列表 + 逐条目录问题（管理端展示用）。
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

	// 目录在、契约缺或坏：表里有行就用表定义（兼容期），并把 issue 的措辞换成
	// 「暂用数据库定义」，否则用户看到「缺 TOOL.json」会以为工具不可用。
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
				issues[i].Error = "TOOL.json 缺失或内容有误，暂用数据库定义（打开「自定义工具」页会自动补写）"
			}
			continue
		}
		issues = append(issues, toolfile.ScanIssue{
			Path:  name,
			Error: "记录存在但工具目录已删除（可删除该记录）",
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Slice(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
	return out, issues, nil
}

// Get 按短名取。
//
// 存在性以目录为准；目录在但契约缺/坏时，有表行就退回表（兼容期），两者都没有
// 才返回 sql.ErrNoRows —— 与仓储同一套错误语义，调用方不用区分来源。
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
	// 契约不可用：有行先用行，否则不存在（或语法错误要暴露出去）。
	if row != nil {
		t := *row
		return &t, nil
	}
	if ferr != nil {
		return nil, ferr
	}
	return nil, sql.ErrNoRows
}

// ===== 正式运行视图：定义取当前版本，策略取表 =====
//
// 草稿视图（List / Get）回答「正在编辑什么」；正式运行必须回答「这一版发布的是什么」。
// 两者混用的后果不是显示问题：脚本取版本目录、契约取草稿，会让「发布后改描述/入口/超时」
// 立刻影响线上（entry 改名甚至直接跑不起来）。

// releasedRow 有当前版本（is_current）的工具。
type releasedRow struct {
	ID      string
	Name    string
	Version int
}

// versionDir 某工具第 n 版目录的宿主绝对路径：
// {root}/tenants/{schema}/versions/tools/<name>/v<n>。
func (s *Store) versionDir(name string, version int) string {
	key := paths.VersionKey(s.schema, paths.VersionTools, name, version)
	return filepath.Join(s.rootDir, filepath.FromSlash(key))
}

// releasedRows 读「有当前版本」的工具（可按 name 收窄）。
// 版本行的存在性以 tool_versions 为准：没有版本的草稿工具不应出现在正式运行里。
func (s *Store) releasedRows(name string) ([]releasedRow, error) {
	if db.DB == nil {
		return nil, fmt.Errorf("数据库未初始化：无法读取自定义工具版本")
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

// readReleasedContract 读版本目录里的契约并校验。
// 缺失 / 非法都返回错误：调用方决定是整条跳过（清单）还是报错（单取）。
func (s *Store) readReleasedContract(r releasedRow) (*models.ToolFile, error) {
	dir := s.versionDir(r.Name, r.Version)
	f, err := toolfile.ReadAt(dir)
	if err != nil {
		return nil, fmt.Errorf("读取工具 %q 的 v%d 契约失败: %w", r.Name, r.Version, err)
	}
	if f == nil {
		return nil, fmt.Errorf("工具 %q 的 v%d 缺少契约文件 %s（版本目录：%s）", r.Name, r.Version, models.ToolFileName, dir)
	}
	if verr := f.Validate(r.Name); verr != nil {
		return nil, fmt.Errorf("工具 %q 的 v%d 契约无效: %w", r.Name, r.Version, verr)
	}
	return f, nil
}

// Released 正式运行视图（单个）：
//   - 定义（描述 / 参数 / 语言 / 入口 / 超时 / 出网 / 配置声明）取**当前版本目录**的 TOOL.json；
//   - 策略（id / status / configs）仍取表 —— 授权与配置不冻结，锁废 / revoke 必须立即生效。
//
// 没有当前版本时返回 sql.ErrNoRows：正式运行不认从未发布的草稿（与 agent / skill 同口径）。
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

// ListReleased 正式运行的清单：只列「有当前版本」且状态正常的工具。
//
// 契约缺失 / 非法的条目**不进清单**：宁可不出现，也不退回草稿定义 —— 那等于发布冻结失效。
// 单取（Released）仍会给出具体原因，便于排查「为什么这个工具没出现在线上」。
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

// Status 只取编辑态（文件闸用；锁定态也要能判，所以不能复用「只给正常态」的筛选）。
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

// Upsert 写策略行：有行就更新，没行就插入。
//
// 「目录即实体」之后必然出现「盘上有工具、表里没行」（手工放进去的工程目录、
// 从版本库拉下来的工具包），更新状态/配置时不能因为缺行就失败。
func (s *Store) Upsert(t *models.CustomTool) error {
	err := s.Repository.Update(s.schema, t)
	if errors.Is(err, sql.ErrNoRows) {
		return s.Repository.Create(s.schema, t)
	}
	return err
}

// merge 把契约叠到策略行上：行提供 id / status / configs / 时间戳，文件提供其余。
func merge(f *models.ToolFile, row *models.CustomTool, name string) models.CustomTool {
	t := models.CustomTool{Name: name, Status: models.StatusNormal}
	if row != nil {
		t = *row
	}
	f.Apply(&t)
	return t
}
