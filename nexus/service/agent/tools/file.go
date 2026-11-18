package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"flame/nexus/shared/quota"
)

// 文件工具：read_file / write_file / edit_file / list_files / delete_file / file_exists
// 全部走 storage.Storage，路径经 safePath 锁进工作区。

// fileReadTool 读取文件内容。
type fileReadTool struct{}

func (t *fileReadTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "read_file",
		Category:    CategoryFile,
		Description: "读取工作区内的文本文件内容，返回 content、size、exists。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "相对工作区的文件路径；以 \"/\" 开头表示从租户根算：超级智能体可及全租户（如 /agents/poet/AGENT.md），非超级智能体可读自己的定义目录（如 /agents/你的名字/AGENT.md），其中只有 /agents/你的名字/skills/技能名/ 可写"},
			},
			"required": []any{"path"},
		},
	}
}

func (t *fileReadTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	sp, _, _, err := resolveRead(c, argString(args, "path"))
	if err != nil {
		return nil, err
	}

	exists, err := c.Storage.Exists(ctx, sp)
	if err != nil {
		return nil, err
	}
	if !exists {
		return map[string]any{"content": "", "size": 0, "exists": false}, nil
	}

	data, err := c.Storage.ReadFile(ctx, sp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"content": string(data), "size": len(data), "exists": true}, nil
}

// fileWriteTool 写入或追加文件内容。
type fileWriteTool struct{}

func (t *fileWriteTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "write_file",
		Category:    CategoryFile,
		Description: "写入（或追加）工作区内的文本文件，自动创建父目录。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "相对工作区的文件路径；以 \"/\" 开头表示从租户根算：超级智能体可及全租户（如 /agents/poet/AGENT.md），非超级智能体只能写自己的私有 skill 目录（如 /agents/你的名字/skills/技能名/），自己的 AGENT.md 只读。工作区内的文件不是 agent/skill 实体，实体须用元工具创建"},
				"content": map[string]any{"type": "string", "description": "要写入的文本内容"},
				"append":  map[string]any{"type": "boolean", "description": "是否追加（默认覆盖）"},
			},
			"required": []any{"path", "content"},
		},
	}
}

func (t *fileWriteTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	sp, rel, _, err := resolveWrite(ctx, c, argString(args, "path"))
	if err != nil {
		return nil, err
	}
	content := argString(args, "content")

	oldSize := int64(0)
	if argBool(args, "append") {
		exists, err := c.Storage.Exists(ctx, sp)
		if err != nil {
			return nil, err
		}
		if exists {
			old, err := c.Storage.ReadFile(ctx, sp)
			if err != nil {
				return nil, err
			}
			oldSize = int64(len(old))
			content = string(old) + content
		}
	} else if sz, _, serr := c.Storage.Stat(ctx, sp); serr == nil {
		oldSize = sz // 覆盖写：用 new-old 计算真实增量
	}

	// 只有写进「会话工作区」才占配额；写 /agents/... 这类定义域不受影响
	schema, qkey, qok := quotaScope(c, sp)
	delta := int64(len(content)) - oldSize
	var evicted []string
	if qok && delta > 0 {
		ev, qerr := quota.Admit(ctx, c.Storage, schema, qkey, c.UserID, delta)
		if qerr != nil {
			return nil, qerr
		}
		evicted = ev
	}

	if err := c.Storage.WriteFile(ctx, sp, []byte(content)); err != nil {
		return nil, err
	}
	if qok && delta != 0 {
		quota.Commit(ctx, schema, qkey, c.UserID, delta)
	}

	out := map[string]any{"path": rel, "written": len(content)}
	if len(evicted) > 0 {
		// 让模型知道有旧文件被配额清理掉了，避免它继续引用已删除的路径
		out["evicted"] = evicted
	}
	return out, nil
}

