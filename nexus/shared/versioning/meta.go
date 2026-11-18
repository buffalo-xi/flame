package versioning

// 运行时读「技能元数据」的当前版本快照。
//
// 为什么元数据也要读版本：描述是渐进式披露里给模型看的那一行，可见性决定它出不出现，
// 工具依赖决定读完之后多出哪些工具。只冻正文不冻这几项，就会出现「改一句描述、
// 不用发布」也改变了模型看到的清单口径 —— 版本就说不清「当时模型看到什么」。
//
// 三条口径：
//   - 私有技能：元数据在所属 agent 的版本行 skills 列（私有技能没有独立版本号）；
//   - 租户级技能：元数据在 skill_versions 当前行（含 tool_deps）；
//   - 读取失败（比如实体从未发布）不 panic、不退回草稿：返回 ErrNoReleasedVersion，
//     由调用方降级成「只有名字，没有正文/描述」—— 见 skills.go 的处理。

import (
	"database/sql"
	"encoding/json"
	"errors"

	"flame/nexus/shared/db"
	"flame/nexus/shared/versions"
)

// SkillMeta 一条技能的元数据快照（正文不在这里，走文件清单）。
type SkillMeta struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	UserInvocable   bool            `json:"user_invocable"`
	CommandDispatch string          `json:"command_dispatch,omitempty"`
	CommandTool     string          `json:"command_tool,omitempty"`
	Restricted      bool            `json:"restricted"`
	ToolDeps        []toolDep       `json:"tool_deps,omitempty"`
}

// ToolRefs 声明的工具依赖 ref 列表（复制一份，避免调用方改到快照）。
func (m SkillMeta) ToolRefs() []string {
	if len(m.ToolDeps) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.ToolDeps))
	for _, d := range m.ToolDeps {
		out = append(out, d.Ref)
	}
	return out
}

type toolDep struct {
	Type string `json:"type"` // group | tool
	Ref  string `json:"ref"`
}

// AgentPrivateSkillMeta 读 agent 当前版本里的私有技能元数据（name → meta）。
//
// 没有当前版本时返回 ErrNoReleasedVersion —— 正式运行时这个 agent 本身就起不来，
// 这里极少真的走到；返回错误而不是空 map，是为了不把「未发布」伪装成「没有技能」。
func AgentPrivateSkillMeta(schema, agentName string) (map[string]SkillMeta, error) {
	var raw []byte
	err := db.DB.QueryRow(`SELECT v.skills FROM `+schema+`.agent_versions v
		JOIN `+schema+`.agents a ON a.id = v.agent_id
		WHERE a.name = $1 AND v.is_current`, agentName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, versions.ErrNoReleasedVersion
	}
	if err != nil {
		return nil, err
	}
	var list []SkillMeta
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
	}
	out := make(map[string]SkillMeta, len(list))
	for _, m := range list {
		out[m.Name] = m
	}
	return out, nil
}

// TenantSkillMeta 批量读租户级技能当前版本的元数据（skill id → meta）。
//
// 返回的 map 只含**有当前版本**的技能；调用方对缺失项降级为「只给名字」，
// 不要拿草稿描述顶上去（那正是要堵的洞）。
func TenantSkillMeta(schema string, skillIDs []string) (map[string]SkillMeta, error) {
	if len(skillIDs) == 0 {
		return map[string]SkillMeta{}, nil
	}
	rows, err := db.DB.Query(`SELECT skill_id::text, name, COALESCE(description,''),
			COALESCE(metadata,'{}'::jsonb), COALESCE(user_invocable,false),
			COALESCE(command_dispatch,''), COALESCE(command_tool,''), COALESCE(restricted,true),
			COALESCE(tool_deps,'[]'::jsonb)
		FROM `+schema+`.skill_versions
		WHERE is_current AND skill_id = ANY($1::uuid[])`, pgUUIDArray(skillIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]SkillMeta, len(skillIDs))
	for rows.Next() {
		var id string
		var m SkillMeta
		var meta, deps []byte
		if err := rows.Scan(&id, &m.Name, &m.Description, &meta, &m.UserInvocable,
			&m.CommandDispatch, &m.CommandTool, &m.Restricted, &deps); err != nil {
			return nil, err
		}
		m.Metadata = json.RawMessage(meta)
		if len(deps) > 0 {
			_ = json.Unmarshal(deps, &m.ToolDeps)
		}
		out[id] = m
	}
	return out, rows.Err()
}

// SkillToolDepsSnapshot 冻结某租户级技能声明的工具依赖（发布 / 回填时写入 skill_versions）。
//
// 依赖是「声明」，属于定义；授权（能不能真的调到）永远取当前 —— revoke 必须立即生效。
func SkillToolDepsSnapshot(schema, skillID string) ([]byte, error) {
	rows, err := db.DB.Query(`SELECT item_type, ref FROM `+schema+`.skill_tool_grants
		WHERE skill_id = $1 ORDER BY ref`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	deps := make([]toolDep, 0, 4)
	for rows.Next() {
		var d toolDep
		if err := rows.Scan(&d.Type, &d.Ref); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(deps)
}

// pgUUIDArray 把 uuid 列表转成 lib/pq 认的数组字面量。
//
// 用 text[] 再 ::uuid[] 转：pq 对 []string 走的是 {a,b} 字面量，不能直接当 uuid[] 参数。
func pgUUIDArray(ids []string) string {
	b := make([]byte, 0, len(ids)*40+2)
	b = append(b, '{')
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, id...)
	}
	b = append(b, '}')
	return string(b)
}
