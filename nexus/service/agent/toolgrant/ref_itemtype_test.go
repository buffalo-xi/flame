package toolgrant

import (
	"testing"

	"flame/nexus/shared/models"
)

// item_type can only be derived from the ref: write entry points do not accept it from the caller.
// Deriving it wrongly produces a "contradictory row" - item_type and ref pointing at different shapes, so the frontend's checkbox
// state no longer matches the grant row, and nothing errors (see the notes on ItemTypeOfRef).
func TestItemTypeOfRef(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		// Group shape
		{"group:fs-basic", models.GrantItemGroup},
		{"preset:default", models.GrantItemGroup},
		{"builtin:file", models.GrantItemGroup}, // a category
		{"mcp:fs", models.GrantItemGroup},       // a whole connection
		{"rpc:erp", models.GrantItemGroup},
		{"local:rpc", models.GrantItemGroup},

		// Single-tool shape
		{"builtin:read_file", models.GrantItemTool},
		{"mcp:fs:read_file", models.GrantItemTool},
		{"rpc:erp:query", models.GrantItemTool},
		{"local:rpc:flow-a", models.GrantItemTool},
		{"custom:web_search", models.GrantItemTool}, // one ref per tool
		{"plugin:web_search", models.GrantItemTool},

		// An illegal ref: conservatively tool (the write path already stops it with ValidateRef; here it only has to not panic)
		{"", models.GrantItemTool},
	}
	for _, c := range cases {
		if got := ItemTypeOfRef(c.ref); got != c.want {
			t.Errorf("ItemTypeOfRef(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}
