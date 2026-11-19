package flows

import (
	"encoding/json"
	"testing"
)

// validateDiagramLabels is the hard check at the save point: the canvas submits a JSON string
// (the front end stringifies once) while tools write an object, and both shapes must be caught.
func TestValidateDiagramLabels(t *testing.T) {
	cases := []struct {
		name     string
		diagram  string
		wantCode string
	}{
		{
			name:    "unique across the diagram",
			diagram: `{"nodes":[{"id":"1","data":{"label":"Start"}},{"id":"2","data":{"label":"Code"}}]}`,
		},
		{
			name:     "duplicate in the object shape",
			diagram:  `{"nodes":[{"id":"2","data":{"label":"Code"}},{"id":"3","data":{"label":"Code"}}]}`,
			wantCode: "flow.duplicate_label",
		},
		{
			name:     "duplicate in the string shape submitted by the canvas",
			diagram:  `"{\"nodes\":[{\"id\":\"2\",\"data\":{\"label\":\"Code\"}},{\"id\":\"3\",\"data\":{\"label\":\"Code\"}}]}"`,
			wantCode: "flow.duplicate_label",
		},
		{
			name:    "an empty label does not take part",
			diagram: `{"nodes":[{"id":"2","data":{}},{"id":"3","data":{}}]}`,
		},
		{
			name:    "empty diagram",
			diagram: ``,
		},
		{
			name:    "an unknown shape is let through (handled at run time)",
			diagram: `[]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, params, msg := validateDiagramLabels(json.RawMessage(tc.diagram))
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if code == "" {
				return
			}
			if params == nil || params["names"] == "" {
				t.Fatalf("params = %v, want the duplicated names", params)
			}
			if msg == "" {
				t.Fatal("the operator diagnostic is empty")
			}
		})
	}
}
