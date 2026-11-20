package agentconfig

import (
	"database/sql"
	"testing"
)

func null(v int) sql.NullInt32 { return sql.NullInt32{Int32: int32(v), Valid: true} }

// resolveField is the single implementation of every "tenant / system / fallback" rule, pinned case by case:
// a table that was never created, a value configured above its parent, a -1 or a 0 written directly — all inputs that
// really happen must converge on a positive number.
func TestResolveField(t *testing.T) {
	const fb = 80
	tests := []struct {
		name   string
		tenant sql.NullInt32
		sys    sql.NullInt32
		want   int
	}{
		{"neither configured -> fallback", sql.NullInt32{}, sql.NullInt32{}, fb},
		{"system row only -> the system value", sql.NullInt32{}, null(100), 100},
		{"tenant below system -> the tenant value", null(40), null(100), 40},
		{"tenant above system -> clamped to the system value (the system row is the ceiling)", null(500), null(100), 100},
		{"tenant row only -> the tenant value (the fallback is the ceiling when the system row is unset)", null(40), sql.NullInt32{}, 40},
		{"tenant -1 -> fallback (this table does not accept \"unlimited\")", null(-1), sql.NullInt32{}, fb},
		{"tenant 0 -> fallback (an odd value from a direct database edit)", null(0), sql.NullInt32{}, fb},
		{"system -1 -> treated as unset, the tenant value still applies", null(40), null(-1), 40},
		{"system 0 -> treated as unset, the tenant value still applies", null(40), null(0), 40},
		{"system -1 and the tenant unset -> fallback", sql.NullInt32{}, null(-1), fb},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveField(tc.tenant, tc.sys, fb); got != tc.want {
				t.Errorf("resolveField = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEffective pins the two-level merge on whole rows: Resolve is Effective behind the cache, and the HTTP layers show
// Effective as "what actually applies", so the two must never disagree about clamping.
func TestEffective(t *testing.T) {
	tenant := &Row{MaxIterations: null(500), MaxCallDepth: null(2)}
	sys := &Row{MaxIterations: null(100)}

	got := Effective(tenant, sys)
	if got.MaxIterations != 100 {
		t.Errorf("MaxIterations = %d, want 100 (clamped by the system row)", got.MaxIterations)
	}
	if got.MaxCallDepth != 2 {
		t.Errorf("MaxCallDepth = %d, want 2 (the tenant row)", got.MaxCallDepth)
	}
	if got.MaxTools != 50 || got.MaxOutputBytes != 64<<10 {
		t.Errorf("unset fields = %d / %d, want the fallbacks 50 / 65536", got.MaxTools, got.MaxOutputBytes)
	}

	// No rows at all: everything is the code fallback, exactly as before the table existed.
	if got := Effective(nil, nil); got != Fallback() {
		t.Errorf("Effective(nil, nil) = %+v, want the fallback %+v", got, Fallback())
	}
}

// TestValidateTenant: the write side must reject < 1 always, and above the system level only when that level is set.
func TestValidateTenant(t *testing.T) {
	sys := &Row{MaxIterations: null(100), MaxCallDepth: null(5)}

	cases := []struct {
		name      string
		tenant    *Row
		wantCode  string
		wantField string
	}{
		{"all unset -> accepted", &Row{}, "", ""},
		{"nil row -> accepted", nil, "", ""},
		{"-1 -> rejected (this config has no unlimited)", &Row{MaxTools: null(-1)}, CodeInvalidValue, "max_tools"},
		{"0 -> rejected", &Row{MaxOutputBytes: null(0)}, CodeInvalidValue, "max_output_bytes"},
		{"above the system level -> rejected", &Row{MaxIterations: null(101)}, CodeExceedsSystemLimit, "max_iterations"},
		{"equal to the system level -> accepted", &Row{MaxIterations: null(100)}, "", ""},
		{"system unset -> the tenant value is accepted", &Row{MaxTools: null(9999)}, "", ""},
		{"system -1 (direct database edit) -> not an upper bound", &Row{MaxIterations: null(500)}, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The last case needs an unusable system value, so it swaps the row in.
			s := sys
			if tc.name == "system -1 (direct database edit) -> not an upper bound" {
				s = &Row{MaxIterations: null(-1)}
			}
			err := ValidateTenant(tc.tenant, s)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("ValidateTenant = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateTenant = nil, want %s", tc.wantCode)
			}
			if err.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", err.Code, tc.wantCode)
			}
			if err.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", err.Field, tc.wantField)
			}
			if err.Params["field"] != tc.wantField {
				t.Errorf("Params[field] = %v, want %q (the bundled copy interpolates it)", err.Params["field"], tc.wantField)
			}
		})
	}
}

// TestRowJSON: a missing row and a row that is entirely unset must render the same four nulls; a missing key would make
// the frontend read undefined instead of "not set".
func TestRowJSON(t *testing.T) {
	for _, r := range []*Row{nil, {}} {
		got := r.JSON()
		if len(got) != 4 {
			t.Fatalf("Row.JSON() has %d keys, want 4", len(got))
		}
		for _, name := range fieldNames {
			if v, ok := got[name]; !ok || v != nil {
				t.Errorf("Row.JSON()[%q] = %v (present=%v), want nil", name, v, ok)
			}
		}
	}

	got := (&Row{MaxIterations: null(80)}).JSON()
	if got["max_iterations"] != int32(80) {
		t.Errorf("max_iterations = %v, want 80", got["max_iterations"])
	}
}

// The fallbacks must equal the hard-coded constants that existed before this package, one for one:
// with no table, behaviour has to match today's exactly, or "we added a setting" silently becomes a behaviour change.
func TestFallbackMatchesLegacyConstants(t *testing.T) {
	fb := Fallback()
	if fb.MaxIterations != 80 {
		t.Errorf("MaxIterations fallback = %d, want 80 (formerly agent_impl.maxIterations)", fb.MaxIterations)
	}
	if fb.MaxCallDepth != 3 {
		t.Errorf("MaxCallDepth fallback = %d, want 3 (formerly caller.maxCallDepth)", fb.MaxCallDepth)
	}
	if fb.MaxTools != 50 {
		t.Errorf("MaxTools fallback = %d, want 50 (formerly toolgrant.DefaultMaxTools)", fb.MaxTools)
	}
	if fb.MaxOutputBytes != 64<<10 {
		t.Errorf("MaxOutputBytes fallback = %d, want 65536 (formerly tools.DefaultMaxOutput)", fb.MaxOutputBytes)
	}
}
