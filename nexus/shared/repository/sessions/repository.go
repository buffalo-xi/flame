package sessions

import (
	"errors"
	"strings"

	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// The three ways session resolution can fail. They are kept apart so the entry points can map them to different protocol
// error codes, while guaranteeing that "does not exist" and "is not yours" always merge into one outward message (see
// Lookup).
var (
	// ErrSessionRequired means a session_id was needed but not supplied.
	ErrSessionRequired = errors.New("session_id is required")
	// ErrSessionInvalid means session_id is not a valid UUID.
	// It must be blocked up front: sessions.id and session_records.session_id are both uuid columns, and handing a dirty
	// string to PG only buys "invalid input syntax for type uuid".
	ErrSessionInvalid = errors.New("session_id is not a valid UUID")
	// ErrSessionForbidden means the session does not exist, or does not belong to this owner.
	// The two cases deliberately share one error: separating them would hand out an interface for probing whether a sid
	// exists.
	ErrSessionForbidden = errors.New("session not found or not owned by this owner")
)

// ParseID normalises and validates a session id supplied by the client: empty -> ErrSessionRequired; not a UUID ->
// ErrSessionInvalid.
//
// Every place that hands a sid to SQL must pass through here first - sessions.id and session_records.session_id are both
// uuid columns, so a dirty string buys PG's 22P02, which is neither an error meant for the caller nor free of the
// implementation detail "this value was used as an id".
func ParseID(sessionID string) (string, error) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", ErrSessionRequired
	}
	if _, err := uuid.Parse(sid); err != nil {
		return "", ErrSessionInvalid
	}
	return sid, nil
}

// ResolveForUser resolves a session_id by "the user the session belongs to" - the single criterion for external entry
// points (HTTP chat / MCP / RPC).
//
// It differs from Lookup in that it checks only user_id, never the owner. A session belongs to a user; owner_type /
// owner_id merely record who created it, and when a parent and a child agent share one session the owner cannot match -
// gating on the owner would reject call_agent's legitimate cascade wholesale.
//
// Failure modes match Lookup: empty -> ErrSessionRequired; not a UUID -> ErrSessionInvalid; missing or not owned by that
// user -> ErrSessionForbidden (one shared message, to defeat probing).
func ResolveForUser(schema, sessionID, userID string) (*models.Session, error) {
	id, err := ParseID(sessionID)
	if err != nil {
		return nil, err
	}
	sess, err := NewRepository().Get(schema, id)
	if err != nil {
		return nil, err
	}
	if !sess.VisibleToUser(userID) {
		return nil, ErrSessionForbidden
	}
	return sess, nil
}

// ResolveOrCreateForUser resolves a session by the user criterion, creating it with that id when it does not exist (subject
// to the session-count quota).
//
// For protocol endpoints (RPC / MCP) only: the caller brings its own UUID, so the first call is also the first record. Like
// ResolveForUser it checks only user_id - a session belongs to a user, and the owner merely records who created it.
func ResolveOrCreateForUser(schema, sessionID, ownerType, ownerID, userID, title string) (*models.Session, error) {
	id, err := ParseID(sessionID)
	if err != nil {
		return nil, err
	}
	repo := NewRepository()
	sess, err := repo.Get(schema, id)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		if !sess.VisibleToUser(userID) {
			return nil, ErrSessionForbidden
		}
		return sess, nil
	}
	return repo.CreateWithID(schema, id, ownerType, ownerID, userID, title)
}

// Repository is the session store interface (generalised: agent + flow).
type Repository interface {
	// Create creates a session (userID may be empty) and returns its ID.
	// Over the session-count quota it returns *quota.ExceededError (the console turns that into a 507, and the human
	// deletes old sessions before retrying).
	Create(schema, ownerType, ownerID, userID, title string) (string, error)
	// CreateWithID idempotently creates a session with the given id: on conflict it reads back and checks visibility, and
	// it goes through the session-count quota.
	// Protocol endpoints (which bring their own UUID) use this, not EnsureExists.
	// The difference from Create: over the limit it reclaims this user's oldest session and then creates (endpoints have
	// no delete API).
	CreateWithID(schema, id, ownerType, ownerID, userID, title string) (*models.Session, error)
	// ListByOwner lists one owner's sessions (updated_at descending).
	// A non-empty userID filters by user; limit<=0 does not limit; a non-empty keyword matches the title fuzzily.
	ListByOwner(schema, ownerType, ownerID, userID string, limit int, keyword string) ([]models.Session, error)
	// ListByUser lists all of one user's sessions under the given owner_type (updated_at descending). limit<=0 does not
	// limit.
	ListByUser(schema, ownerType, userID string, limit int) ([]models.Session, error)
	// Get reads a session by ID; (nil, nil) when it does not exist.
	Get(schema, sessionID string) (*models.Session, error)
	// Lookup reads a session by ID and checks its "creator" (ownerType, ownerID):
	// empty -> ErrSessionRequired; not a UUID -> ErrSessionInvalid;
	// missing or not owned by (ownerType, ownerID) -> ErrSessionForbidden (one shared message, to defeat probing).
	//
	// External entry points (HTTP chat / MCP / RPC) should use ResolveForUser and gate on the user: a session belongs to a
	// user, the owner only records who created it, and when a parent and a child agent share one session the owner cannot
	// match. The owner dimension stays here for cases with no login state that only need "did this owner create it", such
	// as server-signed tickets.
	Lookup(schema, sessionID, ownerType, ownerID string) (*models.Session, error)
	// UpdateTitle updates the title and refreshes updated_at.
	UpdateTitle(schema, sessionID, title string) error
	// Touch refreshes updated_at (called after a turn).
	Touch(schema, sessionID string) error
	// EnsureExists makes sure the session with the given id exists: missing means create it with that id, present means
	// refresh updated_at.
	EnsureExists(schema, id, ownerType, ownerID, userID, title string) error
	// Delete deletes a session (its session_records, workspace directory and usage counter included).
	Delete(schema, sessionID string) error
	// DeleteByOwner deletes every session of one owner (agent / flow) - records, rows, workspace directories and usage
	// counters included - and returns how many were removed.
	//
	// Deleting an agent or a flow must call this: once the owner is gone its sessions can no longer be listed anywhere
	// (the console lists them per agent / flow), yet they keep counting against the user's session quota and keep their
	// workspaces on disk.
	DeleteByOwner(schema, ownerType, ownerID string) (int, error)
}
