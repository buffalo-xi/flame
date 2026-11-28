package executors

import (
	"testing"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/service/flow/core"
)

// Every trace carries the node's own window: without it "did these two members overlap" cannot be answered from run_flow, and
// the only alternative is comparing total run durations by eye.
func TestCollectNodeTracesCarriesNodeWindow(t *testing.T) {
	ctx := newContainerContext(t, nil)
	finished := flow.NewNodeResult(&flow.Node{ID: "3", Type: "code"}, "p1", ctx)
	finished.Finish(flow.StatusSuccess, "", "")
	running := flow.NewNodeResult(&flow.Node{ID: "4", Type: "code"}, "p2", ctx)
	ctx.ExecutedNodeMap.Set("p1_3", finished)
	ctx.ExecutedNodeMap.Set("p2_4", running)

	traces := collectNodeTraces(ctx)
	if len(traces) != 2 {
		t.Fatalf("traces = %d, want 2", len(traces))
	}
	byID := map[string]tools.FlowNodeTrace{}
	for _, tr := range traces {
		byID[tr.NodeID] = tr
	}
	done, ok := byID["3"]
	if !ok {
		t.Fatalf("no trace for node 3: %+v", traces)
	}
	start, err := time.Parse(time.RFC3339Nano, done.StartTime)
	if err != nil {
		t.Fatalf("start_time = %q, want an RFC3339 timestamp: %v", done.StartTime, err)
	}
	end, err := time.Parse(time.RFC3339Nano, done.EndTime)
	if err != nil {
		t.Fatalf("end_time = %q, want an RFC3339 timestamp: %v", done.EndTime, err)
	}
	if end.Before(start) {
		t.Errorf("window = %s..%s, want the end after the start", done.StartTime, done.EndTime)
	}
	// A node still running has no end: the field must be empty rather than year 1, which would read as a real timestamp.
	if pending := byID["4"]; pending.EndTime != "" {
		t.Errorf("end_time = %q for a node that never finished, want empty", pending.EndTime)
	}
}

// stopOfTraces asks the same question as runStopSummary on the stored side ("did a successful exit node end the run?"), so
// the two must not drift. Traces arrive in ascending start order, which is what makes "the first one" well defined.
func TestStopOfTraces(t *testing.T) {
	trace := func(id, typ, status string) tools.FlowNodeTrace {
		return tools.FlowNodeTrace{NodeID: id, Type: typ, Status: status}
	}
	cases := []struct {
		name   string
		nodes  []tools.FlowNodeTrace
		reason string
		nodeID string
	}{
		{"normal end", []tools.FlowNodeTrace{trace("1", "start", "success")}, "", ""},
		{"exit ends the run", []tools.FlowNodeTrace{trace("1", "code", "success"), trace("7", "exit", "success")}, "exit", "7"},
		{"a skipped or failed exit proves nothing", []tools.FlowNodeTrace{
			trace("7", "exit", "skipped"), trace("8", "exit", "failed"),
		}, "", ""},
		{"first successful exit wins", []tools.FlowNodeTrace{
			trace("5", "exit", "success"), trace("9", "exit", "success"),
		}, "exit", "5"},
	}
	for _, c := range cases {
		reason, nodeID := stopOfTraces(c.nodes)
		if reason != c.reason || nodeID != c.nodeID {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", c.name, reason, nodeID, c.reason, c.nodeID)
		}
	}
}
