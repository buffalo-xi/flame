package agents

import "testing"

// checkJSONPayload is pure string logic, so it can be tested without gin or storage.
func TestCheckJSONPayload(t *testing.T) {
	cases := []struct {
		config, endpoints string
		wantField         string
	}{
		{"", "", ""},
		{`{}`, `[]`, ""},
		{`{"max_turns":10}`, `["/v1/chat"]`, ""},
		{`{`, "", "config"},
		{"max_turns", "", "config"},
		{`{}`, `[`, "endpoints"},
		// Both invalid: the first field in declaration order wins.
		{`{`, `[`, "config"},
	}
	for _, c := range cases {
		req := &agentRequest{Config: c.config, Endpoints: c.endpoints}
		field, ok := req.checkJSONPayload()
		if ok != (c.wantField == "") || field != c.wantField {
			t.Errorf("checkJSONPayload(config=%q, endpoints=%q) = (%q, %v), want (%q, %v)",
				c.config, c.endpoints, field, ok, c.wantField, c.wantField == "")
		}
	}
}
