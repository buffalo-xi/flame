package agent

import (
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
	"flame/nexus/shared/storage"
)

// Which definition each side reads: an ordinary agent follows the mode handed in by the entry point, the supervisor always reads the
// draft. The supervisor rule is the foundation of its "management ability" - everything it builds lives in the draft, so reading the
// released version would hide its own output.
func TestResolveDefinitionMode(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		supervisor bool
		want       tools.DefinitionMode
	}{
		{"ordinary + release entry", string(tools.ModeCurrent), false, tools.ModeCurrent},
		{"ordinary + omitted (draft)", "", false, tools.ModeDraft},
		{"supervisor + release entry still draft", string(tools.ModeCurrent), true, tools.ModeDraft},
		{"supervisor + omitted", "", true, tools.ModeDraft},
	}
	for _, tc := range cases {
		if got := resolveDefinitionMode(tc.raw, tc.supervisor); got != tc.want {
			t.Errorf("%s: resolveDefinitionMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A production run is bound to a version: failing to read AGENT.md there must be an error.
// Silently falling back to the default prompt equals "the live agent quietly changed identity", and the sandbox /agent is usually an
// empty directory at that point too (docker creates a missing host path as an empty directory), so the two failures mask each other.
func TestLoadSystemPromptFailsWithoutVersionEntry(t *testing.T) {
	root := t.TempDir()
	a := &agentImpl{
		instance: &models.Agent{Name: "poet"},
		toolCtx: &tools.Context{
			Schema:         "t0",
			AgentName:      "poet",
			RootDir:        root,
			DefinitionMode: tools.ModeCurrent,
			AgentVersion:   3,
			Storage:        storage.NewFilesystemStorage(root),
		},
	}
	if _, err := a.loadSystemPrompt(); err == nil {
		t.Fatal("a version directory without AGENT.md must be an error, not a silent fallback to the default prompt")
	}
}

// Which definition sub-calls read: three rules apply in order (supervisor always on -> own production always off -> the switch, default on).
func TestResolveFollowDrafts(t *testing.T) {
	cases := []struct {
		name       string
		self       tools.DefinitionMode
		raw        any
		supervisor bool
		want       bool
	}{
		{"ordinary + release + omitted", tools.ModeCurrent, nil, false, false},
		{"ordinary + release + true does not spread", tools.ModeCurrent, true, false, false},
		{"ordinary + draft + omitted (on by default)", tools.ModeDraft, nil, false, true},
		{"ordinary + draft + off", tools.ModeDraft, false, false, false},
		{"ordinary + draft + explicitly on", tools.ModeDraft, true, false, true},
		{"supervisor + explicit off still forced on", tools.ModeDraft, false, true, true},
		{"supervisor + release entry also on", tools.ModeCurrent, nil, true, true},
	}
	for _, tc := range cases {
		if got := resolveFollowDrafts(tc.self, tc.raw, tc.supervisor); got != tc.want {
			t.Errorf("%s: resolveFollowDrafts = %v, want %v", tc.name, got, tc.want)
		}
	}
}
