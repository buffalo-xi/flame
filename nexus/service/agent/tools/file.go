package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"flame/nexus/shared/quota"
	"flame/nexus/shared/storage"
)

// File tools: read_file / write_file / edit_file / list_files / delete_file / file_exists
// All of them go through storage.Storage, with paths locked into the workspace by safePath.

// fileReadTool reads file content.
type fileReadTool struct{}

func (t *fileReadTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "read_file",
		Category:    CategoryFile,
		Description: "Read a text file in the workspace. Returns content, size and exists.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File path relative to the workspace; a leading \"/\" resolves from the tenant root: a supervisor reaches the whole tenant (e.g. /agents/poet/AGENT.md), a non-supervisor reads its own definition directory (e.g. /agents/<your-name>/AGENT.md), of which only /agents/<your-name>/skills/<skill>/ is writable"},
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

// fileWriteTool writes or appends file content.
type fileWriteTool struct{}

func (t *fileWriteTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "write_file",
		Category:    CategoryFile,
		Description: "Write (or append to) a text file in the workspace, creating parent directories as needed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path relative to the workspace; a leading \"/\" resolves from the tenant root: a supervisor reaches the whole tenant (e.g. /agents/poet/AGENT.md), a non-supervisor can only write its own private skill directory (e.g. /agents/<your-name>/skills/<skill>/), and its own AGENT.md is read-only. Files in the workspace are not agent/skill entities — create entities with the meta tools"},
				"content": map[string]any{"type": "string", "description": "Text to write"},
				"append":  map[string]any{"type": "boolean", "description": "Whether to append (default: overwrite)"},
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
		oldSize = sz // overwrite: the real delta is new minus old
	}

	// Only writes into the session workspace consume quota; definition-domain writes such as /agents/... are not affected
	schema, qkey, qok := quotaScope(c, sp)
	delta := int64(len(content)) - oldSize
	var evicted []string
	if qok && delta > 0 {
		ev, qerr := quota.Admit(ctx, c.Storage, schema, qkey, c.UserID, delta)
		if qerr != nil {
			return nil, quotaExceededError(qerr, ev)
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
		// Let the model know that old files were evicted by the quota, so it stops referencing deleted paths
		out["evicted"] = evicted
	}
	return out, nil
}

// quotaExceededError keeps the quota error intact (errors.As still works) while telling the model which files the
// pre-check already deleted to make room. Without that the model keeps referencing paths that no longer exist.
func quotaExceededError(err error, evicted []string) error {
	if len(evicted) == 0 {
		return err
	}
	return fmt.Errorf("%w (files deleted to make room: %s)", err, strings.Join(evicted, ", "))
}

// quotaFreed measures the bytes an entry occupies before it is deleted: a file's size, or a recursive sum for a directory.
// It only ever gives quota back, so an imprecise estimate costs one extra calibration scan later, never correctness.
func quotaFreed(ctx context.Context, st storage.Storage, key string) int64 {
	if sz, _, err := st.Stat(ctx, key); err == nil {
		return sz
	}
	w, ok := st.(storage.Walker)
	if !ok {
		return 0
	}
	var total int64
	_ = w.WalkPrefix(ctx, key, func(_ string, size int64, _ time.Time) error {
		total += size
		return nil
	})
	return total
}

// quotaScope decides whether this write lands in the session workspace (the only place that consumes session quota).
// sp is a storage-relative path; c.WorkKey is the session root, and definition domains such as /agents/... never count.
func quotaScope(c *Context, sp string) (schema, key string, ok bool) {
	if c == nil || c.SessionID == "" || c.WorkKey == "" {
		return "", "", false
	}
	if sp != c.WorkKey && !strings.HasPrefix(sp, c.WorkKey+"/") {
		return "", "", false
	}
	return c.Schema, c.WorkKey, true
}

// fileEditTool edits part of a file's text (old -> new).
type fileEditTool struct{}

func (t *fileEditTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "edit_file",
		Category:    CategoryFile,
		Description: "Edit text inside a file: replace old with new (only the first match by default).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string", "description": "File path relative to the workspace; a leading \"/\" resolves from the tenant root: a supervisor reaches the whole tenant (e.g. /agents/poet/AGENT.md), a non-supervisor can only write its own private skill directory (e.g. /agents/<your-name>/skills/<skill>/), and its own AGENT.md is read-only. Files in the workspace are not agent/skill entities — create entities with the meta tools"},
				"old":   map[string]any{"type": "string", "description": "Old text fragment to replace"},
				"new":   map[string]any{"type": "string", "description": "Replacement text fragment"},
				"count": map[string]any{"type": "number", "description": "How many matches to replace (default 1; -1 = all)"},
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
		count = 1 // replace the first match by default
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
			return nil, quotaExceededError(qerr, ev)
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

