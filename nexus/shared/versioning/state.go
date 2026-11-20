package versioning

// Version state (not a DB column): the current version number plus whether the draft has unreleased changes.
//
// A flow's definition lives in database columns, so the comparison can be written in SQL (see fillVersionState in
// repository/flows/versions.go). The definition of an agent / skill / tool has two halves, "columns + directory":
//   - the metadata (description / visibility / argument schema / tool dependencies) is in the version row and is compared
//     with SQL IS DISTINCT FROM;
//   - the body is the directory and can only be compared file by file against the version's file list, by sha256.
//
// Comparing only the files and not the metadata would produce "the description was changed, the UI says it is released,
// production still runs the old one" — exactly the kind of lie the version system exists to prevent. So both halves
// count.
//
// When the version information cannot be read it returns (0, false): the same rule as flows, so a failed version query
// never breaks the detail page.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"reflect"
	"strings"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/versions"
)

// SkillVersionState is the version state of a tenant-level skill.
//
// A private skill has no version number of its own (its version is the version of the agent it belongs to) and therefore
// always returns (0, false); the frontend shows "follows the owning agent's version" instead of "unreleased".
func SkillVersionState(ctx context.Context, st storage.Storage, schema string, s *models.Skill) (int, bool) {
	if string(s.AgentID) != "" {
		return 0, false
	}
	var (
		version     int
		rawFiles    []byte
		rawDeps     []byte
		metaChanged bool
	)
	err := db.DB.QueryRow(`SELECT v.version, COALESCE(v.files,'[]'::jsonb), COALESCE(v.tool_deps,'[]'::jsonb),
			(v.description IS DISTINCT FROM s.description
			 OR v.metadata::text IS DISTINCT FROM s.metadata::text
			 OR v.user_invocable IS DISTINCT FROM s.user_invocable
			 OR v.command_dispatch IS DISTINCT FROM s.command_dispatch
			 OR v.command_tool IS DISTINCT FROM s.command_tool
			 OR v.restricted IS DISTINCT FROM s.restricted)
		FROM `+schema+`.skill_versions v
		JOIN `+schema+`.skills s ON s.id = v.skill_id
		WHERE v.skill_id = $1 AND v.is_current`, s.ID).
		Scan(&version, &rawFiles, &rawDeps, &metaChanged)
	if err != nil {
		return 0, false
	}
	if metaChanged {
		return version, true
	}

	// Tool dependencies: a declaration is part of the definition. The same query as at release time (ORDER BY ref), decoded
	// into a slice before comparing — comparing the JSON bytes directly would be affected by key order / whitespace.
	curDeps, derr := SkillToolDepsSnapshot(schema, s.ID)
	if derr != nil {
		return version, true
	}
	if !sameDeps(rawDeps, curDeps) {
		return version, true
	}

	files, ferr := versions.Decode(rawFiles)
	if ferr != nil {
		return version, true
	}
	if st == nil {
		return version, false
	}
	changed, cerr := DraftChanged(ctx, st, path.Join(paths.SkillsKey(schema), s.Name), files, nil)
	if cerr != nil {
		return version, true
	}
	return version, changed
}

// ToolVersionState is the version state of a custom tool (the directory is the entity, there is no rename, so it is keyed
// by name only).
//
// configs is not compared: it is a runtime fact (keys / switches), not a definition; at release time it is stored as '{}'
// anyway.
func ToolVersionState(ctx context.Context, st storage.Storage, schema, toolName string) (int, bool) {
	var (
		version     int
		rawFiles    []byte
		metaChanged bool
	)
	err := db.DB.QueryRow(`SELECT v.version, COALESCE(v.files,'[]'::jsonb),
			(v.display_name IS DISTINCT FROM t.display_name
			 OR v.description IS DISTINCT FROM t.description
			 OR v.parameters::text IS DISTINCT FROM t.parameters::text
			 OR v.output::text IS DISTINCT FROM t.output::text
			 OR v.lang IS DISTINCT FROM t.lang
			 OR v.entry IS DISTINCT FROM t.entry
			 OR v.timeout_ms IS DISTINCT FROM t.timeout_ms
			 OR v.max_output_bytes IS DISTINCT FROM t.max_output_bytes
			 OR v.egress IS DISTINCT FROM t.egress
			 OR v.config_fields::text IS DISTINCT FROM t.config_fields::text)
		FROM `+schema+`.tool_versions v
		JOIN `+schema+`.custom_tools t ON t.id = v.tool_id
		WHERE t.name = $1 AND v.is_current`, toolName).
		Scan(&version, &rawFiles, &metaChanged)
	if err != nil {
		return 0, false
	}
	if metaChanged {
		return version, true
	}
	files, derr := versions.Decode(rawFiles)
	if derr != nil {
		return version, true
	}
	if st == nil {
		return version, false
	}
	changed, cerr := DraftChanged(ctx, st, path.Join(paths.ToolsKey(schema), toolName), files, nil)
	if cerr != nil {
		return version, true
	}
	return version, changed
}

