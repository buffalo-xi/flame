package daemon

import (
	"testing"
	"time"
)

// The invariant of codeDeadline: a caller-supplied timeout can only **tighten** the interaction cap, never relax it.
//
// That invariant is a bug we already hit: the nexus side configured script.<lang>.timeout to 600, believing scripts
// could get 10 minutes, while it was in fact shaved down to 120s here — and at the time this logic had neither a test
// nor a log line, so "configured but ineffective" could only be found by reading the code.
func TestScriptDeadline(t *testing.T) {
	const maxSec = 120

	cases := []struct {
		name string
		req  time.Duration
		want time.Duration
	}{
		// No timeout supplied (zero or negative): the hard gate is the backstop.
		{"not supplied → hard gate", 0, maxSec * time.Second},
		{"negative (treated as unset) → hard gate", -3 * time.Second, maxSec * time.Second},
		// A smaller value supplied: tightened, the only direction that can change the actual cap.
		{"5s supplied → 5s", 5 * time.Second, 5 * time.Second},
		{"119s supplied → 119s", 119 * time.Second, 119 * time.Second},
		// A larger value supplied: **not** relaxed (600 really appeared in historical configs).
		{"600s supplied → still the hard gate", 600 * time.Second, maxSec * time.Second},
		// Equal: no ambiguity introduced.
		{"120s supplied → 120s", 120 * time.Second, maxSec * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeDeadline(tc.req, maxSec); got != tc.want {
				t.Errorf("codeDeadline(%v, %d) = %v, want %v", tc.req, maxSec, got, tc.want)
			}
		})
	}
}

// The hard gate itself follows the config: changing code_max_sec must change the actual cap immediately, otherwise
// "raise the hard gate" is a no-op.
func TestScriptDeadlineFollowsConfig(t *testing.T) {
	// Same supplied value, different hard gate → different result.
	if got := codeDeadline(300*time.Second, 600); got != 300*time.Second {
		t.Errorf("with a 600s hard gate, 300s supplied should yield 300s, got %v", got)
	}
	if got := codeDeadline(300*time.Second, 120); got != 120*time.Second {
		t.Errorf("with a 120s hard gate, 300s supplied should yield 120s, got %v", got)
	}
}
