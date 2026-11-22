package flows

import (
	"encoding/json"
	"testing"

	"flame/nexus/shared/flowconfig"
)

// The node count is a tenant quota checked at the write boundary: the diagram is fixed for the
// life of a run, so a runtime ceiling could never fire.
func TestValidateDiagramNodeCount(t *testing.T) {
	limits := flowconfig.FlowLimits{MaxNodeCount: 2}
	cases := []struct {
		name     string
		diagram  string
		wantCode string
	}{
		{
			name:    "within the limit",
			diagram: `{"nodes":[{"id":"1"},{"id":"2"}]}`,
		},
		{
			name:     "over the limit",
			diagram:  `{"nodes":[{"id":"1"},{"id":"2"},{"id":"3"}]}`,
			wantCode: "flow.node_limit_exceeded",
		},
		{
			name:    "comments do not count",
			diagram: `{"nodes":[{"id":"1"},{"id":"2"},{"id":"3","type":"comment"}]}`,
		},
		{
			name:     "the string shape submitted by the canvas",
			diagram:  `"{\"nodes\":[{\"id\":\"1\"},{\"id\":\"2\"},{\"id\":\"3\"}]}"`,
			wantCode: "flow.node_limit_exceeded",
		},
		{
			name:    "an unknown shape is let through (reported at run time)",
			diagram: `[]`,
		},
		{
			name:    "an empty diagram is a valid empty draft",
			diagram: ``,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, params, msg := validateDiagramNodeCount(limits, json.RawMessage(tc.diagram))
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if code == "" {
				return
			}
			if params == nil || params["count"] == nil || params["limit"] == nil {
				t.Fatalf("params = %v, want count and limit", params)
			}
			if msg == "" {
				t.Fatal("the operator diagnostic is empty")
			}
		})
	}
}

// A tenant quota of -1 means "unlimited": the check must never reject.
func TestValidateDiagramNodeCountUnlimited(t *testing.T) {
	limits := flowconfig.FlowLimits{MaxNodeCount: -1}
	code, _, _ := validateDiagramNodeCount(limits, json.RawMessage(`{"nodes":[{"id":"1"},{"id":"2"},{"id":"3"}]}`))
	if code != "" {
		t.Fatalf("code = %q, want no rejection for an unlimited quota", code)
	}
}
