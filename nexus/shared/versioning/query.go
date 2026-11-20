package versioning

// The queries that resolve "the current version" at runtime: a production entry point must read the is_current
// row.
//
// It only does **reads**, and returns only two things: the version number plus the storage key of the version
// directory.
// The host path is assembled by the caller with rootDir (the sandbox mount source needs a real path; S3 does not
// support one and reports it explicitly).
//
// With no is_current it always returns versions.ErrNoReleasedVersion (carrying the entity name) and **never
// falls back to the draft** — a silent fallback would make the phrase "a production run" untrue.

import (
	"database/sql"
	"errors"
	"fmt"

	"flame/nexus/shared/db"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/versions"
)

// AgentCurrentVersion returns an agent's current version number and version directory key.
func AgentCurrentVersion(schema, agentName string) (int, string, error) {
	var v int
	err := db.DB.QueryRow(`SELECT v.version FROM `+schema+`.agent_versions v
		JOIN `+schema+`.agents a ON a.id = v.agent_id
		WHERE a.name = $1 AND v.is_current`, agentName).Scan(&v)
	return pick(v, err, schema, paths.VersionAgents, agentName, "agent")
}

// AgentCurrentAttributes returns the definition attributes frozen into an agent's current version: the two switches
// (tools_unrestricted / skill_discovery) and the default reply language (default_language).
//
// They come from the same source as the version's files: a production run must read the copy in the version.
// Reading the current value from the agents table would make "frozen at release" a lie — changing an attribute after
// the release would immediately change production behaviour (an implicit change unrelated to revoke).
// With no is_current it follows the same rule as AgentCurrentVersion: ErrNoReleasedVersion, no fallback to the
// current value.
func AgentCurrentAttributes(schema, agentName string) (toolsUnrestricted, skillDiscovery bool, defaultLanguage string, err error) {
	err = db.DB.QueryRow(`SELECT COALESCE(v.tools_unrestricted,false), COALESCE(v.skill_discovery,false),
			COALESCE(v.default_language,'')
		FROM `+schema+`.agent_versions v
		JOIN `+schema+`.agents a ON a.id = v.agent_id
		WHERE a.name = $1 AND v.is_current`, agentName).
		Scan(&toolsUnrestricted, &skillDiscovery, &defaultLanguage)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, "", fmt.Errorf("agent %q has no released version yet: %w", agentName, versions.ErrNoReleasedVersion)
	}
	return toolsUnrestricted, skillDiscovery, defaultLanguage, err
}

// TenantSkillCurrentVersion returns a tenant-level skill's current version number and version directory key.
//
// A private skill does not come through here: it has no version number of its own and reads the version
// directory of the agent it belongs to.
func TenantSkillCurrentVersion(schema, skillName string) (int, string, error) {
	var v int
	err := db.DB.QueryRow(`SELECT v.version FROM `+schema+`.skill_versions v
		JOIN `+schema+`.skills s ON s.id = v.skill_id
		WHERE s.name = $1 AND s.agent_id IS NULL AND v.is_current`, skillName).
		Scan(&v)
	return pick(v, err, schema, paths.VersionSkills, skillName, "skill")
}

// ToolCurrentVersion returns a custom tool's current version number and version directory key.
func ToolCurrentVersion(schema, toolName string) (int, string, error) {
	var v int
	err := db.DB.QueryRow(`SELECT v.version FROM `+schema+`.tool_versions v
		JOIN `+schema+`.custom_tools t ON t.id = v.tool_id
		WHERE t.name = $1 AND v.is_current`, toolName).Scan(&v)
	return pick(v, err, schema, paths.VersionTools, toolName, "tool")
}

// currentVersionTables are the version tables that may be queried in bulk: table name -> entity id column.
//
// Both identifiers are concatenated into SQL, so only the combinations on this allowlist are accepted and no
// external input ever is.
var currentVersionTables = map[string]string{
	"agent_versions": "agent_id",
	"flow_versions":  "flow_id",
	"skill_versions": "skill_id",
	"tool_versions":  "tool_id",
}

// CurrentVersions bulk-reads "the current version number": entity id -> version.
//
// Only the is_current row is taken, and an entity that was never released is not in the map (the caller treats
// it as 0).
// The list page uses this to finish in one query instead of one per card (N+1).
// It only answers "which version is production on"; "has the draft changed" still needs *VersionState (which
// compares files entity by entity).
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
		return 0, "", fmt.Errorf("%s %q has no released version yet: %w", label, name, versions.ErrNoReleasedVersion)
	}
	if err != nil {
		return 0, "", err
	}
	return v, paths.VersionKey(schema, kind, name, v), nil
}
