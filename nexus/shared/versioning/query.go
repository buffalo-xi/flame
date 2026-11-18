package versioning

// 运行时解析「当前版本」的查询：正式入口必须读 is_current 那一份。
//
// 只做**读**，且只回两类东西：版本号 + 版本目录的存储 key。
// 宿主路径由调用方用 rootDir 拼（沙箱挂载源需要真实路径，S3 不支持，会明确报错）。
//
// 没有 is_current 时一律返回 versions.ErrNoReleasedVersion（带实体名），
// **不退回草稿** —— 静默退回等于「正式运行」这句话不成立。

import (
	"database/sql"
	"errors"
	"fmt"

	"flame/nexus/shared/db"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/versions"
)

// AgentCurrentVersion 返回 agent 的当前版本号与版本目录 key。
func AgentCurrentVersion(schema, agentName string) (int, string, error) {
	var v int
	err := db.DB.QueryRow(`SELECT v.version FROM `+schema+`.agent_versions v
		JOIN `+schema+`.agents a ON a.id = v.agent_id
		WHERE a.name = $1 AND v.is_current`, agentName).Scan(&v)
	return pick(v, err, schema, paths.VersionAgents, agentName, "智能体")
}

// AgentCurrentSwitches 返回 agent 当前版本里冻结的两个「定义型开关」：
// 不限制工具（tools_unrestricted）与「允许发现未绑定的技能」（skill_discovery）。
//
// 它们与版本里的文件同源：正式运行必须读版本里那一份。读 agents 表当前值会让
// 「发布时冻结」名不副实 —— 发布后再改开关会立刻改变线上行为（与 revoke 无关的隐式变更）。
// 没有 is_current 时与 AgentCurrentVersion 同口径：返回 ErrNoReleasedVersion，不退回当前值。
func AgentCurrentSwitches(schema, agentName string) (toolsUnrestricted, skillDiscovery bool, err error) {
	err = db.DB.QueryRow(`SELECT v.tools_unrestricted, v.skill_discovery FROM `+schema+`.agent_versions v
		JOIN `+schema+`.agents a ON a.id = v.agent_id
		WHERE a.name = $1 AND v.is_current`, agentName).Scan(&toolsUnrestricted, &skillDiscovery)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, fmt.Errorf("智能体 %q 尚未发布正式版本: %w", agentName, versions.ErrNoReleasedVersion)
	}
	return toolsUnrestricted, skillDiscovery, err
}

// TenantSkillCurrentVersion 返回租户级技能的当前版本号与版本目录 key。
//
// 私有技能不走这里：它没有独立版本号，读的是所属 agent 的版本目录。
func TenantSkillCurrentVersion(schema, skillName string) (int, string, error) {
	var v int
	err := db.DB.QueryRow(`SELECT v.version FROM `+schema+`.skill_versions v
		JOIN `+schema+`.skills s ON s.id = v.skill_id
		WHERE s.name = $1 AND s.agent_id IS NULL AND v.is_current`, skillName).
		Scan(&v)
	return pick(v, err, schema, paths.VersionSkills, skillName, "技能")
}

// ToolCurrentVersion 返回自定义工具的当前版本号与版本目录 key。
func ToolCurrentVersion(schema, toolName string) (int, string, error) {
	var v int
	err := db.DB.QueryRow(`SELECT v.version FROM `+schema+`.tool_versions v
		JOIN `+schema+`.custom_tools t ON t.id = v.tool_id
		WHERE t.name = $1 AND v.is_current`, toolName).Scan(&v)
	return pick(v, err, schema, paths.VersionTools, toolName, "工具")
}

// currentVersionTables 允许批量查询的版本表：表名 → 实体 id 列。
//
// 这两个标识都要拼进 SQL，所以只认白名单里的组合，绝不接受外部输入。
var currentVersionTables = map[string]string{
	"agent_versions": "agent_id",
	"flow_versions":  "flow_id",
	"skill_versions": "skill_id",
	"tool_versions":  "tool_id",
}

// CurrentVersions 批量读「当前版本号」：实体 id → version。
//
// 只取 is_current 那一行，没发布过的实体不在 map 里（调用方按 0 处理）。
// 列表页用这个一次查完，避免每张卡片各查一次（N+1）。
// 它只回答「线上是第几版」；「草稿有没有改动」仍要用 *VersionState（那要逐实体比文件）。
func CurrentVersions(schema, table string) (map[string]int, error) {
	idCol, ok := currentVersionTables[table]
	if !ok {
		return nil, fmt.Errorf("unsupported version table %q", table)
	}
	rows, err := db.DB.Query(`SELECT ` + idCol + `::text, version FROM ` + schema + `.` + table + ` WHERE is_current`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var (
			id string
			v  int
		)
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

func pick(v int, err error, schema string, kind paths.VersionKind, name, label string) (int, string, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("%s %q 尚未发布正式版本: %w", label, name, versions.ErrNoReleasedVersion)
	}
	if err != nil {
		return 0, "", err
	}
	return v, paths.VersionKey(schema, kind, name, v), nil
}
