package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// ErrNoReleasedVersion means the entity has no released version (no is_current row in the version table).
//
// A production entry point (MCP / RPC / an endpoint / a direct call between flows) must refuse when it sees this,
// never quietly falling back to the draft — otherwise "a production run uses the released version" means nothing.
// A debug entry point (the editor's debugger / a run_flow trial) reads the draft and never comes through here.
var ErrNoReleasedVersion = errors.New("no released version yet")

// ErrCannotDeleteCurrentVersion means the current version cannot be deleted.
//
// A production entry point (MCP / RPC / an endpoint / a direct call between flows) runs the is_current row, so
// deleting it pulls the production definition out from under them and every production call becomes
// "no released version yet". To delete it, switch production over to another version first — that is one action,
// deleting is another.
var ErrCannotDeleteCurrentVersion = errors.New("the current version cannot be deleted; switch production to another version first")

// FlowVersion is one workflow version record (flow_versions).
//
// The list endpoint does not query diagram (it may be tens of KB); when the whole definition is needed, use
// GetCurrentFlowVersion.
type FlowVersion struct {
	FlowID        string      `json:"flow_id"`
	Version       int         `json:"version"`
	Name          string      `json:"name"`
	Description   xsql.String `json:"description"`
	IsCurrent     bool        `json:"is_current"`
	Note          string      `json:"note"`
	CreatedBy     xsql.UUID   `json:"created_by"` // a uuid column: scanning with String reports "cannot convert []uint8 to string"
	CreatedByName xsql.String `json:"created_by_name"`
	CreatedAt     xsql.Time   `json:"created_at"`
}

