package agents

import (
	"strings"
	"testing"
)

// Rename legality is pure string rules, so it can be tested directly without gin or storage.
func TestCheckRename(t *testing.T) {
	cases := []struct {
		old, new, want string
	}{
		{"", "a.md", "path is required"},
		{"a.md", "", "new_path is required"},
		{"a.md", "a.md", "the new name is the same as the old one"},
		{"a.md", "b.md", ""},
		{"a.md", "docs/b.md", ""}, // moving to another directory is supported (the backend allows it; the UI does not expose it yet)
		{"a.md", ".hidden", "a name cannot start with . (the list hides it)"},
		{"a.md", "dir/.hidden", "a name cannot start with . (the list hides it)"},
		// Self-nesting: dir -> dir/sub copies successfully, then deleting the old prefix takes the new location with it.
		{"dir", "dir/sub", "cannot move a directory into its own subtree"},
		{"dir", "dir/sub/x.md", "cannot move a directory into its own subtree"},
		// Same name prefix but not a subtree - must not be rejected by mistake.
		{"dir", "dir2", ""},
		{"dir", "dirx/y", ""},
		{"uploads/a.txt", "uploads/b.txt", ""},
	}
	for _, c := range cases {
		if got := checkRename(c.old, c.new); got != c.want {
			t.Errorf("checkRename(%q, %q) = %q, want %q", c.old, c.new, got, c.want)
		}
	}
}

// The entry path rules are shared by create and rename, so a change here affects both APIs.
func TestCheckEntryPath(t *testing.T) {
	long := strings.Repeat("文", 121)
	cases := []struct {
		clean, want string
	}{
		{"", "path is required"},
		{"a.md", ""},
		{"a/b.md", ""},
		{".keep", "a name cannot start with . (the list hides it)"},
		{"dir/.keep", "a name cannot start with . (the list hides it)"},
		{"a." + "keep", ""}, // only a leading . counts as hidden
		{long, "the name is too long (limit 120 characters)"},
		{strings.Repeat("文", 120), ""},
		{strings.Repeat("a/", 15) + "x", ""},                                                     // 16 segments: within the limit
		{strings.Repeat("a/", 16) + "x", "the directory is nested too deeply (limit 16 levels)"}, // 17 segments: one over
	}
	for _, c := range cases {
		if got := checkEntryPath(c.clean); got != c.want {
			t.Errorf("checkEntryPath(%q) = %q, want %q", c.clean, got, c.want)
		}
	}
}
