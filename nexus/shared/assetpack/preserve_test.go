package assetpack

import "testing"

// An overwriting install preserves the target's credentials: only "empty in the package and the key
// looks like a credential" counts.
func TestPreserveSecretKeys(t *testing.T) {
	incoming := map[string]any{
		"api_key":     "",      // cleared at listing time -> the target value should come back
		"model":       "gpt-4", // the package has a value -> the package wins
		"description": "",      // the key does not look like a credential -> untouched (may legitimately be empty)
		"new_token":   "",      // the target has none -> stays empty
		"provider": map[string]any{
			"token": "",          // nested levels are preserved too
			"url":   "https://a", // the in-package value wins
		},
	}
	target := map[string]any{
		"api_key":     "sk-live-1",
		"model":       "old-model",
		"description": "old description",
		"provider": map[string]any{
			"token": "tgt-token",
			"url":   "https://old",
		},
	}

	kept := PreserveSecretKeys(incoming, target)
	if kept != 2 {
		t.Fatalf("kept count = %d, want 2 (api_key and provider.token)", kept)
	}

	if got := incoming["api_key"].(string); got != "sk-live-1" {
		t.Fatalf("api_key did not get the target value: %q", got)
	}
	if got := incoming["model"].(string); got != "gpt-4" {
		t.Fatalf("a non-empty field from the package was overwritten by the target: %q", got)
	}
	if got := incoming["description"].(string); got != "" {
		t.Fatalf("an empty value whose key is not credential-like must not be filled: %q", got)
	}
	if got := incoming["new_token"].(string); got != "" {
		t.Fatalf("a key the target lacks must not be invented: %q", got)
	}
	provider := incoming["provider"].(map[string]any)
	if got := provider["token"].(string); got != "tgt-token" {
		t.Fatalf("the nested credential did not get the target value: %q", got)
	}
	if got := provider["url"].(string); got != "https://a" {
		t.Fatalf("a nested non-credential field was modified: %q", got)
	}
}

// The package carries a non-empty credential: the user explicitly changed it, so the target's old
// value must not win.
func TestPreserveSecretKeysPrefersIncomingValue(t *testing.T) {
	incoming := map[string]any{"token": "new"}
	target := map[string]any{"token": "old"}
	if kept := PreserveSecretKeys(incoming, target); kept != 0 {
		t.Fatalf("nothing should be preserved, kept=%d", kept)
	}
	if got := incoming["token"].(string); got != "new" {
		t.Fatalf("the new in-package value was overwritten: %q", got)
	}
}

// The target side is empty or of the wrong type: nothing is filled, the empty in-package value stays.
func TestPreserveSecretKeysNoTargetValue(t *testing.T) {
	incoming := map[string]any{"token": "", "password": ""}
	target := map[string]any{"token": "", "password": 12345}
	if kept := PreserveSecretKeys(incoming, target); kept != 0 {
		t.Fatalf("nothing should be preserved, kept=%d", kept)
	}
}

// An empty map needs no traversal (config is often empty).
func TestPreserveSecretKeysEmpty(t *testing.T) {
	if kept := PreserveSecretKeys(map[string]any{}, map[string]any{"token": "x"}); kept != 0 {
		t.Fatalf("an empty incoming should return 0, got %d", kept)
	}
	if kept := PreserveSecretKeys(map[string]any{"token": ""}, nil); kept != 0 {
		t.Fatalf("an empty target should return 0, got %d", kept)
	}
}
