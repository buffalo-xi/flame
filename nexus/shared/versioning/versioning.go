// Package versioning 版本化的「跨实体」部分：私有技能快照、租户枚举。
//
// 单个实体的发布/载入/删除 SQL 留在各自的 admin 包里 —— 版本表的镜像列必须和实体表
// 逐列对齐，放在一起才好审；这里只放三处都要用、且与具体实体列无关的东西。
//
// 为什么私有技能要在这里处理：它没有独立版本号，目录在 agent 根目录之内，
// 随所属 agent 的版本一起冻结（见 nexus/docs/terminology.md）。
//   - 正文进 agent_versions.files（由快照遍历 skills/ 得到）；
//   - 元数据进 agent_versions.skills（本包 PrivateSkillsSnapshot）；
//   - 已废弃（status=3）的技能不进快照（否则正式运行的 /agent 里还能 cat 到它）。
package versioning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// DeprecatedPrivateSkillDirs 已废弃私有技能的目录（相对 agent 目录），用于从快照里排除。
func DeprecatedPrivateSkillDirs(schema, agentID string) ([]string, error) {
	rows, err := db.DB.Query(`SELECT name FROM `+schema+`.skills
		WHERE agent_id = $1 AND status = $2`, agentID, models.StatusDeprecated)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, path.Join(paths.AgentSkillsSubdir, name))
	}
	return out, rows.Err()
}

// PrivateSkillsSnapshot 冻结私有技能的元数据与工具依赖（正文由文件清单覆盖）。
//
// 工具依赖是「声明」，属于定义；授权（agent_tool_grants / skill_tool_grants 的生效面）
// 是运行时事实，不进快照 —— revoke 必须立即生效。
func PrivateSkillsSnapshot(schema, agentID string) ([]byte, error) {
	rows, err := db.DB.Query(`SELECT id, name, COALESCE(description,''), COALESCE(metadata,'{}'::jsonb),
			COALESCE(user_invocable,false), COALESCE(command_dispatch,''), COALESCE(command_tool,''),
			COALESCE(restricted,true)
		FROM `+schema+`.skills
		WHERE agent_id = $1 AND status <> $2
		ORDER BY name ASC`, agentID, models.StatusDeprecated)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SkillMeta, 0, 4)
	index := map[string]int{} // skill id → out 下标（依赖表按 skill_id 关联）
	for rows.Next() {
		var id string
		var s SkillMeta
		var meta []byte
		if err := rows.Scan(&id, &s.Name, &s.Description, &meta, &s.UserInvocable,
			&s.CommandDispatch, &s.CommandTool, &s.Restricted); err != nil {
			return nil, err
		}
		if len(meta) > 0 {
			s.Metadata = json.RawMessage(meta)
		}
		index[id] = len(out)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	deps, err := db.DB.Query(`SELECT g.skill_id, g.item_type, g.ref
		FROM `+schema+`.skill_tool_grants g
		JOIN `+schema+`.skills s ON s.id = g.skill_id
		WHERE s.agent_id = $1 AND s.status <> $2
		ORDER BY g.skill_id, g.ref`, agentID, models.StatusDeprecated)
	if err != nil {
		return nil, err
	}
	defer deps.Close()
	for deps.Next() {
		var skillID string
		var d toolDep
		if err := deps.Scan(&skillID, &d.Type, &d.Ref); err != nil {
			return nil, err
		}
		if i, ok := index[skillID]; ok {
			out[i].ToolDeps = append(out[i].ToolDeps, d)
		}
	}
	if err := deps.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// KeptPrivateSkills 草稿 skills/ 下存在、但该版本快照里没有的私有技能名。
//
// 载入旧 agent 版本时会用到：这些技能是独立实体（skills 表有行），
// 版本里没有它往往只是「发布之后才建的」，删目录就会变成「有行、没正文」的空技能。
func KeptPrivateSkills(ctx context.Context, st storage.Storage, draftKey string, rawSkills []byte) ([]string, error) {
	var snaps []SkillMeta
	if len(rawSkills) > 0 {
		if err := json.Unmarshal(rawSkills, &snaps); err != nil {
			return nil, err
		}
	}
	inVersion := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		inVersion[s.Name] = true
	}

	entries, err := st.List(ctx, path.Join(draftKey, paths.AgentSkillsSubdir))
	if err != nil {
		return nil, nil // 没有 skills/ 目录 = 没有要保留的
	}
	var kept []string
	for _, e := range entries {
		if !e.IsDir || inVersion[e.Name] {
			continue
		}
		kept = append(kept, e.Name)
	}
	return kept, nil
}