// ListFlowVersions lists every version of one workflow (newest first).
func (r *PostgresRepository) ListFlowVersions(authInfo *auth.AuthInfo, flowID string) ([]FlowVersion, error) {
	rows, err := db.DB.Query(`SELECT v.flow_id, v.version, v.name, v.description, v.is_current,
			v.note, v.created_by, COALESCE(u.name, ''), v.created_at
		FROM `+authInfo.Schema+`.flow_versions v
		LEFT JOIN `+authInfo.Schema+`.users u ON u.id = v.created_by
		WHERE v.flow_id = $1
		ORDER BY v.version DESC`, flowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FlowVersion
	for rows.Next() {
		var v FlowVersion
		if err := rows.Scan(&v.FlowID, &v.Version, &v.Name, &v.Description, &v.IsCurrent,
			&v.Note, &v.CreatedBy, &v.CreatedByName, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReleaseFlowVersion freezes the current working copy (the flows row) into a new version and makes it current.
//
// The version number is taken as max+1 under a row lock: two concurrent releases queue up instead of computing the
// same number and hitting the primary key. Historical versions only have is_current set to false; their content is
// untouched.
func (r *PostgresRepository) ReleaseFlowVersion(authInfo *auth.AuthInfo, flowID, note string) (*FlowVersion, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	version, createdAt, err := releaseFlowVersionTx(tx, authInfo.Schema, flowID, note, authInfo.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.flowVersionMeta(authInfo, flowID, version, createdAt, note), nil
}

// RollbackFlowVersion switches "the version in use" back to a historical one — a pure pointer change that creates
// no new version.
//
// It and ApplyFlowVersionToDraft are two actions for two intentions:
//   - rollback: production goes back to the old artefact, the canvas (draft) is untouched;
//   - apply: the canvas is replaced with the old content to keep editing, production is untouched.
//
// Merging them into one action costs one of two things: a rollback overwrites a draft someone else is writing, or
// every rollback publishes another version, filling the history with identical copies (and making "why do v1 and
// v3 hold the same content" unanswerable).
func (r *PostgresRepository) RollbackFlowVersion(authInfo *auth.AuthInfo, flowID string, target int) (*FlowVersion, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Lock the entity row: serialise against release, so the pointer cannot be moved mid-release
	var status int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&status); err != nil {
		return nil, err
	}
	if !models.IsModifiable(status) {
		return nil, fmt.Errorf("this workflow is deprecated and cannot switch versions")
	}

	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND version = $2)`, flowID, target).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("version v%d: %w", target, sql.ErrNoRows)
	}

	if _, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flow_versions SET is_current = false
		WHERE flow_id = $1 AND is_current`, flowID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flow_versions SET is_current = true
		WHERE flow_id = $1 AND version = $2`, flowID, target); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.getFlowVersion(authInfo, flowID, target)
}

// ApplyFlowVersionToDraft writes a version's content back into the working copy (the canvas) **without releasing
// it**.
//
// The purpose is "keep editing from an old version": once written it is in the "changed" state and only affects
// production when released. The current production version is not touched by a single byte — which is exactly why
// apply and rollback are separate.
func (r *PostgresRepository) ApplyFlowVersionToDraft(authInfo *auth.AuthInfo, flowID string, target int) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&status); err != nil {
		return err
	}
	if !models.IsModifiable(status) {
		return fmt.Errorf("this workflow is deprecated and cannot load a version")
	}

	res, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flows f
		SET description = v.description, diagram = v.diagram, arguments = v.arguments,
		    outputs = v.outputs, env_vars = v.env_vars, flow_vars = v.flow_vars,
		    timeout = v.timeout, max_exec_count = v.max_exec_count,
		    store_runs = v.store_runs, session_memory = v.session_memory,
		    updated_at = now(), updated_by = NULLIF($3, '')::uuid
		FROM `+authInfo.Schema+`.flow_versions v
		WHERE f.id = $1 AND v.flow_id = $1 AND v.version = $2`,
		flowID, target, authInfo.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("version v%d: %w", target, sql.ErrNoRows)
	}
	return tx.Commit()
}

// UnreleaseFlowVersion takes the current version off production: it clears is_current and nothing else.
//
// The version row stays (the history keeps its number and its content), the draft is untouched, and no endpoint or
// grant is touched. Production entry points then see ErrNoReleasedVersion — the explicit "nothing is released"
// state, the same shape as an entity that never released. Deleting the entity requires this step first: a delete
// must not be the thing that takes a service down.
func (r *PostgresRepository) UnreleaseFlowVersion(authInfo *auth.AuthInfo, flowID string) (int, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Lock the entity row first: release / rollback / delete all move or check is_current, and the read below must
	// see the final owner (ReleaseFlowVersion and DeleteFlowVersion serialise the same way).
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM `+authInfo.Schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&one); err != nil {
		return 0, err
	}

	var version int
	err = tx.QueryRow(`SELECT version FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND is_current`, flowID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoReleasedVersion
	}
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flow_versions SET is_current = false
		WHERE flow_id = $1 AND is_current`, flowID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return version, nil
}

// DeleteFlowVersion deletes one historical version: it only clears the history row, touching neither production
// nor the canvas.
//
// The current version is always refused (ErrCannotDeleteCurrentVersion): it is where production calls take their
// definition from, and the only record of "what production is running" (flow_runs stores no version number). Taking
// a service down is a different action and should not hide inside "delete history".
//
// A deprecated flow is refused too — the same rule as release / rollback / apply.
//
// Version numbers are not reused: after deleting v2 the next release is still MAX(version)+1, never another v2.
// Reusing a number would make "roll back to v2" point at something that drifts with every deletion, and the history
// notes would stop adding up.
func (r *PostgresRepository) DeleteFlowVersion(authInfo *auth.AuthInfo, flowID string, target int) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Lock the entity row first: release and rollback both move is_current, and only after serialising does the read
	// below reflect the final ownership
	var status int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&status); err != nil {
		return err
	}
	if !models.IsModifiable(status) {
		return fmt.Errorf("this workflow is deprecated and cannot delete a version")
	}

	var isCurrent bool
	err = tx.QueryRow(`SELECT is_current FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND version = $2`, flowID, target).Scan(&isCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("version v%d: %w", target, sql.ErrNoRows)
	}
	if err != nil {
		return err
	}
	if isCurrent {
		// When it is the only one left, "switch production to another version" is not the advice to give — there is
		// no other version and the user would be stuck
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM `+authInfo.Schema+`.flow_versions
			WHERE flow_id = $1`, flowID).Scan(&total); err != nil {
			return err
		}
		if total <= 1 {
			return fmt.Errorf("v%d is the only version and cannot be deleted; release a new version first to prune the history", target)
		}
		return ErrCannotDeleteCurrentVersion
	}

	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND version = $2`, flowID, target); err != nil {
		return err
	}
	return tx.Commit()
}