// AgentVersionState is the version state of an agent.
//
// All three parts have to be compared, or something is missed:
//  1. the definition columns of agents (description / config / the two switches / the default reply language);
//  2. the **metadata** of private skills (description / visibility / tool dependencies) — it lives in the version row's
//     skills column;
//  3. the body directory (AGENT.md + private skill files), excluding deprecated private skills (they are excluded at
//     release time too).
func AgentVersionState(ctx context.Context, st storage.Storage, schema, agentID, agentName string) (int, bool) {
	var (
		version     int
		rawFiles    []byte
		rawSkills   []byte
		metaChanged bool
	)
	err := db.DB.QueryRow(`SELECT v.version, COALESCE(v.files,'[]'::jsonb), COALESCE(v.skills,'[]'::jsonb),
			(v.description IS DISTINCT FROM a.description
			 OR v.config::text IS DISTINCT FROM a.config::text
			 OR v.tools_unrestricted IS DISTINCT FROM a.tools_unrestricted
			 OR v.skill_discovery IS DISTINCT FROM a.skill_discovery
			 OR v.default_language IS DISTINCT FROM a.default_language)
		FROM `+schema+`.agent_versions v
		JOIN `+schema+`.agents a ON a.id = v.agent_id
		WHERE v.agent_id = $1 AND v.is_current`, agentID).
		Scan(&version, &rawFiles, &rawSkills, &metaChanged)
	if err != nil {
		return 0, false
	}
	if metaChanged {
		return version, true
	}

	// Private skill metadata: the same query and the same ORDER BY name as at release time, compared field by field
	now, serr := PrivateSkillsSnapshot(schema, agentID)
	if serr != nil || !sameSkillSnapshot(rawSkills, now) {
		return version, true
	}

	files, derr := versions.Decode(rawFiles)
	if derr != nil {
		return version, true
	}
	if st == nil {
		return version, false
	}
	// The same exclusion rule as at release time: deprecated private skills do not enter the snapshot, so they must not
	// count as "changed" here either
	skip, kerr := DeprecatedPrivateSkillDirs(schema, agentID)
	if kerr != nil {
		return version, true
	}
	changed, cerr := DraftChanged(ctx, st, paths.AgentsKey(schema, agentName), files, skip)
	if cerr != nil {
		return version, true
	}
	return version, changed
}

// sameSkillSnapshot compares two private skill metadata snapshots.
// Both are decoded into []SkillMeta before DeepEqual: JSON key order is not stable, so comparing bytes directly would
// report false positives.
func sameSkillSnapshot(a, b []byte) bool {
	var sa, sb []SkillMeta
	if len(a) > 0 {
		if err := json.Unmarshal(a, &sa); err != nil {
			return false
		}
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &sb); err != nil {
			return false
		}
	}
	if len(sa) == 0 && len(sb) == 0 {
		return true
	}
	return reflect.DeepEqual(sa, sb)
}

// sameDeps compares two tool dependency snapshots (both come from the same query, so the order is stable).
func sameDeps(a, b []byte) bool {
	var da, db2 []toolDep
	if len(a) > 0 {
		if err := json.Unmarshal(a, &da); err != nil {
			return false
		}
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &db2); err != nil {
			return false
		}
	}
	if len(da) == 0 && len(db2) == 0 {
		return true
	}
	return reflect.DeepEqual(da, db2)
}

// DraftChanged reports whether the draft directory disagrees with the frozen file list:
// an extra file, a missing file or a different sha256 on any file all count as a change.
//
// skipDirs are paths relative to the draft directory (such as "skills/old"), on the same rule as the directories excluded
// at release time; content under those prefixes neither counts as an extra file nor takes part in the comparison.
func DraftChanged(ctx context.Context, st storage.Storage, draftDir string, files []assetpack.FileEntry, skipDirs []string) (bool, error) {
	if st == nil {
		return false, nil
	}
	want := make(map[string]string, len(files))
	for _, f := range files {
		want[strings.TrimPrefix(f.Path, "/")] = f.SHA256
	}
	skip := func(rel string) bool {
		for _, d := range skipDirs {
			if rel == d || strings.HasPrefix(rel, d+"/") {
				return true
			}
		}
		return false
	}

	changed := false
	seen := make(map[string]bool, len(want))
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		entries, err := st.List(ctx, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			childRel := e.Name
			if rel != "" {
				childRel = path.Join(rel, e.Name)
			}
			if skip(childRel) {
				continue
			}
			if e.IsDir {
				if err := walk(path.Join(dir, e.Name), childRel); err != nil {
					return err
				}
				continue
			}
			seen[childRel] = true
			sum, ok := want[childRel]
			if !ok {
				changed = true // the version does not have this file
				continue
			}
			data, rerr := st.ReadFile(ctx, path.Join(dir, e.Name))
			if rerr != nil {
				return rerr
			}
			h := sha256.Sum256(data)
			if hex.EncodeToString(h[:]) != sum {
				changed = true
			}
		}
		return nil
	}
	if err := walk(draftDir, ""); err != nil {
		return false, err
	}
	if len(want) != len(seen) {
		changed = true // the version has it, the draft does not
	}
	return changed, nil
}