// fileListTool lists a directory's content.
type fileListTool struct{}

func (t *fileListTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "list_files",
		Category:    CategoryFile,
		Description: "List the files and directories under a directory in the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dir": map[string]any{"type": "string", "description": "Directory path relative to the workspace (empty = root); a leading \"/\" resolves from the tenant root: a supervisor reaches the whole tenant (e.g. /agents/poet), a non-supervisor reads its own definition directory (e.g. /agents/<your-name>), of which only /agents/<your-name>/skills/ is writable"},
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

// fileDeleteTool deletes a file or directory (a directory is deleted recursively, see Execute).
type fileDeleteTool struct{}

func (t *fileDeleteTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "delete_file",
		Category:    CategoryFile,
		Description: "Delete a file in the workspace; if the target is a directory, it is deleted recursively with everything inside (not recoverable).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Path relative to the workspace; a leading \"/\" resolves from the tenant root: a supervisor reaches the whole tenant (e.g. /agents/poet/AGENT.md), a non-supervisor can only write its own private skill directory (e.g. /agents/<your-name>/skills/<skill>/), and its own AGENT.md is read-only"},
			},
			"required": []any{"path"},
		},
	}
}

// Execute always deletes once as a file and once as a directory, with no recursive flag - either way it is a delete.
//
//	Delete       : file / empty directory (local: os.Remove; S3: delete that object key)
//	DeletePrefix : the directory and all its descendants (local: os.RemoveAll; S3: batch-delete the prefix)
//
// It deliberately avoids deciding "file or directory" - storage has no reliable IsDir: local's Exists is true for
// directories too, and S3's List(file path) returns an empty list because prefixOf appends "/", which is indistinguishable
// from an empty directory. The admin-side DeleteAgentEntry makes the same trade-off (admin/agents/files.go).
//
// Only DeletePrefix serves as the failure criterion: it is the "catch-all" operation, so success means the target no longer
// exists; Delete returns success for any key on S3 and necessarily fails on a non-empty directory locally, so neither works
// as a criterion. The side effect is that deleting a non-existent path also succeeds (idempotent) - the same as S3's
// original behaviour, i.e. local is brought to the same rule.
//
// One exception: SKILL.md under root addressing may not be deleted on its own (see the guard below).
func (t *fileDeleteTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	sp, rel, fromRoot, err := resolveWrite(ctx, c, argString(args, "path"))
	if err != nil {
		return nil, err
	}
	// SKILL.md is a skill's entry file: deleting it would leave an empty skill "registered in the database but with no
	// body", and at run time an unreadable body **degrades silently to empty content** (see skillManager.Get). The admin API
	// has long protected it (isSkillEntry in admin/skills/files.go); this fills the gap - so all three entries (admin /
	// super agent / non-super agent) follow the same rule. To delete a whole skill use delete_own_skill /
	// delete_agent_skill / delete_shared_skill.
	if fromRoot && strings.EqualFold(path.Base(rel), skillEntryFile) {
		if tgt, e := resolveTarget(rel); e == nil && tgt.kind == "skill" {
			return nil, fmt.Errorf("%s is the skill entry file and cannot be deleted on its own; use delete_own_skill / delete_agent_skill / delete_shared_skill to delete the whole skill", skillEntryFile)
		}
	}
	// Measure before deleting: deleting frees quota and the accounting has to hear about it. Without this the workspace
	// keeps looking full after the agent cleans up (the admin-side DeleteSessionEntry does the same), so the agent would
	// keep deleting one file per turn until the next calibration scan.
	schema, qkey, qok := quotaScope(c, sp)
	var freed int64
	if qok {
		freed = quotaFreed(ctx, c.Storage, sp)
	}
	_ = c.Storage.Delete(ctx, sp)
	if err := c.Storage.DeletePrefix(ctx, sp); err != nil {
		return nil, err
	}
	if qok && freed > 0 {
		quota.Commit(ctx, schema, qkey, c.UserID, -freed)
	}
	return map[string]any{"path": rel, "deleted": true}, nil
}

// fileExistsTool reports whether a file exists.
type fileExistsTool struct{}

func (t *fileExistsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "file_exists",
		Category:    CategoryFile,
		Description: "Check whether a file exists in the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File path relative to the workspace; a leading \"/\" resolves from the tenant root: a supervisor reaches the whole tenant (e.g. /agents/poet/AGENT.md), a non-supervisor reads its own definition directory (e.g. /agents/<your-name>/AGENT.md), of which only /agents/<your-name>/skills/<skill>/ is writable"},
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
