package agentruns

import (
	"time"

	"flame/nexus/shared/models"
)

// Filter is the admin console's criteria for querying past agent runs. Every field is optional and a zero
// value means "no filter".
//
// It is deliberately separate from sessionrecords.RecordFilter: that one queries memory rows (which are
// always bound to a session), while this one queries the audit log, where a stateless run has no session.
type Filter struct {
	AgentID   string
	SessionID string
	UserID    string // who triggered the run; empty filters nothing (rows recorded before the column had a value stay visible)
	Status    string
	RunType   string
	From      *time.Time
	To        *time.Time
}

// Empty reports whether no criterion was given at all. The caller uses it to refuse an unconditional
// bulk delete before it reaches the database.
func (f Filter) Empty() bool {
	return f.AgentID == "" && f.SessionID == "" && f.UserID == "" && f.Status == "" && f.RunType == "" &&
		f.From == nil && f.To == nil
}

// Repository is the agent run log storage interface.
type Repository interface {
	// SaveRun saves one run record and trims the agent's oldest records beyond maxN (maxN <= 0 trims nothing).
	SaveRun(schema string, run *models.AgentRunRecord, maxN int) error

	// ListRuns lists past runs across the tenant (newest start first), returning the page and the total.
	ListRuns(schema string, f Filter, limit int, offset int) ([]*models.AgentRunRecord, int, error)

	// GetRun returns one run record by id (nil when it does not exist).
	GetRun(schema, id string) (*models.AgentRunRecord, error)

	// DeleteRuns deletes records by criteria, returning the number removed.
	// An empty filter is refused: a bulk clean-up without a condition would wipe the tenant's log.
	DeleteRuns(schema string, f Filter) (int64, error)

	// DeleteRun deletes one record by id, returning the number removed.
	DeleteRun(schema, id string) (int64, error)
}

// NewRepository creates the agent run log storage instance.
func NewRepository() Repository {
	return &PostgresRepository{}
}