// quotaScope 判断这次写入是否落在会话工作区里（只有它会占用会话配额）。
// sp 是存储相对路径；c.WorkKey 是会话根，/agents/... 之类的定义域一律不算。
func quotaScope(c *Context, sp string) (schema, key string, ok bool) {
	if c == nil || c.SessionID == "" || c.WorkKey == "" {
		return "", "", false
	}
	if sp != c.WorkKey && !strings.HasPrefix(sp, c.WorkKey+"/") {
		return "", "", false
	}
	return c.Schema, c.WorkKey, true
}

// fileEditTool 编辑文件中的部分文本内容（old → new）。
type fileEditTool struct{}

func (t *fileEditTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "edit_file",
		Category:    CategoryFile,
		Description: "编辑文件内的文本：把 old 替换为 new（默认只替换第一处匹配）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string", "description": "相对工作区的文件路径；以 \"/\" 开头表示从租户根算：超级智能体可及全租户（如 /agents/poet/AGENT.md），非超级智能体只能写自己的私有 skill 目录（如 /agents/你的名字/skills/技能名/），自己的 AGENT.md 只读。工作区内的文件不是 agent/skill 实体，实体须用元工具创建"},
				"old":   map[string]any{"type": "string", "description": "要替换的旧文本片段"},
				"new":   map[string]any{"type": "string", "description": "替换后的新文本片段"},
				"count": map[string]any{"type": "number", "description": "替换次数（默认 1，-1=全部）"},
			},
			"required": []any{"path", "old", "new"},
		},
	}
}

func (t *fileEditTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	sp, rel, _, err := resolveWrite(ctx, c, argString(args, "path"))
	if err != nil {
		return nil, err
	}
	old := argString(args, "old")
	if old == "" {
		return nil, errors.New("old text must not be empty")
	}

	data, err := c.Storage.ReadFile(ctx, sp)
	if err != nil {
		return nil, err
	}
	content := string(data)

	total := strings.Count(content, old)
	if total == 0 {
		return nil, fmt.Errorf("old text not found in %s", rel)
	}

	count := argInt(args, "count")
	if count == 0 {
		count = 1 // 默认替换第一处
	}

	result := strings.Replace(content, old, argString(args, "new"), count)

	replaced := total
	if count > 0 && count < total {
		replaced = count
	}

	schema, qkey, qok := quotaScope(c, sp)
	delta := int64(len(result)) - int64(len(data))
	var evicted []string
	if qok && delta > 0 {
		ev, qerr := quota.Admit(ctx, c.Storage, schema, qkey, c.UserID, delta)
		if qerr != nil {
			return nil, qerr
		}
		evicted = ev
	}

	if err := c.Storage.WriteFile(ctx, sp, []byte(result)); err != nil {
		return nil, err
	}
	if qok && delta != 0 {
		quota.Commit(ctx, schema, qkey, c.UserID, delta)
	}

	out := map[string]any{"path": rel, "replaced": replaced}
	if len(evicted) > 0 {
		out["evicted"] = evicted
	}
	return out, nil
}

// fileListTool 列出目录内容。
type fileListTool struct{}

func (t *fileListTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "list_files",
		Category:    CategoryFile,
		Description: "列出工作区内某个目录下的文件/目录名。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dir": map[string]any{"type": "string", "description": "相对工作区的目录路径（空=根）；以 \"/\" 开头表示从租户根算：超级智能体可及全租户（如 /agents/poet），非超级智能体可读自己的定义目录（如 /agents/你的名字），其中只有 /agents/你的名字/skills/ 可写"},
			},
		},
	}
}

func (t *fileListTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	sp, rel, _, err := resolveRead(c, argString(args, "dir"))
	if err != nil {
		return nil, err
	}
	entries, err := c.Storage.List(ctx, sp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"dir": rel, "entries": entries}, nil
}

// fileDeleteTool 删除文件或目录（目录递归删，见 Execute）。
type fileDeleteTool struct{}

