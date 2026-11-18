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

// customToolAdmin 实现 tools.CustomToolAdmin：自定义工具的增删改（仅超级智能体的元工具会用到）。
//
// 为什么放在 service 层而不是 tools 包：tools 包只该知道「有哪些工具、怎么跑」，
// 碰表结构就意味着它要 import xsql / 仓储 / 配置根目录 —— 与 customToolSource
// 中间隔一层的理由相同。
//
// 写盘只做本地文件系统、不绕 storage 抽象：脚本执行本身要求宿主路径可达
// （沙箱挂载源），S3 存储下这条链路本来就不可用，admin/customtools 同口径。
type customToolAdmin struct {
	schema string
	// store 合并视图：Get 以目录为准（盘上手工放进来的工具也能被改/删），
	// Create/Update/Delete 透传给仓储。
	store  *customtoolstore.Store
	grants toolgrantsrepo.Repository
}

func newCustomToolAdmin(schema string) *customToolAdmin {
	return &customToolAdmin{
		schema: schema,
		store:  customtoolstore.New(config.Cfg.Agent.RootDir, schema),
		grants: toolgrantsrepo.NewRepository(),
	}
}

// toolDir 工具目录的宿主绝对路径：{root}/tenants/{schema}/tools/{name}。
func (a *customToolAdmin) toolDir(name string) string {
	return filepath.Join(paths.ResolveToolsDir(config.Cfg.Agent.RootDir, a.schema), name)
}

// Create 建实体：表行 + 目录 + 可跑的最小骨架。
//
// 先落库再落盘，落盘失败回滚那一行 —— 与 admin 的创建顺序一致（避免「库里有、盘上没有」）。
// 骨架不是空文件：工具在超级智能体写第一版代码之前就应当是**可调用**的，否则调一次得到
// 「文件不存在」，看起来像工具坏了。
func (a *customToolAdmin) Create(ctx context.Context, in tools.CustomToolInput) (*tools.CustomToolSpec, error) {
	cur, err := a.store.Get(a.schema, in.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if cur != nil {
		return nil, fmt.Errorf("自定义工具 %q 已存在（改名不支持：授权 ref 是 custom:<name>）", in.Name)
	}
	// 目录在但契约不可用（缺 TOOL.json / JSON 写坏了）时 Get 会返回 ErrNoRows，
	// 此时不能当成「不存在」直接创建：writeEntryFile 会覆盖掉用户手里的实现代码。
	if st, serr := os.Stat(a.toolDir(in.Name)); serr == nil && st.IsDir() {
		return nil, fmt.Errorf("工具目录 %q 已存在但缺可用的 TOOL.json，请先修复或删除该目录", in.Name)
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
	// 服务层再校一次入口：工具层的校验只保护元工具这条路径，
	// 将来若有别的调用方（如管理端复用本实现）不能指望它一定先跑过。
	if err := models.ValidateToolEntry(t.Entry); err != nil {
		return nil, err
	}

	if err := a.store.Create(a.schema, t); err != nil {
		return nil, fmt.Errorf("创建自定义工具失败: %w", err)
	}
	// 契约先于代码落盘：排查时先看「这个工具声明了什么」，代码缺失比契约缺失好查。
	if err := a.writeContract(t); err != nil {
		_ = a.store.Delete(a.schema, t.Name)
		return nil, fmt.Errorf("写入契约文件失败（已回滚表行）: %w", err)
	}
	if err := writeEntryFile(a.toolDir(t.Name), t.Entry, tools.SkeletonForLang(t.Lang)); err != nil {
		_ = a.store.Delete(a.schema, t.Name)
		_ = os.RemoveAll(a.toolDir(t.Name)) // 别留下「有目录、没表行」的孤儿
		return nil, fmt.Errorf("写入实现骨架失败（已回滚表行）: %w", err)
	}
	spec := tools.CustomToolSpecFromModel(t)
	return &spec, nil
}

// Update 只改元数据。name 与 status 不在入参里：改名打断授权，状态切换只走管理端。
func (a *customToolAdmin) Update(ctx context.Context, name string, in tools.CustomToolUpdate) (*tools.CustomToolSpec, error) {
	cur, err := a.store.Get(a.schema, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("自定义工具 %q 不存在", name)
		}
		return nil, err
	}
	// 与 agent / skill 同一条闸：锁定(2) / 废弃(3) 的工具不允许改。
	if !models.IsModifiable(cur.Status) {
		return nil, fmt.Errorf("自定义工具 %q 已锁定或废弃，先由管理员切回正常态", name)
	}
	// 以文件里的契约为基线再叠加本次改动（读路径文件优先）：
	// 从表行起改，一次更新就会把 TOOL.json 里手改过的契约悄悄盖回旧值。
	toolfile.Load(config.Cfg.Agent.RootDir, a.schema, cur)

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
		// 按键合并而不是整体替换：模型看不到已有 secret 值，整体替换会把它抹掉。
		// 显式空串仍然可以清空某一项。
		merged := cur.ConfigMap()
		if merged == nil {
			merged = make(map[string]any, len(in.Configs))
		}
		for k, v := range in.Configs {
			merged[k] = v
		}
		cur.Configs = xsql.JSONB{Data: merged}
	}

	// Upsert 而不是 Update：盘上有目录、表里没行的工具（手工放进来 / 从版本库拉下来）
	// 也要能改状态与配置 —— 缺行不该让「编辑」报一个查不到记录。
	if err := a.store.Upsert(cur); err != nil {
		return nil, fmt.Errorf("更新自定义工具失败: %w", err)
	}
	if err := a.writeContract(cur); err != nil {
		// 文件没写成时必须把它删掉：读路径「文件优先」，留着旧文件会让这次更新
		// 在下一轮读取里被旧契约盖回去（表改了、表现没变）。
		_ = os.Remove(toolfile.Path(config.Cfg.Agent.RootDir, a.schema, cur.Name))
		return nil, fmt.Errorf("设置已保存，但写入契约文件失败（已回退为表定义）: %w", err)
	}
	spec := tools.CustomToolSpecFromModel(cur)
	return &spec, nil
}

