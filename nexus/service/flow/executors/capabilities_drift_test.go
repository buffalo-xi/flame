package executors

import (
	"sort"
	"testing"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/flowcheck"
)

// flowcheck keeps a copy of every node's capability declaration: importing executors from there would close a cycle,
// because executors already imports service/agent/tools (flowtest.go). This test is what keeps the copy honest - a new
// executor, a changed action list or a new error code fails here instead of silently going unchecked.
func TestCapabilitiesMatchFlowcheck(t *testing.T) {
	for _, typ := range RegisteredTypes() {
		ex, ok := GetExecutor(typ)
		if !ok {
			t.Fatalf("%s has no executor", typ)
		}
		want := ex.Capabilities()
		got, ok := flowcheck.CapabilitiesOf(typ)
		if !ok {
			t.Errorf("%s has an executor but no entry in flowcheck's capability table", typ)
			continue
		}
		if !sameSet(want.ErrorCodes, got.ErrorCodes) {
			t.Errorf("%s error codes: executors %v, flowcheck %v", typ, want.ErrorCodes, got.ErrorCodes)
		}
		wantActions := make([]string, 0, len(want.Actions))
		for _, a := range want.Actions {
			wantActions = append(wantActions, string(a))
		}
		if !sameSet(wantActions, got.Actions) {
			t.Errorf("%s actions: executors %v, flowcheck %v", typ, wantActions, got.Actions)
		}
		if want.HasPost != got.HasPost {
			t.Errorf("%s HasPost: executors %v, flowcheck %v", typ, want.HasPost, got.HasPost)
		}
		if !sameSet(want.FixedOutputs, got.FixedOutputs) {
			t.Errorf("%s fixed outputs: executors %v, flowcheck %v", typ, want.FixedOutputs, got.FixedOutputs)
		}
	}

	if !sameSet(flow.AllErrorCodes(), flowcheck.AllErrorCodes) {
		t.Errorf("all error codes: core %v, flowcheck %v", flow.AllErrorCodes(), flowcheck.AllErrorCodes)
	}

	// The raisable set is a second copy for the same cycle reason: a code added on one side and not the other would let
	// the editor accept what the runner refuses, or the reverse.
	var raisable []string
	for _, c := range flow.AllErrorCodes() {
		if IsRaisableCode(c) {
			raisable = append(raisable, c)
		}
	}
	if !sameSet(raisable, flowcheck.RaisableCodes) {
		t.Errorf("raisable codes: executors %v, flowcheck %v", raisable, flowcheck.RaisableCodes)
	}
}

// sameSet compares two lists as sets: order carries no meaning in a capability declaration.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
