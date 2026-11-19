package globalvars

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// Reference-surface check: before deleting a global variable, find out what
// still references it.
//
// Why a dedicated scan is worth it: once the variable is gone, the nodes and
// documents that reference it only fail at run time, possibly days later in a
// scheduled task. The moment of deletion is the only chance to put "what you
// are about to break" in front of a person.
//
// The two halves of the scan:
//   1) structured references in the database - flows.diagram / flows.env_vars /
//      agents.config / skills.metadata;
//   2) document references on disk - ${global_vars.X} inside AGENT.md / SKILL.md.
// Scanning only the database yields the false conclusion "nobody uses it, safe to
// delete", because document references are not in the database at all.
//
// Documents and agent configs are scanned for the substring "global_vars.<name>":
// both ${...} and {{.global_vars.X}} contain it, so no separate regex is needed.
// Flows cannot be scanned that way - their current form (a node input's global
// source) does not contain that substring, so the diagram has to be parsed and its
// inputs inspected, otherwise the same false conclusion comes back.
// ============================================================================

// UsageHit is one reference site.
type UsageHit struct {
	Kind string `json:"kind"` // flow / agent / skill / agent_doc / skill_doc
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// maxDocScanBytes bounds how much of a single document is read. Documents are prose for
// humans, not data files; anything larger is not a .md worth loading into memory for a
// deletion check.
const maxDocScanBytes = 1 << 20

var (
	usageStoreOnce sync.Once
	usageStore     storage.Storage
	usageStoreErr  error
)

func usageStorage() (storage.Storage, error) {
	usageStoreOnce.Do(func() {
		usageStore, usageStoreErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return usageStore, usageStoreErr
}

// ScanUsage finds the places that reference a global variable.
//
// Any error fails the whole call (no partial result): the caller relies on it to decide
// "can this be deleted", and an incomplete scan's "no references" is more dangerous than
// no scan at all.
func ScanUsage(schema, name string) ([]UsageHit, error) {
	if schema == "" || name == "" {
		return nil, fmt.Errorf("missing tenant or variable name")
	}
	needle := "global_vars." + name

	hits := make([]UsageHit, 0, 4)

	// 1) Flows: the global source of a node input variable (structured) and the legacy text form.
	//    Since global variables became an input source, the database no longer contains the
	//    substring "global_vars.<name>"; a substring-only scan would conclude "nobody uses it,
	//    safe to delete" - the diagram must be parsed and its inputs inspected.
	flowHits, err := scanFlows(schema, name, needle)
	if err != nil {
		return nil, fmt.Errorf("failed to scan flows: %w", err)
	}
	hits = append(hits, flowHits...)

	// 2) Agents: config may hold agent-level tool or model settings
	agentHits, err := scanRows(schema, "agent", `SELECT id::text, name FROM `+schema+`.agents
		WHERE strpos(COALESCE(config::text, ''), $1) > 0`, needle)
	if err != nil {
		return nil, fmt.Errorf("failed to scan agents: %w", err)
	}
	hits = append(hits, agentHits...)

	// 3) Skills: description and metadata
	skillHits, err := scanRows(schema, "skill", `SELECT id::text, name FROM `+schema+`.skills
		WHERE strpos(COALESCE(metadata::text, ''), $1) > 0
		   OR strpos(COALESCE(description, ''), $1) > 0`, needle)
	if err != nil {
		return nil, fmt.Errorf("failed to scan skills: %w", err)
	}
	hits = append(hits, skillHits...)

	// 4) Documents: AGENT.md / SKILL.md (including agent-private skills)
	docHits, err := scanDocs(schema, needle)
	if err != nil {
		return nil, err
	}
	hits = append(hits, docHits...)

	return hits, nil
}

// scanFlows scans flows: the global source of input variables (current form) plus the legacy
// text form.
//
// It pre-filters in SQL (rows containing "global" or the legacy substring) and then parses the
// diagrams in Go to decide: loading and parsing every diagram costs time linear in the number
// of flows, and the filter removes the vast majority of rows.
func scanFlows(schema, name, needle string) ([]UsageHit, error) {
	rows, err := db.DB.Query(`SELECT id::text, name, COALESCE(diagram::text, '') FROM `+schema+`.flows
		WHERE strpos(COALESCE(diagram::text, ''), '"global"') > 0
		   OR strpos(COALESCE(diagram::text, ''), $1) > 0`, needle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UsageHit
	for rows.Next() {
		var id, flowName, diagram string
		if err := rows.Scan(&id, &flowName, &diagram); err != nil {
			return nil, err
		}
		if flowRefsGlobalVar(diagram, name) {
			out = append(out, UsageHit{Kind: "flow", ID: id, Name: flowName})
		}
	}
	return out, rows.Err()
}

// flowRefsGlobalVar reports whether a diagram references the global variable called name.
//
// Two checks: the global source of an input variable (current form), and the legacy
// {{.global_vars.NAME}} text still left in the diagram - that form no longer runs, but
// deleting the variable would still make it worse.
func flowRefsGlobalVar(diagram, name string) bool {
	if diagram == "" {
		return false
	}
	if strings.Contains(diagram, "global_vars."+name) {
		return true
	}
	if !strings.Contains(diagram, `"global"`) {
		return false
	}
	var root any
	if err := json.Unmarshal([]byte(diagram), &root); err != nil {
		return false
	}
	for _, ref := range assetpack.GlobalVarRefsInNodeInputs(root) {
		if ref == name {
			return true
		}
	}
	return false
}

func scanRows(schema, kind, query, needle string) ([]UsageHit, error) {
	rows, err := db.DB.Query(query, needle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UsageHit
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out = append(out, UsageHit{Kind: kind, ID: id, Name: name})
	}
	return out, rows.Err()
}

// scanDocs walks the tenant definition domain through the storage abstraction looking for .md files.
//
// Only the agents/ and skills/ trees are scanned: versions/ holds historical snapshots, and
// referencing a snapshot does not mean "still in use"; counting them would attach a pile of
// long-released old copies to every deletion prompt forever.
func scanDocs(schema, needle string) ([]UsageHit, error) {
	st, err := usageStorage()
	if err != nil {
		return nil, fmt.Errorf("failed to open storage, document references were not checked: %w", err)
	}
	walker, ok := st.(storage.Walker)
	if !ok {
		return nil, fmt.Errorf("the current storage backend does not support walking, document references were not checked")
	}

	ctx := context.Background()
	dirs := []string{
		path.Join(paths.TenantsSubdir, schema, paths.AgentsSubdir),
		paths.SkillsKey(schema),
	}

	var out []UsageHit
	for _, dir := range dirs {
		werr := walker.WalkPrefix(ctx, dir, func(rel string, size int64, _ time.Time) error {
			if size <= 0 || size > maxDocScanBytes || !strings.HasSuffix(strings.ToLower(rel), ".md") {
				return nil
			}
			data, rerr := st.ReadFile(ctx, rel)
			if rerr != nil {
				return nil // deleted between the walk and the read: skip it rather than failing a deletion check
			}
			if strings.Contains(string(data), needle) {
				out = append(out, UsageHit{Kind: docKind(rel), Name: rel})
			}
			return nil
		})
		if werr != nil {
			return nil, fmt.Errorf("failed to scan documents: %w", werr)
		}
	}
	return out, nil
}

func docKind(rel string) string {
	if strings.HasSuffix(strings.ToLower(rel), strings.ToLower(paths.AgentEntryFile)) {
		return "agent_doc"
	}
	return "skill_doc"
}

// GlobalVarUsage reports the reference surface of a global variable (used by the confirmation
// dialog before deletion).
func GlobalVarUsage(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		xhttp.ErrCode(c, "globalvar.name_required", nil, "the variable name cannot be empty", "")
		return
	}

	hits, err := ScanUsage(info.Schema, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": hits, "total": len(hits)}, "")
}

// describeHits squeezes the reference surface into one sentence: the variable name plus the
// first few positions, so the error message stays short.
func describeHits(hits []UsageHit) string {
	const limit = 5
	parts := make([]string, 0, limit)
	for i, h := range hits {
		if i >= limit {
			parts = append(parts, fmt.Sprintf("... and %d more", len(hits)))
			break
		}
		parts = append(parts, h.KindLabel()+h.Name)
	}
	return strings.Join(parts, ", ")
}

// KindLabel is the reference-kind wording used directly in error messages.
func (h UsageHit) KindLabel() string {
	switch h.Kind {
	case "flow":
		return "flow "
	case "agent":
		return "agent "
	case "skill":
		return "skill "
	case "agent_doc":
		return "doc "
	case "skill_doc":
		return "doc "
	default:
		return ""
	}
}
