package tools

import (
	"path/filepath"
	"testing"
)

// Draft mode must stay exactly as before the change: editing, debugging and test chats all rely on it, and any path drift shows up
// as "AGENT.md cannot be read / the skill script cannot be found".
func TestDraftModePathsUnchanged(t *testing.T) {
	c := &Context{
		Schema:    "t0",
		AgentName: "poet",
		RootDir:   "/root",
		AgentRoot: "/root/tenants/t0/agents/poet",
		SkillRoot: "/root/tenants/t0/skills",
		ToolsRoot: "/root/tenants/t0/tools",
	}
	if c.VersionBound() {
		t.Fatal("a zero-value DefinitionMode must not bind a version (the default has to be the draft)")
	}
	if got, want := c.AgentPath("AGENT.md"), "tenants/t0/agents/poet/AGENT.md"; got != want {
		t.Fatalf("AgentPath = %q, want %q", got, want)
	}

	key, host, err := c.SkillReadDir("", "verse")
	if err != nil {
		t.Fatalf("failed to resolve the tenant-level skill directory: %v", err)
	}
	if want := "tenants/t0/skills/verse"; key != want {
		t.Fatalf("tenant skill key = %q, want %q", key, want)
	}
	if want := filepath.Join("/root", "tenants/t0/skills/verse"); host != want {
		t.Fatalf("tenant skill host = %q, want %q", host, want)
	}

	key, _, err = c.SkillReadDir("poet", "verse")
	if err != nil {
		t.Fatalf("failed to resolve the private skill directory: %v", err)
	}
	if want := "tenants/t0/agents/poet/skills/verse"; key != want {
		t.Fatalf("private skill key = %q, want %q", key, want)
	}

	dir, err := c.ToolDir("calc")
	if err != nil {
		t.Fatalf("failed to resolve the tool directory: %v", err)
	}
	if want := filepath.Join("/root/tenants/t0/tools", "calc"); dir != want {
		t.Fatalf("tool dir = %q, want %q", dir, want)
	}
}

// A production run reads the version directory: AGENT.md lives in versions/agents/<name>/v<n>/, and private skills live under the
// same version snapshot's skills/ (private skills have no version number of their own).
func TestCurrentModePathsUseVersionDir(t *testing.T) {
	c := &Context{
		Schema:         "t0",
		AgentName:      "poet",
		DefinitionMode: ModeCurrent,
		AgentVersion:   3,
		RootDir:        "/root",
	}
	if !c.VersionBound() {
		t.Fatal("current with a version number should count as bound")
	}
	if got, want := c.AgentPath("AGENT.md"), "tenants/t0/versions/agents/poet/v3/AGENT.md"; got != want {
		t.Fatalf("AgentPath = %q, want %q", got, want)
	}

	// Its own private skill: reads this agent's version snapshot, no database lookup needed
	key, host, err := c.SkillReadDir("poet", "verse")
	if err != nil {
		t.Fatalf("failed to resolve the private skill version directory: %v", err)
	}
	if want := "tenants/t0/versions/agents/poet/v3/skills/verse"; key != want {
		t.Fatalf("private skill version key = %q, want %q", key, want)
	}
	if want := filepath.Join("/root", "tenants/t0/versions/agents/poet/v3/skills/verse"); host != want {
		t.Fatalf("private skill version host = %q, want %q", host, want)
	}
}

// Without a storage root (object storage / unit tests) a production run must fail explicitly -
// mounting a sandbox on an empty path only yields the much harder to diagnose "directory does not exist".
func TestCurrentModeWithoutRootDirFails(t *testing.T) {
	c := &Context{Schema: "t0", AgentName: "poet", DefinitionMode: ModeCurrent, AgentVersion: 1}
	if _, _, err := c.SkillReadDir("poet", "verse"); err == nil {
		t.Fatal("a missing RootDir should be an error, not an empty host path")
	}
}

// Child call mode: the only formula is "child draft = own draft && FollowDrafts".
// This truth table is the guard rail for "production entry points never spread drafts" - changing any cell requires thinking it
// through here first.
func TestChildMode(t *testing.T) {
	cases := []struct {
		name   string
		self   DefinitionMode
		follow bool
		want   DefinitionMode
	}{
		{"chat/debug + switch on", ModeDraft, true, ModeDraft},
		{"chat/debug + switch off", ModeDraft, false, ModeCurrent},
		{"production entry + switch on (no effect)", ModeCurrent, true, ModeCurrent},
		{"production entry + switch off", ModeCurrent, false, ModeCurrent},
	}
	for _, tc := range cases {
		c := &Context{DefinitionMode: tc.self, FollowDrafts: tc.follow}
		if got := c.ChildMode(); got != tc.want {
			t.Errorf("%s: ChildMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}
