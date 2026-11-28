package flowcheck

import "testing"

// config.inputs is dead on the types that read none: the run passes and the author believes values were handed in. It is a
// warning rather than an error, because the node itself runs - it just ignores the block.
func TestIgnoredInputs(t *testing.T) {
	cfg := map[string]any{"inputs": map[string]any{
		"x": map[string]any{"type": "string", "source_type": "node", "source_ref": "1", "source_key": "out"},
	}}
	cases := []struct {
		typ  string
		want bool
	}{
		{"process", true}, {"function", true}, {"call", true},
		{"memory", true},
		{"break", true}, {"continue", true}, {"return", true}, {"exit", true},
		// These really do read it, so reporting them would be a false positive on a correct diagram.
		{"code", false}, {"llm", false}, {"http", false}, {"loop", false}, {"branch", false}, {"result", false},
	}
	for _, tc := range cases {
		findings, err := Step(tc.typ, cfg, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		got := false
		for _, f := range findings {
			if f.Code != CodeIgnoredInputs {
				continue
			}
			got = true
			if f.Severity != SeverityWarning {
				t.Errorf("%s: ignored_inputs severity = %s, want warning (the node still runs)", tc.typ, f.Severity)
			}
			if f.Path != "inputs" {
				t.Errorf("%s: ignored_inputs path = %q, want inputs", tc.typ, f.Path)
			}
		}
		if got != tc.want {
			t.Errorf("%s: ignored_inputs reported = %v, want %v", tc.typ, got, tc.want)
		}
	}
}

// The reason is echoed next to the removal, so every ignoring type has to carry one.
func TestInputsIgnoredReasonCoversEveryType(t *testing.T) {
	for typ := range inputsIgnoringTypes {
		if IgnoresInputs(typ) && InputsIgnoredReason(typ) == "" {
			t.Errorf("%s ignores inputs but has no reason text", typ)
		}
	}
	if InputsIgnoredReason("code") != "" {
		t.Error("a type that reads inputs must not claim a reason")
	}
}