// RestoreSkillRow 把版本快照里的元数据写回 skills 行（载入版本用）。
//
// 只写「定义」字段：名称 / 归属 / 状态是实体身份，不随版本走
// （改名、换归属、锁废都不该被一次载入悄悄改掉）。
func RestoreSkillRow(tx *sql.Tx, schema, skillID string, m SkillMeta) error {
	meta := "{}"
	if len(m.Metadata) > 0 {
		meta = string(m.Metadata)
	}
	_, err := tx.Exec(`UPDATE `+schema+`.skills
		SET description = $2, metadata = $3, user_invocable = $4,
		    command_dispatch = $5, command_tool = $6, restricted = $7, updated_at = now()
		WHERE id = $1`,
		skillID, m.Description, meta, m.UserInvocable, m.CommandDispatch, m.CommandTool, m.Restricted)
	return err
}

// RestoreSkillDeps 用版本里冻结的依赖声明重建某技能的 skill_tool_grants（载入版本用）。
//
// 只动「声明」表：agent_tool_grants 是运行时授权，revoke 必须立即生效，不在这里回滚。
func RestoreSkillDeps(tx *sql.Tx, schema, skillID string, raw []byte) error {
	var deps []toolDep
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &deps); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM `+schema+`.skill_tool_grants WHERE skill_id = $1`, skillID); err != nil {
		return err
	}
	for _, d := range deps {
		if _, err := tx.Exec(`INSERT INTO `+schema+`.skill_tool_grants (skill_id, item_type, ref)
			VALUES ($1, $2, $3)`, skillID, d.Type, d.Ref); err != nil {
			return err
		}
	}
	return nil
}

// EnsureSkillVisibility 载入版本后的可见性自愈。
//
// restricted 是「可见性声明」，随版本冻结；白名单（agent_skill_bindings 的 skill 侧）是授权，
// 永远取当前（revoke 必须立即生效）。两半被拆开回滚后能拼出一个从未真实存在过的状态：
// restricted=true + 白名单为空 = 谁都发现不了。载入旧版本时它几乎必然出现，
// 所以这里直接退回「全员可见」，由调用方在 warnings 里点名。
func EnsureSkillVisibility(tx *sql.Tx, schema, skillID string) (downgraded bool, err error) {
	var restricted bool
	if err := tx.QueryRow(`SELECT restricted FROM `+schema+`.skills WHERE id = $1`, skillID).
		Scan(&restricted); err != nil {
		return false, err
	}
	if !restricted {
		return false, nil
	}
	var hasWhitelist bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+schema+`.agent_skill_bindings
		WHERE skill_id = $1)`, skillID).Scan(&hasWhitelist); err != nil {
		return false, err
	}
	if hasWhitelist {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE `+schema+`.skills SET restricted = false, updated_at = now()
		WHERE id = $1`, skillID); err != nil {
		return false, err
	}
	return true, nil
}

// RestoreAgentPrivateSkills 把版本里冻结的私有技能元数据与依赖写回仍存在的私有技能行。
//
// 版本之后新建的私有技能不动（快照里没有它们，不等于要删）；已废弃的本来就不在快照里。
func RestoreAgentPrivateSkills(tx *sql.Tx, schema, agentID string, raw []byte) error {
	var snaps []SkillMeta
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &snaps); err != nil {
			return err
		}
	}
	for _, m := range snaps {
		var id string
		err := tx.QueryRow(`SELECT id FROM `+schema+`.skills
			WHERE agent_id = $1 AND name = $2`, agentID, m.Name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue // 该私有技能已被删除：目录在版本里，行没了就不复活
		}
		if err != nil {
			return err
		}
		if err := RestoreSkillRow(tx, schema, id, m); err != nil {
			return err
		}
		deps, derr := json.Marshal(m.ToolDeps)
		if derr != nil {
			return derr
		}
		if len(m.ToolDeps) == 0 {
			deps = []byte("[]")
		}
		if err := RestoreSkillDeps(tx, schema, id, deps); err != nil {
			return err
		}
	}
	return nil
}

// TenantSchemas 列出已开通的租户 schema（以是否建了 agents 表为准）。
//
// 回填命令用它遍历；platform / public / 系统 schema 排除在外。
func TenantSchemas() ([]string, error) {
	rows, err := db.DB.Query(`SELECT s.schema_name
		FROM information_schema.schemata s
		WHERE EXISTS (SELECT 1 FROM information_schema.tables t
		               WHERE t.table_schema = s.schema_name AND t.table_name = 'agents')
		  AND s.schema_name NOT IN ('public', 'platform', 'pg_catalog', 'information_schema')
		ORDER BY s.schema_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
