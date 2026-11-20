package versioning

// Reading the "skill metadata" snapshot of the current version at runtime.
//
// Why the metadata is read from the version too: the description is the one line shown to the model in progressive
// disclosure, the visibility decides whether it appears at all, and the tool dependencies decide which extra tools exist
// once it has been read. Freezing only the body and not these items would mean "edit one description line, no release
// needed" still changes the catalogue the model sees — and then the version can no longer say "what the model saw at that
// time".
//
// Three rules:
//   - private skills: the metadata is in the skills column of the owning agent's version row (a private skill has no
//     version number of its own);
//   - tenant-level skills: the metadata is in the current row of skill_versions (including tool_deps);
//   - a failed read (for example the entity was never released) neither panics nor falls back to the draft: it returns
//     ErrNoReleasedVersion and the caller degrades to "just the name, no body / description" — see the handling in
//     skills.go.

import (
	"database/sql"
	"encoding/json"
	"errors"

	"flame/nexus/shared/db"
	"flame/nexus/shared/versions"
)

// SkillMeta is the metadata snapshot of one skill (the body is not here, it comes from the file list).
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

// ToolRefs is the declared tool dependency ref list (copied, so a caller cannot mutate the snapshot).
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

// AgentPrivateSkillMeta reads the private skill metadata in the agent's current version (name → meta).
//
// With no current version it returns ErrNoReleasedVersion — in a production run the agent itself would not start, so this
// is rarely reached; it returns an error rather than an empty map so "unreleased" is never disguised as "has no skills".
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

// TenantSkillMeta batch-reads the current version metadata of tenant-level skills (skill id → meta).
//
// The returned map contains only the skills that **have a current version**; the caller degrades a missing one to "just
// the name" — it must not substitute the draft description (that is exactly the hole being plugged).
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

// SkillToolDepsSnapshot freezes the tool dependencies declared by a tenant-level skill (written to skill_versions at
// release / backfill time).
//
// A dependency is a "declaration" and part of the definition; a grant (whether it can really be called) is always the
// current one — revoke must take effect immediately.
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

// pgUUIDArray turns a uuid list into an array literal lib/pq accepts.
//
// Pass it as text[] and cast with ::uuid[]: pq renders []string as a {a,b} literal, which cannot be passed as a uuid[]
// argument directly.
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
