package agents

import (
	"strconv"
	"testing"

	"flame/nexus/shared/models"
)

// makeRecords builds n records with ids r0..r(n-1) in the same order ListBySessionPage returns them: oldest first.
func makeRecords(n int) []models.SessionRecord {
	out := make([]models.SessionRecord, n)
	for i := range out {
		out[i] = models.SessionRecord{ID: "r" + strconv.Itoa(i)}
	}
	return out
}

// Regression cases for pageRecords.
//
// Historical bug: it sliced records[:limit] (dropping from the tail). Because records is oldest-first,
// dropping from the tail removed exactly the **newest** record: past 10 records in a session,
// opening it from the sidebar never showed the last record.
func TestPageRecords(t *testing.T) {
	t.Run("fetches one extra: drop the oldest from the head, keep the newest", func(t *testing.T) {
		got, hasMore := pageRecords(makeRecords(11), 10)
		if !hasMore {
			t.Fatal("an extra record was fetched, so older records exist and hasMore should be true")
		}
		if len(got) != 10 {
			t.Fatalf("len = %d, want 10", len(got))
		}
		if got[0].ID != "r1" {
			t.Errorf("the head should be r1 (the oldest r0 was dropped), got %s", got[0].ID)
		}
		if got[len(got)-1].ID != "r10" {
			t.Errorf("the newest record r10 must be kept, got %s", got[len(got)-1].ID)
		}
	})

	t.Run("exactly one page: no truncation and no older record", func(t *testing.T) {
		got, hasMore := pageRecords(makeRecords(10), 10)
		if hasMore {
			t.Error("length equals the limit, so hasMore should be false")
		}
		if len(got) != 10 || got[9].ID != "r9" {
			t.Errorf("the page result must not change: len=%d last=%s", len(got), got[len(got)-1].ID)
		}
	})

	t.Run("less than one page", func(t *testing.T) {
		got, hasMore := pageRecords(makeRecords(3), 10)
		if hasMore || len(got) != 3 {
			t.Errorf("len=%d hasMore=%v, want len=3 hasMore=false", len(got), hasMore)
		}
	})

	t.Run("empty list", func(t *testing.T) {
		got, hasMore := pageRecords(nil, 10)
		if hasMore || len(got) != 0 {
			t.Errorf("len=%d hasMore=%v, want len=0 hasMore=false", len(got), hasMore)
		}
	})
}
