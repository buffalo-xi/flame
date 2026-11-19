package share

import "testing"

// When packaging, "custom tools that travel with the bundle" are recognised only as custom:<name>.
//
// Getting this rule wrong produces two very different symptoms, and both really happened:
//   - plugin:web_search taken as a directory name -> a colon is illegal in a Windows file name -> the whole listing failed;
//   - custom:web_search used as the directory name including the prefix -> tools/custom:web_search does not exist ->
//     the contract read got ErrNoRows and was skipped silently -> not one custom tool travelled with the bundle.
func TestCustomToolNameOfRef(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"custom:web_search", "web_search"},
		{"custom:Weather-2", "Weather-2"},
		{"  custom:weather  ", "weather"}, // ParseRef trims spaces
		{"plugin:web_search", ""},         // plugins come from the disk implementation, no body
		{"plugin:web_search:extra", ""},   // a plugin has no third segment
		{"builtin:read_file", ""},
		{"builtin:file", ""},
		{"group:ops", ""},
		{"preset:agent-manage", ""},
		{"mcp:fs:read_file", ""},
		{"rpc:planner:run", ""},
		{"local:rpc:poet", ""},
		{"", ""},
		{"custom:", ""},        // missing subject
		{"custom:../evil", ""}, // the subject contains a path separator
		{"custom:..", ""},      // the subject cannot start with a dot
		{"nonsense", ""},       // no scope
		{"weather", ""},        // no compatibility for legacy bare names: when in doubt, do not package
		{"weird:x:y:z", ""},    // too many segments
	}
	for _, c := range cases {
		if got := customToolNameOfRef(c.ref); got != c.want {
			t.Errorf("customToolNameOfRef(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}
