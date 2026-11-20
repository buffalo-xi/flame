package callchain

import (
	"context"
	"strings"
	"testing"
)

// Two fixed entities: F (a flow) calls A (an agent).
// An element looks like <id>|<kind>; the note segment is for readability only and takes no part in duplicate detection.
var (
	idF    = "f1111111-1111-1111-1111-111111111111"
	idA    = "a2222222-2222-2222-2222-222222222222"
	flowF  = Key(KindFlow, idF)
	agentA = Key(KindAgent, idA)
)

// TestSiblingRepeatIsNotLoop: F calling A twice in a row (f a a) is not a loop — sibling calls each derive from the
// "parent chain", which never contains A itself.
func TestSiblingRepeatIsNotLoop(t *testing.T) {
	parent := []string{flowF}
	for i := 0; i < 2; i++ {
		if Contains(parent, agentA) {
			t.Fatalf("sibling call #%d was wrongly detected as a loop", i+1)
		}
		// The derived child chain carries A, but must not modify the parent (otherwise the second call would hit)
		child := Append(parent, agentA)
		if len(child) != 2 || ID(child[1]) != idA {
			t.Fatalf("child chain = %v", child)
		}
		if len(parent) != 1 {
			t.Fatalf("the parent chain was modified: %v", parent)
		}
	}
}

// TestReorderIsLoop: f → a → f must be refused — F is already an ancestor of A.
func TestReorderIsLoop(t *testing.T) {
	chainAtA := Append([]string{flowF}, agentA)
	if !Contains(chainAtA, flowF) {
		t.Fatal("f→a→f should hit, but did not")
	}
}

// TestSelfCallIsLoop: a self-call (a → a) hits as well — calling A from inside A, with A already in the chain.
func TestSelfCallIsLoop(t *testing.T) {
	if !Contains([]string{agentA}, agentA) {
		t.Fatal("a→a should hit")
	}
}

// TestContainsIgnoresNote: duplicate detection compares ids only, so a missing note, a different note or different casing
// all count as the same entity.
func TestContainsIgnoresNote(t *testing.T) {
	chain := []string{idA + "|t0/agent"}
	cases := []struct {
		key  string
		want bool
		why  string
	}{
		{idA, true, "the note may be omitted"},
		{idA + "|flow", true, "a different note does not change identity"},
		{strings.ToUpper(idA), true, "case insensitive"},
		{idA + "|whatever", true, "the note's content is not validated semantically"},
		{"a2222222-2222-2222-2222-222222222223", false, "a different id must not hit"},
		{"", false, "an empty key is always false"},
	}
	for _, c := range cases {
		if got := Contains(chain, c.key); got != c.want {
			t.Errorf("Contains(%q) = %v, want %v (%s)", c.key, got, c.want, c.why)
		}
	}
}

// TestKeyAndID: Key builds <id>|<kind>; ID takes the part before the pipe.
func TestKeyAndID(t *testing.T) {
	if flowF != idF+"|flow" {
		t.Fatalf("Key = %q", flowF)
	}
	if ID(flowF) != idF {
		t.Fatalf("ID = %q", ID(flowF))
	}
	if ID(idA) != idA {
		t.Fatal("ID of a bare id should be the id itself")
	}
	// A missing kind degrades to a bare id: still detectable, just with one segment less context
	if Key("", "x") != "x" {
		t.Errorf("a missing kind should degrade to a bare id, got %q", Key("", "x"))
	}
	if Key(KindAgent, "") != "" {
		t.Error("a missing id should return an empty string (no identity to speak of)")
	}
	// Lower-cased uniformly: the database holds lower case, while a URL / the frontend may send upper case
	if Key("Agent", "ABC") != "abc|agent" {
		t.Errorf("Key did not normalise case: %q", Key("Agent", "ABC"))
	}
}

// TestValid element shape: the id segment allows identifiers only, the note segment allows "/", and an empty segment is
// always invalid.
func TestValid(t *testing.T) {
	valid := []string{
		flowF,
		agentA,
		"abc",
		"abc|agent",
		// The note segment is only character-whitelisted, never structurally validated: if the peer carries a schema or an
		// instance suffix, an older version must accept it too (dropping the whole chain equals a missed detection).
		"abc|t0/agent",
		"abc|agent@nexus-2",
	}
	for _, k := range valid {
		if !Valid(k) {
			t.Errorf("%q should be valid", k)
		}
	}
	invalid := []string{
		"",
		"|t0/agent",                     // empty id
		"abc|",                          // empty note
		"bad key",                       // space
		"bad:key",                       // colon
		"abc|bad key",                   // space inside the note
		strings.Repeat("a", maxIDLen+1), // id too long
	}
	for _, k := range invalid {
		if Valid(k) {
			t.Errorf("%q should be invalid", k)
		}
	}
}

// TestNormalizeDropsGarbage drops a dirty element rather than failing the whole call: the chain is cooperative
// information, and one bad element must not turn a normal request into a 400.
func TestNormalizeDropsGarbage(t *testing.T) {
	got := Normalize([]any{flowF, 42, "bad key", agentA})
	if len(got) != 2 || ID(got[0]) != idF || ID(got[1]) != idA {
		t.Fatalf("Normalize = %v", got)
	}
}

// TestNormalizeLowercases: the peer / frontend may send upper case; it is lower-cased before duplicate detection, or the
// same entity would be missed just because it was spelled differently.
func TestNormalizeLowercases(t *testing.T) {
	got := Normalize([]any{"ABC-1|Agent"})
	if len(got) != 1 || got[0] != "abc-1|agent" {
		t.Fatalf("Normalize = %v", got)
	}
}

// TestParseParamsTooLong: an over-long chain must be refused (tooLong=true), never truncated — the dropped segments may
// be exactly where the re-entry being looked for is hiding.
func TestParseParamsTooLong(t *testing.T) {
	raw := make([]any, 0, MaxLen+1)
	for i := 0; i <= MaxLen; i++ {
		raw = append(raw, agentA)
	}
	params := map[string]any{"_meta": map[string]any{MetaKey: raw}}
	if _, tooLong := ParseParams(params); !tooLong {
		t.Fatal("a chain over the limit should report tooLong")
	}
	if _, tooLong := ParseParams(map[string]any{"_meta": map[string]any{MetaKey: []any{flowF}}}); tooLong {
		t.Fatal("a normal-length chain should not report tooLong")
	}
	if _, tooLong := ParseParams(nil); tooLong {
		t.Fatal("missing params should not report tooLong")
	}
}

// TestContextRoundTrip: an outbound call carries the chain through ctx (no extra function parameter) and must get it back.
func TestContextRoundTrip(t *testing.T) {
	ctx := With(context.Background(), []string{flowF})
	if got := From(ctx); len(got) != 1 || got[0] != flowF {
		t.Fatalf("From = %v", got)
	}
	if got := From(context.Background()); got != nil {
		t.Fatalf("an empty ctx should return nil, got %v", got)
	}
	// An empty chain is not written to ctx: that would give an ordinary call an extra value layer for nothing
	if got := From(With(context.Background(), nil)); got != nil {
		t.Fatalf("an empty chain should not be written to ctx, got %v", got)
	}
}