// writeContract 把表行写成 TOOL.json —— 契约文件的唯一写入口。
//
// 不在这里做「差异比对」：文件就几百字节，无条件重写比「先读再比再决定」少一条
// 可能出错的路径，也顺带保证格式稳定（同一份序列化函数）。
func (a *customToolAdmin) writeContract(t *models.CustomTool) error {
	return toolfile.WriteOf(config.Cfg.Agent.RootDir, a.schema, t)
}

// Delete 删表行 + 删目录。
//
// force=false 且仍被引用时返回 (refs, nil)：由工具层翻译成「拒绝 + 列出引用方」。
// 引用行不清理 —— 与 admin 同口径，误删后重建同名工具可自动恢复授权。
func (a *customToolAdmin) Delete(ctx context.Context, name string, force bool) (*tools.CustomToolRefs, error) {
	if _, err := a.store.Get(a.schema, name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("自定义工具 %q 不存在", name)
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
		return refs, fmt.Errorf("删除自定义工具失败: %w", err)
	}
	if dir := a.toolDir(name); dir != "" {
		if rerr := os.RemoveAll(dir); rerr != nil {
			// 行已删，目录残留只影响盘占用；不吞错但也不回滚（回滚会留下更奇怪的半态）。
			return refs, fmt.Errorf("已删除 %q，但清理目录失败: %w", name, rerr)
		}
	}
	// 版本域同样按 name 键：不清会让「删除后同名重建」在发布 v1 时撞上旧目录
	// （发布前置的 EnsureAbsent 会报 version exists，错误信息完全指不到这里）。
	ver := filepath.Join(config.Cfg.Agent.RootDir,
		filepath.FromSlash(paths.VersionEntityKey(a.schema, paths.VersionTools, name)))
	if rerr := os.RemoveAll(ver); rerr != nil {
		return refs, fmt.Errorf("已删除 %q，但清理版本目录失败: %w", name, rerr)
	}
	return refs, nil
}

// ListAll 全部工具（含锁定 / 废弃态）。
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

// customToolRefScope 授权 ref 的命名空间前缀（custom:<name>）。
// 与 toolgrant.ScopeCustom 同一个值：这里只拼查询串，不引 toolgrant 以免两包循环。
const customToolRefScope = "custom:"

// usages 查 custom:<name> 被谁引用（agent 授权 / skill 依赖 / 自定义组）。
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

// writeEntryFile 写实现入口文件（自动建父目录，支持 tools/<name>/lib/x.py 这类子目录）。
func writeEntryFile(dir, entry, content string) error {
	p := filepath.Join(dir, filepath.FromSlash(entry))
	// Join 之后再确认一次仍在工具目录内：filepath.Join 会 Clean，但上面的
	// entryInsideToolDir 只检查了相对路径形态，这里做最终兜底。
	if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		return fmt.Errorf("entry %q 解析后跳出工具目录", entry)
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
