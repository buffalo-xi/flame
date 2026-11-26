package flowconfig

import "testing"

// Configuring -1 ("unlimited") must fall back: a pure flow cycle relies on this one gate, and allowing it to be
// switched off hands the defence to a single misconfiguration.
func TestDepthLimit(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		want       int
	}{
		{"a normal value is used as is", 5, 5},
		{"1 is a legitimate limit", 1, 1},
		{"-1 falls back", -1, fallback.MaxRecursionDepth},
		{"0 falls back", 0, fallback.MaxRecursionDepth},
	}
	for _, tc := range cases {
		l := FlowLimits{MaxRecursionDepth: tc.configured}
		if got := l.DepthLimit(); got != tc.want {
			t.Errorf("%s: limit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Even when the built-in default is misconfigured, the depth gate keeps a positive number.
func TestDepthLimitNeverReturnsUnlimited(t *testing.T) {
	saved := fallback.MaxRecursionDepth
	fallback.MaxRecursionDepth = -1
	defer func() { fallback.MaxRecursionDepth = saved }()

	l := FlowLimits{MaxRecursionDepth: -1}
	if got := l.DepthLimit(); got != defaultMaxRecursionDepth {
		t.Fatalf("limit = %d, want the guard value %d", got, defaultMaxRecursionDepth)
	}
}

// Retention is always a positive count: downstream, 0 means "store nothing" and a non-positive session count means
// "no memory", so -1 ("unlimited" in the configuration) must never reach them.
func TestRetentionLimits(t *testing.T) {
	cases := []struct {
		name     string
		in       int
		wantRuns int
		wantSess int
	}{
		{"a normal value is used as is", 7, 7, 7},
		{"-1 falls back to the default", -1, fallback.MaxRunRecords, fallback.MaxSessionRecords},
		{"0 falls back to the default", 0, fallback.MaxRunRecords, fallback.MaxSessionRecords},
	}
	for _, tc := range cases {
		l := FlowLimits{MaxRunRecords: tc.in, MaxSessionRecords: tc.in}
		if got := l.RunRecordLimit(); got != tc.wantRuns {
			t.Errorf("%s: run records = %d, want %d", tc.name, got, tc.wantRuns)
		}
		if got := l.SessionRecordLimit(); got != tc.wantSess {
			t.Errorf("%s: session records = %d, want %d", tc.name, got, tc.wantSess)
		}
	}
}

// A misconfigured default still yields 1, never 0 or -1: those would read as "off".
func TestRetentionLimitsNeverReturnNonPositive(t *testing.T) {
	savedRuns, savedSess := fallback.MaxRunRecords, fallback.MaxSessionRecords
	fallback.MaxRunRecords, fallback.MaxSessionRecords = -1, 0
	defer func() { fallback.MaxRunRecords, fallback.MaxSessionRecords = savedRuns, savedSess }()

	l := FlowLimits{MaxRunRecords: -1, MaxSessionRecords: -1}
	if got := l.RunRecordLimit(); got != 1 {
		t.Fatalf("run records = %d, want 1", got)
	}
	if got := l.SessionRecordLimit(); got != 1 {
		t.Fatalf("session records = %d, want 1", got)
	}
}
