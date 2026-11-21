// backfillversions backfills v1 (a release snapshot) for existing entities.
//
// Why it exists: after versioning, production entry points only read is_current and refuse when there is
// no version (ErrNoReleasedVersion). Existing entities have no row at all in the version tables — SQL can
// create the row, but the content of v1 requires **copying directories**, which plain SQL cannot do (see
// the comments in nexus/sql/entity_versions.sql). So the backfill is a one-off program, not a migration
// script.
//
// Ordering requirement: **finish the backfill before opening the runtime gate** (P1), otherwise every
// production call fails.
//
// Usage:
//
//	go run ./cmd/backfillversions                 # all tenants
//	go run ./cmd/backfillversions -dry-run        # report only, no DB writes (version dirs are trial-built then deleted)
//	go run ./cmd/backfillversions -schema t0      # a single tenant
//
// Idempotent: only entities without a version row are processed, so repeated runs do not create v2.
// A single entity failing does not abort: a summary is printed at the end and a non-zero exit code flags
// the manual work left over.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/toolfile"
	"flame/nexus/shared/versioning"
	"flame/nexus/shared/versions"
	"flame/tools/xlog"
)

// backfillNote is the note stored on the backfilled version. It is tenant-visible data: it shows up in the
// release history of the entity and the UI renders it verbatim.
//
// English because a migration has no language input at all - the script runs per tenant schema from an operator's
// shell, with no request and no UI locale attached. With nothing to resolve, the rule is the same as skeleton.Default:
// fall back to the product's primary language. It also says outright that this is a migration, so nobody mistakes it
// for a release someone chose to publish.
const backfillNote = "Migration: current content at the time the version table was created (backfilled as v1)"

type stats struct {
	released int
	skipped  int // already has a version row
	conflict int // the version directory exists but there is no row
	failed   int
}

func (s stats) String() string {
	return fmt.Sprintf("backfilled %d, skipped %d, directory conflicts %d, failed %d",
		s.released, s.skipped, s.conflict, s.failed)
}

func main() {
	dryRun := flag.Bool("dry-run", false,
		"report only, no DB writes; trial version directories are built and then deleted immediately (surfacing missing entry files / overruns early)")
	onlySchema := flag.String("schema", "", "process only this tenant schema (all by default)")
	flag.Parse()

	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		fail("failed to load the config: %v", err)
	}
	config.Cfg = cfg

	// db.InitDatabase logs, so the logger must be ready first (otherwise xlog dereferences nil internally).
	if err := xlog.Init("logs", "backfillversions"); err != nil {
		fail("failed to initialize the logger: %v", err)
	}
	defer xlog.Close()

	if err := db.InitDatabase(); err != nil {
		fail("failed to connect to the database: %v", err)
	}
	defer func() { _ = db.DB.Close() }()

	st, err := storage.New(cfg.Storage)
	if err != nil {
		fail("failed to initialize storage (driver=%s): %v", cfg.Storage.Driver, err)
	}

	schemas, err := versioning.TenantSchemas()
	if err != nil {
		fail("failed to enumerate tenants: %v", err)
	}
	if *onlySchema != "" {
		found := false
		for _, s := range schemas {
			if s == *onlySchema {
				found = true
				break
			}
		}
		if !found {
			fail("tenant %q does not exist (or has no agents table)", *onlySchema)
		}
		schemas = []string{*onlySchema}
	}

	fmt.Printf("backfilling version v1: %d tenant(s)%s\n", len(schemas), mark(*dryRun, " (dry-run, no DB writes)"))
	bad := false
	for _, schema := range schemas {
		ctx := context.Background()
		fmt.Printf("\n== %s ==\n", schema)

		a := backfillAgents(ctx, st, schema, *dryRun)
		fmt.Printf("  agents:      %s\n", a)
		s := backfillSkills(ctx, st, schema, *dryRun)
		fmt.Printf("  skills:      %s\n", s)
		t := backfillTools(ctx, st, schema, *dryRun, cfg.Storage.RootDir)
		fmt.Printf("  customtools: %s\n", t)

		if a.failed+s.failed+t.failed > 0 {
			bad = true
		}
	}
	if bad {
		fmt.Println("\nsome entities failed to backfill (see the log above). Re-run once fixed: this command is idempotent.")
		os.Exit(1)
	}
	fmt.Println("\ndone. The gate that makes production entry points read is_current (P1) can now be opened.")
}