func (t *fileDeleteTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "delete_file",
		Category:    CategoryFile,
		Description: "删除工作区内的文件；若目标是目录则连同其全部内容递归删除（不可恢复）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "相对工作区的路径；以 \"/\" 开头表示从租户根算：超级智能体可及全租户（如 /agents/poet/AGENT.md），非超级智能体只能写自己的私有 skill 目录（如 /agents/你的名字/skills/技能名/），自己的 AGENT.md 只读"},
			},
			"required": []any{"path"},
		},
	}
}

// Execute 一律按「文件 + 目录」各删一次，不加 recursive 参数 —— 反正都是删。
//
//	Delete       ：文件 / 空目录（local: os.Remove；S3: 删那个对象键）
//	DeletePrefix ：目录及其全部子孙（local: os.RemoveAll；S3: 批删前缀）
//
// 刻意不做「文件还是目录」判定 —— storage 层没有可靠的 IsDir：local 的 Exists 对目录
// 同样为真；S3 的 List(文件路径) 因 prefixOf 补了 "/" 而返回空列表，与空目录无法区分。
// 管理端 DeleteAgentEntry 同款取舍（admin/agents/files.go）。
//
// 失败判据只认 DeletePrefix：它是"通吃"操作，成功即代表目标已不存在；而 Delete 在 S3 上
// 对任意键都返回成功、在 local 上删非空目录必然失败，都不适合当判据。副作用是删除
// 不存在的路径也算成功（幂等）—— 与 S3 原本的行为一致，即把 local 收敛到同一口径。
//
// 一处例外：根寻址下的 SKILL.md 不允许单独删除（见下方守卫）。
func (t *fileDeleteTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	sp, rel, fromRoot, err := resolveWrite(ctx, c, argString(args, "path"))
	if err != nil {
		return nil, err
	}
	// SKILL.md 是技能的入口文件：删掉会留下「登记在库、没有正文」的空技能，而运行时
	// 读不到正文会**静默降级为空内容**（见 skillManager.Get）。管理端接口早已保护
	// （admin/skills/files.go 的 isSkillEntry），这里补齐 —— 让三个入口（管理端 /
	// 超级智能体 / 非超级智能体）口径一致。要删整个技能请用 delete_own_skill / delete_agent_skill /
	// delete_shared_skill。
	if fromRoot && strings.EqualFold(path.Base(rel), skillEntryFile) {
		if tgt, e := resolveTarget(rel); e == nil && tgt.kind == "skill" {
			return nil, fmt.Errorf("%s 是技能入口文件，不允许单独删除；删除整个技能请用 delete_own_skill / delete_agent_skill / delete_shared_skill", skillEntryFile)
		}
	}
	_ = c.Storage.Delete(ctx, sp)
	if err := c.Storage.DeletePrefix(ctx, sp); err != nil {
		return nil, err
	}
	return map[string]any{"path": rel, "deleted": true}, nil
}

// fileExistsTool 判断文件是否存在。
type fileExistsTool struct{}

func (t *fileExistsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "file_exists",
		Category:    CategoryFile,
		Description: "判断工作区内的文件是否存在。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "相对工作区的文件路径；以 \"/\" 开头表示从租户根算：超级智能体可及全租户（如 /agents/poet/AGENT.md），非超级智能体可读自己的定义目录（如 /agents/你的名字/AGENT.md），其中只有 /agents/你的名字/skills/技能名/ 可写"},
			},
			"required": []any{"path"},
		},
	}
}

func (t *fileExistsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	sp, _, _, err := resolveRead(c, argString(args, "path"))
	if err != nil {
		return nil, err
	}
	exists, err := c.Storage.Exists(ctx, sp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"exists": exists}, nil
}

func init() {
	Register(&fileReadTool{})
	Register(&fileWriteTool{})
	Register(&fileEditTool{})
	Register(&fileListTool{})
	Register(&fileDeleteTool{})
	Register(&fileExistsTool{})
}
