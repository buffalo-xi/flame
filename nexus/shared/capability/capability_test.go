package capability

import "testing"

// The old key must be normalised to the new one: sandbox.script was renamed to sandbox.code in 2026-10, and old rows may still be in the
// database. A broken normalisation means a **silent blackout** - the capability gate denies by default, so a workflow code node reports
// "not enabled on the platform" while the administrator has it switched on.
func TestNormalize(t *testing.T) {
	if got := Normalize(LegacyScript); got != Code {
		t.Errorf("Normalize(%q) = %q, want %q", LegacyScript, got, Code)
	}
	for _, key := range All() {
		if got := Normalize(key); got != key {
			t.Errorf("Normalize(%q) should not change a new key, got %q", key, got)
		}
	}
	if got := Normalize("sandbox.unknown"); got != "sandbox.unknown" {
		t.Errorf("an unknown key should be returned as is, got %q", got)
	}
}

// All() lists new keys only: if an old key slipped in, the admin side would show an extra card with the same name and a different key.
func TestAllExcludesLegacy(t *testing.T) {
	for _, key := range All() {
		if key == LegacyScript {
			t.Fatalf("All() must not contain the old key %q", LegacyScript)
		}
	}
	var hasCode bool
	for _, key := range All() {
		if key == Code {
			hasCode = true
		}
	}
	if !hasCode {
		t.Errorf("All() is missing %q (the capability card will not list this row)", Code)
	}
}

// During the transition Valid accepts both keys: the frontend takes new keys from All(), but external scripts may still send the old one,
// and rejecting it outright would be a pointless 400. Writes have Normalize as a backstop, so no old key is stored.
func TestValidAcceptBothKeys(t *testing.T) {
	if !Valid(Code) {
		t.Errorf("Valid(%q) = false", Code)
	}
	if !Valid(LegacyScript) {
		t.Errorf("Valid(%q) = false (it should be accepted during the transition)", LegacyScript)
	}
	if Valid("sandbox.nope") {
		t.Error("an unknown key should be refused")
	}
}

// Label / ShortLabel both normalise first: otherwise an old key renders as a bare key name, and an administrator seeing "sandbox.script" has
// no way to map it to the "code snippet execution" row in the UI.
func TestLabelNormalizesLegacy(t *testing.T) {
	if Label(LegacyScript) != Label(Code) {
		t.Errorf("Label(old key) = %q, it should match the new key", Label(LegacyScript))
	}
	if ShortLabel(LegacyScript) != ShortLabel(Code) {
		t.Errorf("ShortLabel(old key) = %q, it should match the new key", ShortLabel(LegacyScript))
	}
	if Label(Code) == Code {
		t.Errorf("Label(%q) has no copy (the UI would show a bare key name)", Code)
	}
	if ShortLabel(Code) == Code {
		t.Errorf("ShortLabel(%q) has no copy", Code)
	}
}