func mark(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

// ===== agents =====

func backfillAgents(ctx context.Context, st storage.Storage, schema string, dry bool) stats {
	var out stats
	rows, err := db.DB.Query(`SELECT a.id::text, a.name FROM ` + schema + `.agents a
		WHERE NOT EXISTS (SELECT 1 FROM ` + schema + `.agent_versions v WHERE v.agent_id = a.id)
		ORDER BY a.name`)
	if err != nil {
		fmt.Printf("  [agents] query failed: %v\n", err)
		out.failed++
		return out
	}
	targets := collect(rows)
	if len(targets) == 0 {
		return out
	}

	for _, t := range targets {
		dir := paths.VersionKey(schema, paths.VersionAgents, t.name, 1)
		if exists, _ := st.Exists(ctx, dir); exists {
			fmt.Printf("  [agents] %s: the version directory exists but there is no row, skipping (needs manual confirmation): %s\n", t.name, dir)
			out.conflict++
			continue
		}

		// Deprecated private skills do not go into the snapshot; private skill metadata goes into the skills column.
		skip, err := versioning.DeprecatedPrivateSkillDirs(schema, t.id)
		if err != nil {
			fmt.Printf("  [agents] %s: failed to read the private skills: %v\n", t.name, err)
			out.failed++
			continue
		}
		privates, err := versioning.PrivateSkillsSnapshot(schema, t.id)
		if err != nil {
			fmt.Printf("  [agents] %s: failed to build the skill snapshot: %v\n", t.name, err)
			out.failed++
			continue
		}

		draftKey, kerr := paths.EntityDraftKey(schema, paths.VersionAgents, t.name)
		if kerr != nil {
			fmt.Printf("  [agents] %s: %v\n", t.name, kerr)
			out.failed++
			continue
		}
		files, err := versions.Snapshot(ctx, st, draftKey, dir, paths.AgentEntryFile, skip)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [agents] %s: freeze failed (%v)%s\n", t.name, err,
				hintEntryMissing(paths.AgentEntryFile))
			out.failed++
			continue
		}
		raw, err := versions.Encode(files)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			out.failed++
			continue
		}
		if dry {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [agents] %s: would backfill v1 (%d files)\n", t.name, len(files))
			out.released++
			continue
		}

		err = insertAgentVersion(schema, t.id, raw, privates)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [agents] %s: failed to write the version row: %v\n", t.name, err)
			out.failed++
			continue
		}
		fmt.Printf("  [agents] %s: v1 (%d files)\n", t.name, len(files))
		out.released++
	}
	return out
}

// insertAgentVersion uses INSERT ... SELECT to copy the mirror columns from the entity row — the column
// list is written once, so a column added to the entity table cannot be silently missed here (a miss is
// caught by check 3 in entity_versions.sql).
func insertAgentVersion(schema, id string, files, skills []byte) error {
	_, err := db.DB.Exec(`INSERT INTO `+schema+`.agent_versions
		(agent_id, version, name, description, config, tools_unrestricted, skill_discovery,
		 default_language, files, skills, is_current, note, created_by)
		SELECT a.id, 1, a.name, a.description, a.config, a.tools_unrestricted, a.skill_discovery,
		       a.default_language, $2, $3, true, $4, a.created_by
		FROM `+schema+`.agents a WHERE a.id = $1`, id, files, skills, backfillNote)
	return err
}

// ===== skills (only tenant-level ones; private skills travel with the agent version) =====

func backfillSkills(ctx context.Context, st storage.Storage, schema string, dry bool) stats {
	var out stats
	rows, err := db.DB.Query(`SELECT s.id::text, s.name FROM ` + schema + `.skills s
		WHERE s.agent_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM ` + schema + `.skill_versions v WHERE v.skill_id = s.id)
		ORDER BY s.name`)
	if err != nil {
		fmt.Printf("  [skills] query failed: %v\n", err)
		out.failed++
		return out
	}
	targets := collect(rows)
	if len(targets) == 0 {
		return out
	}

	for _, t := range targets {
		dir := paths.VersionKey(schema, paths.VersionSkills, t.name, 1)
		if exists, _ := st.Exists(ctx, dir); exists {
			fmt.Printf("  [skills] %s: the version directory exists but there is no row, skipping (needs manual confirmation): %s\n", t.name, dir)
			out.conflict++
			continue
		}

		draftKey, kerr := paths.EntityDraftKey(schema, paths.VersionSkills, t.name)
		if kerr != nil {
			fmt.Printf("  [skills] %s: %v\n", t.name, kerr)
			out.failed++
			continue
		}
		files, err := versions.Snapshot(ctx, st, draftKey, dir, paths.SkillEntryFile, nil)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [skills] %s: freeze failed (%v)%s\n", t.name, err,
				hintEntryMissing(paths.SkillEntryFile))
			out.failed++
			continue
		}
		raw, err := versions.Encode(files)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			out.failed++
			continue
		}
		if dry {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [skills] %s: would backfill v1 (%d files)\n", t.name, len(files))
			out.released++
			continue
		}

		// Tool dependencies are declarations and part of the definition: frozen together, on the same terms
		// as release in admin/skills.
		deps, derr := versioning.SkillToolDepsSnapshot(schema, t.id)
		if derr != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [skills] %s: failed to read the tool dependencies: %v\n", t.name, derr)
			out.failed++
			continue
		}
		_, err = db.DB.Exec(`INSERT INTO `+schema+`.skill_versions
			(skill_id, version, name, description, metadata, user_invocable, command_dispatch,
			 command_tool, restricted, files, tool_deps, is_current, note, created_by)
			SELECT s.id, 1, s.name, s.description, s.metadata, s.user_invocable, s.command_dispatch,
			       s.command_tool, s.restricted, $2, $4, true, $3, s.created_by
			FROM `+schema+`.skills s WHERE s.id = $1`, t.id, raw, backfillNote, deps)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [skills] %s: failed to write the version row: %v\n", t.name, err)
			out.failed++
			continue
		}
		fmt.Printf("  [skills] %s: v1 (%d files)\n", t.name, len(files))
		out.released++
	}
	return out
}

