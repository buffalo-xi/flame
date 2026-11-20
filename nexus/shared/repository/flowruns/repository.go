package flowruns

import (
	"time"

	"flame/nexus/shared/models"
)

// Repository is the workflow run record repository interface.
type Repository interface {
	// SaveRun saves one run record (the run plus all its nodes) inside a transaction,
	// and trims the workflow's oldest records beyond maxRunRecords (maxRunRecords <= 0 trims nothing).
	SaveRun(schema string, run *models.FlowRunRecord, nodes []*models.FlowNodeRecord, maxRunRecords int) error

	// ListRuns lists a workflow's past runs (newest start first).
	// An empty runType filters nothing; an empty status filters nothing; a nil startFrom/startTo filters nothing. It
	// returns the page and the total.
	ListRuns(schema string, flowId string, runType string, status string, startFrom, startTo *time.Time, limit int, offset int) ([]*models.FlowRunRecord, int, error)

	// GetRunDetail returns one run record with all of its node records (nodes by start time ascending).
	GetRunDetail(schema string, runID string) (*models.FlowRunRecord, []*models.FlowNodeRecord, error)

	// DeleteRun deletes one run record of a workflow together with its node records, returning the number of runs removed.
	// flowId must always be given as well: deleting by runId alone would turn "delete a record of my own workflow" into
	// "delete any record".
	DeleteRun(schema, flowId, runID string) (int64, error)

	// DeleteRuns deletes every run record of a workflow together with their node records, returning the number of runs
	// removed.
	DeleteRuns(schema, flowId string) (int64, error)

	// DeleteRunsByFilter deletes run records in bulk by optional criteria (workflow / status / time range), returning the
	// number of runs removed.
	// Unconditional deletion is refused outright: the admin side's bulk clean-up without a filter would wipe the table on
	// one mistaken call.
	DeleteRunsByFilter(schema, flowId, status string, startFrom, startTo *time.Time) (int64, error)
}

// NewRepository creates the run record storage instance.
func NewRepository() Repository {
	return &PostgresRepository{}
}
