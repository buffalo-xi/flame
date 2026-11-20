package sessionrecords

import (
	"time"

	"flame/nexus/shared/models"
)

// Repository is the session record storage interface.
type Repository interface {
	// Append appends one record and trims the session's oldest records beyond maxN (maxN<=0 trims nothing).
	Append(schema string, rec *models.SessionRecord, maxN int) error
	// ListBySession reads the history of one owner + session (old to new).
	// limit<=0 means no limit; a non-nil since keeps only records with created_at >= since.
	ListBySession(schema, ownerType, ownerID, sessionID string, limit int, since *time.Time) ([]models.SessionRecord, error)
	// ListBySessionPage reads history page by page: the latest limit records with created_at < before, returned old to
	// new. A nil before starts from the newest; limit<=0 means no limit.
	ListBySessionPage(schema, ownerType, ownerID, sessionID string, limit int, before *time.Time) ([]models.SessionRecord, error)
}
