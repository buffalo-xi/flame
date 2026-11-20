// Package versioning holds the cross-entity part of versioning: private skill snapshots and tenant enumeration.
//
// The release / apply / delete SQL of a single entity stays in its own admin package — the mirror columns of a version
// table must line up with the entity table column by column, and keeping them together is what makes that reviewable;
// only the things all three sides need and that are independent of any specific entity column live here.
//
// Why private skills are handled here: they have no version number of their own, their directory sits inside the agent
// directory, and they are frozen together with the version of the agent that owns them (see nexus/docs/terminology.md).
//   - the body goes into agent_versions.files (obtained by walking skills/ during the snapshot);
//   - the metadata goes into agent_versions.skills (PrivateSkillsSnapshot in this package);
//   - a deprecated skill (status=3) does not enter the snapshot (otherwise /agent in a production run could still cat it).
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

// DeprecatedPrivateSkillDirs are the directories of deprecated private skills (relative to the agent directory), used to
// exclude them from a snapshot.
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

// PrivateSkillsSnapshot freezes the metadata and tool dependencies of private skills (the body is covered by the file
// list).
//
// A tool dependency is a "declaration" and part of the definition; a grant (the effective surface of agent_tool_grants /
// skill_tool_grants) is a runtime fact and does not enter the snapshot — revoke must take effect immediately.
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
	index := map[string]int{} // skill id → index in out (the dependency table is joined by skill_id)
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

// KeptPrivateSkills are the private skill names that exist under the draft skills/ but are absent from that version's
// snapshot.
//
// It is used when loading an old agent version: those skills are entities of their own (they have rows in the skills
// table), and their absence from the version usually just means "created after the release"; deleting the directory would
// leave an empty skill of "a row but no body".
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
		return nil, nil // no skills/ directory = nothing to keep
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

// RestoreSkillRow writes the metadata from a version snapshot back onto the skills row (used when loading a version).
//
// Only the "definition" fields are written: name / ownership / status are the entity's identity and do not travel with a
// version (a rename, an ownership change or a deprecation must not be silently undone by one load).
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

// RestoreSkillDeps rebuilds a skill's skill_tool_grants from the dependency declarations frozen in the version (used when
// loading a version).
//
// It only touches the "declaration" table: agent_tool_grants is a runtime grant, revoke must take effect immediately, and
// it is not rolled back here.
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

// EnsureSkillVisibility is the visibility self-heal after loading a version.
//
// restricted is a "visibility declaration" and is frozen with the version; the allowlist (the skill side of
// agent_skill_bindings) is a grant and is always the current one (revoke must take effect immediately). Rolling the two
// halves back separately can assemble a state that never really existed: restricted=true with an empty allowlist = nobody
// can discover it. That state almost inevitably appears when loading an old version, so it falls back to "visible to
// everyone" here and the caller names it in the warnings.
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

// RestoreAgentPrivateSkills writes the private skill metadata and dependencies frozen in a version back onto the private
// skill rows that still exist.
//
// A private skill created after that version is left alone (its absence from the snapshot does not mean it should be
// deleted); deprecated ones were never in the snapshot.
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
			continue // that private skill was deleted: the directory is in the version, but a missing row is not revived
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

// TenantSchemas lists the provisioned tenant schemas (a schema counts when it has an agents table).
//
// The backfill command walks them; platform / public / system schemas are excluded.
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
