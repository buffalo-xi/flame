package core

import (
	"encoding/json"
	"testing"
)

// TestValidProgressToken applies the spec's constraint: progressToken must be a string or an integer.
// Any other shape (1.5 included - a float that is not an integer) counts as not provided: treat it as unsubscribed rather than pushing
// with an illegal token.
func TestValidProgressToken(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"string", "tok-1", true},
		{"integer float64 (the norm after JSON decoding)", float64(7), true},
		{"int", 7, true},
		{"int64", int64(7), true},
		{"json.Number", json.Number("7"), true},
		{"empty string", "", false},
		{"non-integer", 1.5, false},
		{"bool", true, false},
		{"nil", nil, false},
		{"object", map[string]any{"a": 1}, false},
		{"array", []any{1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidProgressToken(c.in); got != c.want {
				t.Fatalf("ValidProgressToken(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestProgressPumpEnabled: no token means "not subscribed", and the caller takes the synchronous path on that basis.
func TestProgressPumpEnabled(t *testing.T) {
	if NewProgressPump(nil).Enabled() {
		t.Fatal("without a token it must not be Enabled")
	}
	var nilPump *ProgressPump
	if nilPump.Enabled() {
		t.Fatal("a nil pump must not be Enabled (and must not panic)")
	}
	if !NewProgressPump("tok").Enabled() {
		t.Fatal("with a token it should be Enabled")
	}
}

// TestStripLegacyStream: the deprecated arguments.stream has to be stripped - it no longer affects the decision (which only looks at
// progressToken) and must not flow into a flow / agent as a business variable.
// The return value drives "should this be logged", so the two cases have to be distinguishable.
func TestStripLegacyStream(t *testing.T) {
	input := map[string]any{"stream": true, "city": "sh"}
	if !StripLegacyStream(input) {
		t.Fatal("it should return true when stream is present")
	}
	if _, existed := input["stream"]; existed {
		t.Fatal("stream should have been stripped")
	}
	if input["city"] != "sh" {
		t.Fatal("other business arguments must not be touched")
	}

	// Without the key: a no-op, and nothing may be invented
	clean := map[string]any{"city": "sh"}
	if StripLegacyStream(clean) {
		t.Fatal("it should return false when stream is absent")
	}
	if len(clean) != 1 {
		t.Fatalf("the map must not change without stream: %v", clean)
	}
}

// TestProgressPumpNext: the sequence strictly increments from 1, because the spec requires progress to increase on every frame.
func TestProgressPumpNext(t *testing.T) {
	p := NewProgressPump("tok")
	for want := 1.0; want <= 3; want++ {
		if got := p.Next(); got != want {
			t.Fatalf("Next() = %v, want %v", got, want)
		}
	}
}
