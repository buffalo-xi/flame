package agent

import (
	"context"
	"database/sql"
	"testing"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	flows "flame/nexus/shared/repository/flows"
)

// fakeFlowRepo only cares about find's dispatch: it records what "look up by id" received and how often the name lookup was called.
// The other methods are unused, so embedding a nil interface is enough to satisfy flows.Repository.
type fakeFlowRepo struct {
	flows.Repository
	rootID   string
	getIDs   []string
	listHits int
}

func (f *fakeFlowRepo) GetFlow(_ *auth.AuthInfo, id string) (*models.Flow, error) {
	f.getIDs = append(f.getIDs, id)
	if id == f.rootID {
		return &models.Flow{ID: id, Name: "weekly-report"}, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeFlowRepo) ListFlows(_ *auth.AuthInfo, req *flows.ListFlowsRequest) ([]models.Flow, int, error) {
	f.listHits++
	return []models.Flow{{ID: f.rootID, Name: req.Name}}, 1, nil
}

// A name must never be passed into `WHERE id = $1` as an id: flows.id is a uuid column, so Postgres reports 22P02
// (invalid input syntax for type uuid) instead of "no such row", and the name fallback could never be reached.
// The tool layer always passes (ref, ref), so find has to decide on the shape itself.
func TestFindDoesNotTreatNameAsID(t *testing.T) {
	const id = "0190f000-0000-7000-8000-000000000001"
	repo := &fakeFlowRepo{rootID: id}
	m := &flowsManager{schema: "t0", userID: "u1", repo: repo}

	f, err := m.find(context.Background(), "weekly-report", "weekly-report")
	if err != nil {
		t.Fatalf("find = %v", err)
	}
	if f == nil || f.ID != id {
		t.Fatalf("the name lookup should hit %s, got %+v", id, f)
	}
	for _, got := range repo.getIDs {
		if got == "weekly-report" {
			t.Fatalf("the name was queried as an id once (getIDs = %v)", repo.getIDs)
		}
	}
	if repo.listHits != 1 {
		t.Errorf("name lookups = %d, want 1", repo.listHits)
	}
}

// A UUID shape is looked up by id directly, without scanning the list again.
func TestFindUsesIDDirectly(t *testing.T) {
	const id = "0190f000-0000-7000-8000-000000000001"
	repo := &fakeFlowRepo{rootID: id}
	m := &flowsManager{schema: "t0", userID: "u1", repo: repo}

	f, err := m.find(context.Background(), id, id)
	if err != nil || f == nil || f.ID != id {
		t.Fatalf("find(id) = %+v, %v", f, err)
	}
	if repo.listHits != 0 {
		t.Errorf("the name list must not be scanned when the id hits, but it was scanned %d times", repo.listHits)
	}
}

// The stop summary is derived, so its rule has to be exactly "the first successful exit node": a skipped or failed exit
// proves nothing, and a second exit can never have run (the first one ended the run).
func TestRunStopSummary(t *testing.T) {
	node := func(id, typ, status string) *models.FlowNodeRecord {
		return &models.FlowNodeRecord{NodeID: id, NodeType: typ, Status: status}
	}
	cases := []struct {
		name   string
		nodes  []*models.FlowNodeRecord
		reason string
		nodeID string
	}{
		{"normal end", []*models.FlowNodeRecord{node("1", "start", "success"), node("2", "code", "success")}, "", ""},
		{"exit ends the run", []*models.FlowNodeRecord{node("1", "code", "success"), node("7", "exit", "success")}, "exit", "7"},
		{"a skipped exit proves nothing", []*models.FlowNodeRecord{node("7", "exit", "skipped")}, "", ""},
		{"a failed exit is a failed run", []*models.FlowNodeRecord{node("7", "exit", "failed")}, "", ""},
		{"first successful exit wins", []*models.FlowNodeRecord{
			node("5", "exit", "success"), node("9", "exit", "success"),
		}, "exit", "5"},
	}
	for _, c := range cases {
		reason, nodeID := runStopSummary(c.nodes)
		if reason != c.reason || nodeID != c.nodeID {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", c.name, reason, nodeID, c.reason, c.nodeID)
		}
	}
}

// flow_run_records.id is a uuid column too: a non-UUID run_id has to count as "does not exist",
// and the 22P02 SQL error must not be thrown at the model.
func TestGetRunTreatsNonUUIDAsNotFound(t *testing.T) {
	m := &flowsManager{schema: "t0", userID: "u1"}
	res, err := m.GetRun(context.Background(), "not-a-run-id")
	if err != nil {
		t.Fatalf("a non-UUID run_id must not be an error: %v", err)
	}
	if res != nil {
		t.Fatalf("should be treated as missing, got %+v", res)
	}
}
