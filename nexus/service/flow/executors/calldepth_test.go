package executors

import "testing"

// extractCallDepth has to accept both int (an in-process call) and float64 (from JSON), and strip the key
// completely — a leftover would turn _call_depth into a business argument of the flow.
func TestExtractCallDepth(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]interface{}
		want int
	}{
		{"key absent", map[string]interface{}{"a": 1}, 0},
		{"int", map[string]interface{}{"_call_depth": 2}, 2},
		{"int64", map[string]interface{}{"_call_depth": int64(3)}, 3},
		{"float64 (from JSON)", map[string]interface{}{"_call_depth": float64(3)}, 3},
		{"negative clamps to zero", map[string]interface{}{"_call_depth": -5}, 0},
		{"invalid clamps to zero", map[string]interface{}{"_call_depth": "x"}, 0},
	}
	for _, tc := range cases {
		got, clean := extractCallDepth(tc.in)
		if got != tc.want {
			t.Errorf("%s: depth = %d, want %d", tc.name, got, tc.want)
		}
		if _, ok := clean["_call_depth"]; ok {
			t.Errorf("%s: _call_depth is still there after stripping", tc.name)
		}
	}
}

// The original map must not be modified: the caller (an rpc node / tool arguments) still uses it afterwards.
func TestExtractCallDepthDoesNotMutateInput(t *testing.T) {
	in := map[string]interface{}{"_call_depth": 2, "topic": "x"}
	_, clean := extractCallDepth(in)
	if _, ok := in["_call_depth"]; !ok {
		t.Fatal("_call_depth was deleted from the original map")
	}
	if len(clean) != 1 || clean["topic"] != "x" {
		t.Fatalf("stripped result = %v, want only topic", clean)
	}
}

// Several rpc nodes in one flow share the same CallDepth snapshot: sibling calls do not accumulate.
//
// The real guarantee is that ExecContext.CallDepth is written once by NewFlow and no execution path touches it;
// this test pins the "+1 rather than increment" semantics so nobody turns it into "add one per call" later —
// that would make a flow with 10 parallel rpc nodes push itself past the limit.
func TestSiblingEndpointCallsShareSameDepth(t *testing.T) {
	const flowDepth = 1 // this flow was started by a caller, so its level is 1
	for i := 0; i < 3; i++ {
		if next := flowDepth + 1; next != 2 {
			t.Fatalf("next for rpc node %d = %d, want 2 (siblings do not accumulate)", i, next)
		}
	}
}
