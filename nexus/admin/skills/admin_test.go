package skills

import "testing"

// TestNormalizeCommandInput pins the repair rules: only the dispatch mode can make the pair invalid, and the pair is always
// stored in one of the two shapes the rest of the system understands.
func TestNormalizeCommandInput(t *testing.T) {
	cases := []struct {
		name           string
		dispatch, tool string
		wantDispatch   string
		wantTool       string
		wantErr        bool
	}{
		{"nothing set", "", "", "", "", false},
		// The target field is hidden while the dispatch mode is empty, so a value there is invisible to the user and must not survive.
		{"target without a mode", "", "local:rpc:daily-report", "", "", false},
		// The editor saves the moment "tool" is picked, before the target is typed: this must not be an error.
		{"tool without a target", "tool", "", "tool", "", false},
		{"complete", "tool", "local:rpc:daily-report", "tool", "local:rpc:daily-report", false},
		{"surrounding spaces", "  tool  ", "  local:rpc:daily-report  ", "tool", "local:rpc:daily-report", false},
		{"unknown mode", "flow", "local:rpc:daily-report", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatch, tool, err := normalizeCommandInput(tc.dispatch, tc.tool)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if dispatch != tc.wantDispatch || tool != tc.wantTool {
				t.Fatalf("(dispatch, tool) = (%q, %q), want (%q, %q)", dispatch, tool, tc.wantDispatch, tc.wantTool)
			}
		})
	}
}
