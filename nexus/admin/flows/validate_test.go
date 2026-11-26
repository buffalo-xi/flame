package flows

import (
	"encoding/json"
	"testing"

	"flame/nexus/shared/flowcheck"
)

// The editor's pre-save check must report the same findings the agent's validate_flow tool does: one rule set, one list.
func TestFlowFindings(t *testing.T) {
	fs := flowFindings(json.RawMessage(`{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"process","data":{"config":{}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","data":{"config":{"code":{"script":"return {}"}}}}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e2","source":"3","target":"4"}
	]}`))
	errs, _ := flowcheck.Summarize(fs)
	if errs == 0 {
		t.Fatalf("a cross-container edge must be reported, got %v", fs)
	}
	found := false
	for _, f := range fs {
		if f.Code == flowcheck.CodeCrossParent {
			found = true
			if f.NodeID == "" {
				t.Errorf("a structural finding must carry the node it starts from: %v", f)
			}
		}
	}
	if !found {
		t.Fatalf("want a cross_parent finding, got %v", fs)
	}
}

// A body that is not a diagram is still one response shape: a shape finding, not a transport error.
func TestFlowFindingsOnUnparsableDiagram(t *testing.T) {
	fs := flowFindings(json.RawMessage(`"not a diagram"`))
	if len(fs) != 1 || fs[0].Severity != flowcheck.SeverityError || fs[0].Code != flowcheck.CodeShape {
		t.Fatalf("want one shape error, got %v", fs)
	}
	if fs[0].Message == "" {
		t.Error("the operator diagnostic is empty")
	}
}

// A clean diagram must produce an empty list, not nil: the front end reads findings.length.
func TestFlowFindingsClean(t *testing.T) {
	fs := flowFindings(json.RawMessage(`{"nodes":[{"id":"1","type":"start"}],"edges":[]}`))
	if fs == nil || len(fs) != 0 {
		t.Fatalf("want an empty (non-nil) list, got %v", fs)
	}
}
