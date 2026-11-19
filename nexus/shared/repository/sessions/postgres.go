package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"sync"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/storage"
	"flame/tools/xlog"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL-backed session store.
type PostgresRepository struct{}

// Create creates a session (userID may be empty) and returns the new session ID.
//
// It also creates the session workspace (users/{uid}/sessions/{sid}/): that is the file tools' default root and the cwd of
// exec, so it must exist before first use (S3 has no empty directories, hence the .keep placeholder).
func (r *PostgresRepository) Create(schema, ownerType, ownerID, userID, title string) (string, error) {
	// Session-count quota: the console refuses over the limit (507) and the human deletes old sessions by hand - chat
	// history does not make that decision for the user. The automatic reclaim for protocol endpoints lives in
	// CreateWithID (their callers have no delete API).
	if err := quota.CheckUserSessions(schema, ownerType, userID); err != nil {
		return "", err
	}
	id := uuid.NewString()
	_, err := db.DB.Exec(`
		INSERT INTO `+schema+`.sessions (id, owner_type, owner_id, user_id, title)
		VALUES ($1,$2,$3,$4,$5)`,
		id, ownerType, ownerID, nullIfEmpty(userID), title)
	if err != nil {
		return "", err
	}
	ensureSessionDir(schema, userID, id)
	return id, nil
}