// ===== custom tools =====
//
// A tool is "directory as entity": only tools with a row in the table are backfilled. A hand-made tool
// that has a directory on disk but no row must be registered first by editing / creating it once (the
// tool_id of a version row has a foreign key); this only reports, it never guesses.
func backfillTools(ctx context.Context, st storage.Storage, schema string, dry bool, rootDir string) stats {
	var out stats
	rows, err := db.DB.Query(`SELECT t.id::text, t.name FROM ` + schema + `.custom_tools t
		WHERE NOT EXISTS (SELECT 1 FROM ` + schema + `.tool_versions v WHERE v.tool_id = t.id)
		ORDER BY t.name`)
	if err != nil {
		fmt.Printf("  [customtools] query failed: %v\n", err)
		out.failed++
		return out
	}
	targets := collect(rows)
	if len(targets) == 0 {
		return out
	}

	store := customtoolstore.New(rootDir, schema)
	for _, t := range targets {
		dir := paths.VersionKey(schema, paths.VersionTools, t.name, 1)
		if exists, _ := st.Exists(ctx, dir); exists {
			fmt.Printf("  [customtools] %s: the version directory exists but there is no row, skipping (needs manual confirmation): %s\n", t.name, dir)
			out.conflict++
			continue
		}

		// Merged view (TOOL.json wins): the "definition" that gets released must match what the runtime sees.
		tool, gerr := store.Get(schema, t.name)
		if errors.Is(gerr, sql.ErrNoRows) || tool == nil {
			fmt.Printf("  [customtools] %s: there is a row but the directory / contract is missing, skipping (needs manual completion)\n", t.name)
			out.failed++
			continue
		}
		if gerr != nil {
			fmt.Printf("  [customtools] %s: failed to read the tool: %v\n", t.name, gerr)
			out.failed++
			continue
		}

		draftKey, kerr := paths.EntityDraftKey(schema, paths.VersionTools, t.name)
		if kerr != nil {
			fmt.Printf("  [customtools] %s: %v\n", t.name, kerr)
			out.failed++
			continue
		}
		files, err := versions.Snapshot(ctx, st, draftKey, dir, tool.Entry, nil)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [customtools] %s: freeze failed (%v)%s\n", t.name, err,
				hintEntryMissing(tool.Entry))
			out.failed++
			continue
		}

		// The snapshot must carry a valid TOOL.json (when the draft lacks or breaks it, write it from the
		// merged view).
		norm := models.ToolFileOf(tool)
		data, merr := models.MarshalToolFile(norm)
		if merr != nil {
			_ = st.DeletePrefix(ctx, dir)
			out.failed++
			continue
		}
		hostDst := filepath.Join(rootDir, filepath.FromSlash(dir))
		if werr := toolfile.WriteAt(hostDst, norm); werr != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [customtools] %s: failed to write the version contract: %v\n", t.name, werr)
			out.failed++
			continue
		}
		files = versions.PutEntry(files, models.ToolFileName, data)

		raw, err := versions.Encode(files)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			out.failed++
			continue
		}
		if dry {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [customtools] %s: would backfill v1 (%d files)\n", t.name, len(files))
			out.released++
			continue
		}

		// The configs value does not go into a version: configuration is runtime fact, not definition.
		_, err = db.DB.Exec(`INSERT INTO `+schema+`.tool_versions
			(tool_id, version, name, display_name, description, parameters, output, lang, entry,
			 timeout_ms, max_output_bytes, egress, config_fields, configs, files,
			 is_current, note, created_by)
			SELECT t.id, 1, t.name, t.display_name, t.description, t.parameters, t.output, t.lang,
			       t.entry, t.timeout_ms, t.max_output_bytes, t.egress, t.config_fields,
			       '{}'::jsonb, $2, true, $3, t.created_by
			FROM `+schema+`.custom_tools t WHERE t.id = $1`, t.id, raw, backfillNote)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [customtools] %s: failed to write the version row: %v\n", t.name, err)
			out.failed++
			continue
		}
		fmt.Printf("  [customtools] %s: v1 (%d files)\n", t.name, len(files))
		out.released++
	}
	return out
}

// ===== helpers =====

type target struct{ id, name string }

func collect(rows *sql.Rows) []target {
	defer rows.Close()
	var out []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.name); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// hintEntryMissing adds a plain-language hint for the most common failure (missing entry file / directory
// absent).
func hintEntryMissing(entry string) string {
	return " (most likely the definition directory does not exist or the entry file " + strings.TrimSpace(entry) + " is missing)"
}

func fail(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}