// getFlowVersion reads one version record (returned to the frontend after a rollback to refresh the list).
func (r *PostgresRepository) getFlowVersion(authInfo *auth.AuthInfo, flowID string, version int) (*FlowVersion, error) {
	var v FlowVersion
	err := db.DB.QueryRow(`SELECT v.flow_id, v.version, v.name, v.description, v.is_current,
			v.note, v.created_by, COALESCE(u.name, ''), v.created_at
		FROM `+authInfo.Schema+`.flow_versions v
		LEFT JOIN `+authInfo.Schema+`.users u ON u.id = v.created_by
		WHERE v.flow_id = $1 AND v.version = $2`, flowID, version).
		Scan(&v.FlowID, &v.Version, &v.Name, &v.Description, &v.IsCurrent,
			&v.Note, &v.CreatedBy, &v.CreatedByName, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetCurrentFlowVersion returns the definition of the "released version", for a production entry point to execute.
//
// The returned models.Flow is a hybrid: the definition columns (diagram / variables / limits / name / description)
// come from the is_current row, while identity and lifecycle (id / status / endpoints / timestamps) come from the
// entity row — deciding runnability at runtime must look at the entity status, which a snapshot does not carry.
// With no released version it returns ErrNoReleasedVersion, and the caller should refuse to execute rather than fall
// back to the draft.
func (r *PostgresRepository) GetCurrentFlowVersion(authInfo *auth.AuthInfo, id string) (*models.Flow, error) {
	base, err := r.GetFlow(authInfo, id)
	if err != nil {
		return nil, err
	}

	var name, note string
	var desc xsql.String
	var diagram xsql.JSONB
	err = db.DB.QueryRow(`SELECT name, description, diagram, note
		FROM `+authInfo.Schema+`.flow_versions
		WHERE flow_id = $1 AND is_current`, id).Scan(&name, &desc, &diagram, &note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoReleasedVersion
	}
	if err != nil {
		return nil, err
	}

	base.Name = name
	base.Description = desc
	base.Diagram = diagram
	return base, nil
}

// flowVersionMeta fills in the display fields of the response after a successful release (name / description /
// releaser's name).
func (r *PostgresRepository) flowVersionMeta(authInfo *auth.AuthInfo, flowID string, version int, createdAt xsql.Time, note string) *FlowVersion {
	v := &FlowVersion{
		FlowID:        flowID,
		Version:       version,
		IsCurrent:     true,
		Note:          note,
		CreatedBy:     xsql.UUID(authInfo.ID),
		CreatedByName: xsql.String(authInfo.Name),
		CreatedAt:     createdAt,
	}
	if err := db.DB.QueryRow(`SELECT name, description FROM `+authInfo.Schema+`.flows WHERE id = $1`, flowID).
		Scan(&v.Name, &v.Description); err != nil {
		return v // failing to fill in the display fields does not change the release itself
	}
	return v
}

// fillVersionState adds "current version number + are there unreleased changes" (two non-database columns) to a
// query result.
//
// The editor must be able to tell three states apart, otherwise a user who edited the canvas believes production
// already picked it up:
//   - CurrentVersion = 0    not released yet (a production call refuses outright)
//   - Dirty = false         the working copy matches the current version
//   - Dirty = true          saved changes are not released yet, production still runs the old version
//
// What is compared is the "definition columns": description / diagram / variables / each limit.
// The name is not compared — renaming changes the external tool name (local__<name>), but that is the entity's
// identity; releasing a version with the same name and content does not "release" a rename, and marking it dirty
// would only mislead.
func (r *PostgresRepository) fillVersionState(authInfo *auth.AuthInfo, f *models.Flow) {
	if f == nil || f.ID == "" {
		return
	}
	var version int
	var dirty bool
	// The jsonb columns must be normalised: the editor writes empty variables as '{}' while a released snapshot may
	// hold NULL, and a bare IS DISTINCT FROM would judge "nothing changed" as dirty, making the badge lie
	// immediately.
	err := db.DB.QueryRow(`SELECT v.version,
			(COALESCE(f.description, '')      IS DISTINCT FROM COALESCE(v.description, '')
			 OR COALESCE(f.diagram,   '{}'::jsonb) IS DISTINCT FROM COALESCE(v.diagram,   '{}'::jsonb)
			 OR COALESCE(f.arguments, '{}'::jsonb) IS DISTINCT FROM COALESCE(v.arguments, '{}'::jsonb)
			 OR COALESCE(f.outputs,   '{}'::jsonb) IS DISTINCT FROM COALESCE(v.outputs,   '{}'::jsonb)
			 OR COALESCE(f.env_vars,  '{}'::jsonb) IS DISTINCT FROM COALESCE(v.env_vars,  '{}'::jsonb)
			 OR COALESCE(f.flow_vars, '{}'::jsonb) IS DISTINCT FROM COALESCE(v.flow_vars, '{}'::jsonb)
			 OR f.timeout          IS DISTINCT FROM v.timeout
			 OR f.max_exec_count   IS DISTINCT FROM v.max_exec_count
			 OR f.store_runs       IS DISTINCT FROM v.store_runs
			 OR f.session_memory   IS DISTINCT FROM v.session_memory)
		FROM `+authInfo.Schema+`.flows f
		JOIN `+authInfo.Schema+`.flow_versions v
		  ON v.flow_id = f.id AND v.is_current
		WHERE f.id = $1`, f.ID).Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		f.CurrentVersion = 0
		f.Dirty = false
		return
	}
	if err != nil {
		// Failing to read the version info must not fail the whole detail page: fall back to "unknown", which the
		// frontend shows as not released
		return
	}
	f.CurrentVersion = version
	f.Dirty = dirty
}

// releaseFlowVersionTx freezes the current working copy inside a transaction:
// row lock -> take the next version number -> clear the old current flag -> insert the new version.
func releaseFlowVersionTx(tx *sql.Tx, schema, flowID, note, userID string) (int, xsql.Time, error) {
	// Row lock: concurrent releases queue here, guaranteeing max(version)+1 does not hit the primary key
	var status int
	if err := tx.QueryRow(`SELECT status FROM `+schema+`.flows WHERE id = $1 FOR UPDATE`, flowID).
		Scan(&status); err != nil {
		return 0, xsql.Time{}, err
	}
	if !models.IsModifiable(status) {
		return 0, xsql.Time{}, fmt.Errorf("this workflow is deprecated and cannot release a version")
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM `+schema+`.flow_versions
		WHERE flow_id = $1`, flowID).Scan(&next); err != nil {
		return 0, xsql.Time{}, err
	}

	if _, err := tx.Exec(`UPDATE `+schema+`.flow_versions SET is_current = false
		WHERE flow_id = $1 AND is_current`, flowID); err != nil {
		return 0, xsql.Time{}, err
	}

	var createdAt xsql.Time
	err := tx.QueryRow(`INSERT INTO `+schema+`.flow_versions
		(flow_id, version, name, description, diagram, arguments, outputs, env_vars, flow_vars,
		 timeout, max_exec_count, store_runs, session_memory, is_current, note, created_by)
		SELECT f.id, $2, f.name, f.description, f.diagram, f.arguments, f.outputs, f.env_vars,
		       f.flow_vars, f.timeout, f.max_exec_count, f.store_runs, f.session_memory,
		       true, $3, NULLIF($4, '')::uuid
		  FROM `+schema+`.flows f
		 WHERE f.id = $1
		RETURNING created_at`, flowID, next, note, userID).Scan(&createdAt)
	if err != nil {
		return 0, xsql.Time{}, err
	}
	return next, createdAt, nil
}
