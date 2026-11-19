package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/capability"
)

// Output validation is shallow only: top-level required plus basic types.
// The point of the assertions is not "how much it blocks" but that **what should pass must pass** - returning extra fields is
// normal (forward compatibility), and treating it as an error would force every script author to chase the schema.
func TestValidateScriptOutput(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"rows"},
		"properties": map[string]any{
			"rows":  map[string]any{"type": "array"},
			"total": map[string]any{"type": "number"},
			"ok":    map[string]any{"type": "boolean"},
		},
	}

	cases := []struct {
		name    string
		result  any
		wantErr string
	}{
		{"valid", map[string]any{"rows": []any{}, "total": float64(3), "ok": true}, ""},
		{"missing required field", map[string]any{"total": float64(1)}, "missing fields"},
		{"type mismatch", map[string]any{"rows": []any{}, "total": "3"}, "wrong type"},
		{"extra fields pass", map[string]any{"rows": []any{}, "extra": "x"}, ""},
		{"not an object at all", []any{1, 2}, "must be a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateScriptOutput("demo", schema, tc.result)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("should pass, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error matching %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Secret masking: only values of length >= 8 are masked.
// Short values (such as "cn") would damage ordinary text, so the rule is fixed and held in place by this test.
func TestMaskSecrets(t *testing.T) {
	cfg := map[string]any{"api_key": "sk-1234567890", "region": "cn", "n": 42}

	out, masked := maskSecrets(`{"echo":"sk-1234567890","region":"cn"}`, cfg)
	if !masked {
		t.Fatal("it should report that masking happened")
	}
	if strings.Contains(out, "sk-1234567890") {
		t.Errorf("the secret was not masked: %s", out)
	}
	if !strings.Contains(out, `"cn"`) {
		t.Errorf("a short value must not be damaged: %s", out)
	}

	if _, m := maskSecrets(`{"x":1}`, nil); m {
		t.Error("without config it must not report masking")
	}
}

// Tool name prefix rule: custom__ is the namespace, and the short name in the database is the name without it.
func TestCustomToolNamePrefix(t *testing.T) {
	if !IsCustomToolName(CustomToolPrefix + "sum_sales") {
		t.Error("the custom__ prefix should be recognised")
	}
	if IsCustomToolName("read_file") || IsCustomToolName("web_search") {
		t.Error("built-in / plugin tools must not be recognised as custom tools")
	}
	if got := CustomShortName(CustomToolPrefix + "sum_sales"); got != "sum_sales" {
		t.Errorf("short name = %q", got)
	}
}

// The capability tier must be exec: a custom tool goes sb.Start -> /v1/exec (a one-shot process, no pool), the same channel as
// run_skill_script. The criterion is the **channel**, not "is a script file being run".
//
// Pinning it prevents a regression: classifying it under sandbox.code (the /v1/code channel tier) would mean "turning off workflow
// code nodes turns off the tenant's custom tools too", and that failure **reports nothing** - the feature simply disappears.
func TestCustomToolRequiresExecCapability(t *testing.T) {
	c := &Context{
		CustomTools: &stubCustomToolSource{spec: &CustomToolSpec{Name: "demo", Lang: "sh", Entry: "main.sh"}},
		// Only the snippet tier is on: this must not be allowed
		Caps: map[string]bool{capability.Code: true},
	}

	_, err := executeCustomTool(context.Background(), c, CustomToolPrefix+"demo", nil)
	if err == nil || !strings.Contains(err.Error(), capability.Exec) {
		t.Fatalf("without exec it should refuse and name %s, got %v", capability.Exec, err)
	}

	// With exec granted it should pass the capability gate and stop at the next check (root not initialized)
	c.Caps = map[string]bool{capability.Exec: true}
	_, err = executeCustomTool(context.Background(), c, CustomToolPrefix+"demo", nil)
	if err == nil || !strings.Contains(err.Error(), "root is not initialized") {
		t.Fatalf("past the capability gate it should stop at the root check, got %v", err)
	}

	// Caps == nil is only for callers with no tenant semantics (tests / internal) and blocks nothing
	c.Caps = nil
	if _, err := executeCustomTool(context.Background(), c, CustomToolPrefix+"demo", nil); err != nil &&
		strings.Contains(err.Error(), "capability") {
		t.Fatalf("a nil Caps must not block on capability, got %v", err)
	}
}

type stubCustomToolSource struct{ spec *CustomToolSpec }

func (s *stubCustomToolSource) List(context.Context) ([]CustomToolSpec, error) { return nil, nil }

func (s *stubCustomToolSource) Get(context.Context, string) (*CustomToolSpec, error) {
	return s.spec, nil
}

func (s *stubCustomToolSource) Status(context.Context, string) (int, bool, error) {
	return 1, true, nil
}
