package agent

import "testing"

// The entry layer uses these two functions to mark "read the draft or the current version for this call".
// Once the key drifts the production entry quietly falls back to the draft - so the key name is pinned down too.
func TestDefinitionModeArguments(t *testing.T) {
	args := map[string]interface{}{"args": map[string]any{}}
	SetReleaseMode(args)
	if got := args[DefinitionModeKey]; got != "current" {
		t.Fatalf("SetReleaseMode wrote %v, want %q (key %q)", got, "current", DefinitionModeKey)
	}
	SetDraftMode(args)
	if got := args[DefinitionModeKey]; got != "draft" {
		t.Fatalf("SetDraftMode wrote %v, want %q", got, "draft")
	}
	// A nil map must not panic: entry-layer arguments may be nil (a purely internal call, say)
	SetReleaseMode(nil)
	SetDraftMode(nil)
}