// ListByOwner lists one owner's sessions (updated_at descending). A non-empty userID filters by user; limit<=0 does not
// limit; a non-empty keyword matches the title fuzzily.
func (r *PostgresRepository) ListByOwner(schema, ownerType, ownerID, userID string, limit int, keyword string) ([]models.Session, error) {
	where := "WHERE owner_type = $1 AND owner_id = $2"
	args := []interface{}{ownerType, ownerID}
	argIdx := 3
	if userID != "" {
		where += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, userID)
		argIdx++
	}
	if keyword != "" {
		where += fmt.Sprintf(" AND title ILIKE $%d", argIdx)
		args = append(args, "%"+keyword+"%")
		argIdx++
	}

	listSQL := `SELECT id, owner_type, owner_id, user_id, title, created_at, updated_at
		FROM ` + schema + `.sessions ` + where + ` ORDER BY updated_at DESC, created_at DESC`
	if limit > 0 {
		listSQL += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, limit)
	}

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.Session, 0)
	for rows.Next() {
		var s models.Session
		if err := rows.Scan(&s.ID, &s.OwnerType, &s.OwnerID, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// ListByUser lists all of one user's sessions under the given owner_type (updated_at descending). limit<=0 does not limit.
func (r *PostgresRepository) ListByUser(schema, ownerType, userID string, limit int) ([]models.Session, error) {
	where := "WHERE owner_type = $1 AND user_id = $2"
	args := []interface{}{ownerType, userID}
	argIdx := 3

	listSQL := `SELECT id, owner_type, owner_id, user_id, title, created_at, updated_at
		FROM ` + schema + `.sessions ` + where + ` ORDER BY updated_at DESC, created_at DESC`
	if limit > 0 {
		listSQL += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, limit)
	}

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.Session, 0)
	for rows.Next() {
		var s models.Session
		if err := rows.Scan(&s.ID, &s.OwnerType, &s.OwnerID, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// Get reads a session by ID; (nil, nil) when it does not exist.
func (r *PostgresRepository) Get(schema, sessionID string) (*models.Session, error) {
	var s models.Session
	err := db.DB.QueryRow(`
		SELECT id, owner_type, owner_id, user_id, title, created_at, updated_at
		FROM `+schema+`.sessions
		WHERE id = $1`, sessionID).Scan(&s.ID, &s.OwnerType, &s.OwnerID, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// Lookup reads a session by ID and checks ownership. See the interface comment: the three failure kinds stay distinct
// internally and merge into one outward message.
//
// Validate the UUID before querying: sessions.id is a uuid column, so a dirty string makes PG raise 22P02 - meaningless
// to the caller, and it leaks SQL detail into the HTTP response.
func (r *PostgresRepository) Lookup(schema, sessionID, ownerType, ownerID string) (*models.Session, error) {
	sid, herr := ParseID(sessionID)
	if herr != nil {
		return nil, herr
	}
	sess, err := r.Get(schema, sid)
	if err != nil {
		return nil, err
	}
	if !sess.OwnedBy(ownerType, ownerID) {
		return nil, ErrSessionForbidden
	}
	return sess, nil
}

// UpdateTitle updates the title and refreshes updated_at.
func (r *PostgresRepository) UpdateTitle(schema, sessionID, title string) error {
	_, err := db.DB.Exec(`UPDATE `+schema+`.sessions SET title = $1, updated_at = now() WHERE id = $2`, title, sessionID)
	return err
}

// Touch refreshes updated_at.
func (r *PostgresRepository) Touch(schema, sessionID string) error {
	_, err := db.DB.Exec(`UPDATE `+schema+`.sessions SET updated_at = now() WHERE id = $1`, sessionID)
	return err
}

// EnsureExists makes sure the session with the given id exists: missing means create it with that id, present means refresh
// updated_at.
func (r *PostgresRepository) EnsureExists(schema, id, ownerType, ownerID, userID, title string) error {
	_, err := db.DB.Exec(`
		INSERT INTO `+schema+`.sessions (id, owner_type, owner_id, user_id, title)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET updated_at = now()`,
		id, ownerType, ownerID, nullIfEmpty(userID), title)
	if err != nil {
		return err
	}
	ensureSessionDir(schema, userID, id)
	return nil
}

// CreateWithID idempotently creates a session with the id the caller supplied (used when a protocol endpoint passes a
// session_id that does not exist yet).
//
// It differs from EnsureExists in two ways: on conflict it reads back and then checks visibility, so a call is never
// attached to somebody else's session; and it goes through the session-count quota (EnsureExists does neither).
func (r *PostgresRepository) CreateWithID(schema, id, ownerType, ownerID, userID, title string) (*models.Session, error) {
	// Protocol endpoints only: over the limit, reclaim this user's oldest session to make room (see ensureSessionSlot).
	if err := r.ensureSessionSlot(schema, ownerType, userID); err != nil {
		return nil, err
	}
	if _, err := db.DB.Exec(`
		INSERT INTO `+schema+`.sessions (id, owner_type, owner_id, user_id, title)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`,
		id, ownerType, ownerID, nullIfEmpty(userID), title); err != nil {
		return nil, err
	}
	// Read back: a concurrent request may have just created the same id (uuids are random, so it is rare, but it must be
	// covered)
	sess, err := r.Get(schema, id)
	if err != nil {
		return nil, err
	}
	if sess == nil || !sess.VisibleToUser(userID) {
		return nil, ErrSessionForbidden
	}
	ensureSessionDir(schema, userID, id)
	return sess, nil
}

// ensureSessionSlot is the session-count quota for protocol endpoints: over the limit it reclaims this user's oldest
// session under that owner type.
//
// Why reclaim only here: RPC / MCP sessions are managed by the caller, which brings its own UUID every time and has no
// delete API - refusing flatly at the limit would lock an integration out forever.
// The console (Create) does not come through here: it returns 507 and lets the human decide what to delete.
// The quota is bucketed by (owner_type, user_id), and endpoint credentials are usually bound to a separate account, so
// reclaiming never squeezes out console sessions.
//
// Cases that return the quota error without reclaiming:
//   - limit <= 0: 0 = creating is forbidden; -1 = unlimited (CheckUserSessions already let it through, so we never get here)
//   - still over the limit after reclaiming (a concurrent create grabbed the slot)
//
// Known trade-off: it picks purely by oldest updated_at and does not check whether that session has a running run. A live
// session refreshes updated_at at the end of every turn, and the target is "the one unused the longest", so hitting a live
// session is very unlikely; ruling it out completely would mean moving the run registry from service/agent into shared
// (an import cycle today).
func (r *PostgresRepository) ensureSessionSlot(schema, ownerType, userID string) error {
	err := quota.CheckUserSessions(schema, ownerType, userID)
	if err == nil {
		return nil
	}
	var qe *quota.ExceededError
	if !errors.As(err, &qe) || qe.Metric != "sessions" || qe.Limit <= 0 {
		return err
	}
	need := evictCount(int(qe.Used), int(qe.Limit))
	n, eerr := r.evictOldestSessions(schema, ownerType, userID, need)
	if eerr != nil {
		xlog.Error("[QUOTA] failed to reclaim sessions schema=%s owner=%s user=%s: %v", schema, ownerType, userID, eerr)
		return err
	}
	xlog.Info("[QUOTA_ALERT] session count at the limit, reclaimed oldest sessions schema=%s owner=%s user=%s used=%d limit=%d evicted=%d",
		schema, ownerType, userID, qe.Used, qe.Limit, n)
	// Re-check: a concurrent create may still leave it over the limit, in which case the quota error is returned as is
	// (HTTP 507 / RPC -32000)
	return quota.CheckUserSessions(schema, ownerType, userID)
}

// evictCount returns how many sessions must be freed to bring used below limit; limit<=0 means creating is not allowed.
func evictCount(used, limit int) int {
	if limit <= 0 || used < limit {
		return 0
	}
	return used - limit + 1
}

// evictOldestSessions deletes the n sessions with the oldest updated_at (records, row, directory and usage counter
// included) and returns how many were actually deleted.
//
// It goes through Delete one by one: each session needs four things cleared (records + row + directory + usage), so a
// batch version would just be a copy of Delete.
// A single failure is logged and skipped to free as much as possible; whether it was enough is decided by the caller
// re-checking the quota.
func (r *PostgresRepository) evictOldestSessions(schema, ownerType, userID string, n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	rows, err := db.DB.Query(`
		SELECT id FROM `+schema+`.sessions
		WHERE owner_type = $1 AND user_id = $2
		ORDER BY updated_at ASC
		LIMIT $3`, ownerType, userID, n)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, n)
	for rows.Next() {
		var id string
		if serr := rows.Scan(&id); serr != nil {
			rows.Close()
			return 0, serr
		}
		ids = append(ids, id)
	}
	if rerr := rows.Err(); rerr != nil {
		rows.Close()
		return 0, rerr
	}
	rows.Close()

	deleted := 0
	for _, id := range ids {
		if derr := r.Delete(schema, id); derr != nil {
			xlog.Error("[QUOTA] failed to reclaim session schema=%s session=%s: %v", schema, id, derr)
			continue
		}
		deleted++
	}
	return deleted, nil
}

// DeleteByOwner deletes every session of one owner (agent / flow), records, rows, workspace directories and usage counters
// included.
//
// It goes through Delete one by one for the same reason evictOldestSessions does: a session needs four things cleared, so a
// batch version would just be a copy of Delete. A single failure is logged and skipped, so one broken session does not block
// the rest, and the caller learns how many went away.
func (r *PostgresRepository) DeleteByOwner(schema, ownerType, ownerID string) (int, error) {
	list, err := r.ListByOwner(schema, ownerType, ownerID, "", -1, "")
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, s := range list {
		if derr := r.Delete(schema, s.ID); derr != nil {
			xlog.Error("failed to delete session schema=%s session=%s: %v", schema, s.ID, derr)
			continue
		}
		deleted++
	}
	return deleted, nil
}

// nullIfEmpty turns an empty string into nil (a uuid column does not accept an empty string).
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// Delete deletes a session (its session_records and its workspace directory included).
func (r *PostgresRepository) Delete(schema, sessionID string) error {
	// Read user_id first: the workspace directory is organised by user, so it cannot be located after the row is gone
	sess, _ := r.Get(schema, sessionID)

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM `+schema+`.session_records WHERE session_id = $1`, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+schema+`.sessions WHERE id = $1`, sessionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if sess != nil {
		removeSessionDir(schema, sess.UserID, sessionID)
		// The directory is gone, so clear the usage counter too (otherwise a re-created session under the same key would
		// inherit the old usage)
		quota.Reset(context.Background(), schema, paths.UserSessionKey(schema, sess.UserID, sessionID))
	}
	return nil
}

// ============================================================================
// Session workspace lifecycle (kept in step with the DB row)
//   Shape: users/{uid}/sessions/{sid}/; not created when user is empty (directories are organised by user).
// ============================================================================

var (
	sessStoreOnce sync.Once
	sessStore     storage.Storage
	sessStoreErr  error
)

// sessionStorage lazily builds the storage instance used for session directories (same config as agent storage).
func sessionStorage() (storage.Storage, error) {
	sessStoreOnce.Do(func() {
		if config.Cfg == nil {
			sessStoreErr = fmt.Errorf("config not loaded")
			return
		}
		sessStore, sessStoreErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return sessStore, sessStoreErr
}

// ensureSessionDir creates the session workspace (S3 has no empty directories, so a .keep placeholder is written).
func ensureSessionDir(schema, userID, sessionID string) {
	if userID == "" {
		return
	}
	st, err := sessionStorage()
	if err != nil || st == nil {
		return
	}
	key := path.Join(paths.UserSessionKey(schema, userID, sessionID), ".keep")
	_ = st.WriteFile(context.Background(), key, nil)
}

// removeSessionDir deletes the session workspace (a missing directory is skipped).
func removeSessionDir(schema, userID, sessionID string) {
	if userID == "" {
		return
	}
	st, err := sessionStorage()
	if err != nil || st == nil {
		return
	}
	_ = st.DeletePrefix(context.Background(), paths.UserSessionKey(schema, userID, sessionID))
}
