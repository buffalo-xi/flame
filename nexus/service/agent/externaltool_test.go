package agent

import (
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
)

// Connection names may contain underscores (toolgrant's namePattern), so the mcp__ / rpc__ split point cannot blindly cut at "__" - the
// longest matching connection name wins. These cases pin down the shapes that would go wrong.
func TestSplitExternalName(t *testing.T) {
	cases := []struct {
		name      string
		prefix    string
		connNames []string
		wantConn  string
		wantTool  string
		wantOK    bool
	}{
		{
			name: "mcp__fs__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantConn:  "fs", wantTool: "read_file", wantOK: true,
		},
		{
			// the connection name itself contains underscores: a blind "__" cut yields ("fs", "prod__read_file")
			name: "mcp__fs__prod__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs", "fs__prod"},
			wantConn:  "fs__prod", wantTool: "read_file", wantOK: true,
		},
		{
			// with only one short connection there is no over-eager guessing: a tool name containing __ is correct too
			name: "mcp__fs__prod__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantConn:  "fs", wantTool: "prod__read_file", wantOK: true,
		},
		{
			name: "rpc__erp__create_order", prefix: tools.ExternalPrefixRPC,
			connNames: []string{"erp", "crm"},
			wantConn:  "erp", wantTool: "create_order", wantOK: true,
		},
		{
			// connection names are case-insensitive (the same criterion as EqualFold on the catalog side)
			name: "mcp__FS__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantConn:  "fs", wantTool: "read_file", wantOK: true,
		},
		{
			// the connection has been deleted: cannot be parsed
			name: "mcp__gone__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantOK:    false,
		},
		{
			// an empty tool name is not a valid call
			name: "mcp__fs__", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantOK:    false,
		},
		{
			// wrong prefix
			name: "rpc__fs__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantOK:    false,
		},
	}

	for _, tc := range cases {
		conn, tool, ok := splitExternalName(tc.name, tc.prefix, tc.connNames)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if conn != tc.wantConn || tool != tc.wantTool {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, conn, tool, tc.wantConn, tc.wantTool)
		}
	}
}

// Has accepts mcp__ / rpc__ only: local__ belongs to localEndpointRunner and must not be taken over by mistake.
func TestExternalRunnerHas(t *testing.T) {
	r := &externalToolRunner{}
	for _, name := range []string{"mcp__fs__read_file", "rpc__erp__create_order"} {
		if !r.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"local__daily-report", "read_file", "custom__x", "web_search"} {
		if r.Has(name) {
			t.Errorf("Has(%q) = true, want false", name)
		}
	}
}

// is_error=true means "a remote business error", not a call failure: the result is still returned so the model can see the error body.
func TestExternalResultKeepsBusinessError(t *testing.T) {
	got := externalResult("余额不足", nil, true, 0)
	if got["is_error"] != true {
		t.Errorf("is_error = %v, want true", got["is_error"])
	}
	if got["content"] != "余额不足" {
		t.Errorf("content = %v", got["content"])
	}
	if _, ok := got["truncated"]; ok {
		t.Error("truncated must not be present when under the limit")
	}
}

// Over the limit it must be truncated and flagged: an external tool dumping a whole file must not eat the context in one go.
func TestExternalResultTruncates(t *testing.T) {
	got := externalResult(strings.Repeat("a", 100), nil, false, 10)
	if got["content"] != strings.Repeat("a", 10) {
		t.Errorf("content length = %d, want 10", len(got["content"].(string)))
	}
	if got["truncated"] != true {
		t.Error("truncated=true must be present when over the limit")
	}
}

// Truncation must not cut in the middle of a UTF-8 character - half a Chinese character is an invalid character in JSON.
func TestClampTextStopsAtRuneBoundary(t *testing.T) {
	// The two-Chinese-character fixture is 6 bytes; limit=4 lands in the middle of the second character
	got, truncated := clampText("汉字", 4)
	if !truncated {
		t.Fatal("should be judged truncated")
	}
	if got != "汉" {
		t.Fatalf("got %q, want %q", got, "汉")
	}
}
