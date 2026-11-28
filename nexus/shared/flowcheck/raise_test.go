package flowcheck

import (
	"sort"
	"testing"
)

// A raise never succeeds, so an outgoing edge is dead by construction. The diagram has to say so instead of letting the
// author believe there is a path after it - that belief is the whole reason the node exists.
func TestRaiseIsTerminal(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"raise","data":{"config":{"raise":{"code":"not_found","message":"gone"}}}},
		{"id":"3","type":"result","data":{"config":{"code":{"script":"x"}}}}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e2","source":"2","target":"3"}
	]}`)
	if !has(fs, SeverityError, CodeNoOutgoing) {
		t.Fatalf("codes = %v, want no_outgoing for a raise with an outgoing edge", codes(fs))
	}
}

// The node never succeeds, so dispatching it as a concurrency member would hand a slot to a step that can only fail its
// host.
func TestRaiseIsNotAConcurrentMember(t *testing.T) {
	if CanBeConcurrentMember("raise") {
		t.Error("raise must not be a concurrent member")
	}
	// The editor feeds this list to the "concurrent member" checkbox, so a type missing here is a checkbox the runner
	// then refuses.
	got := NonMemberTypes()
	if !sort.StringsAreSorted(got) {
		t.Errorf("NonMemberTypes = %v, want sorted output", got)
	}
	found := false
	for _, typ := range got {
		if typ == "raise" {
			found = true
		}
	}
	if !found {
		t.Errorf("NonMemberTypes = %v, want raise in it", got)
	}
}

// Both keys are read before anything happens, so a missing or unusable one is an error at save time rather than a
// surprise at run time.
// `local` and `output` come from the node's own vars, and a raise writes neither: the reference does not fail, it renders
// the literal "<no value>" into the message an operator reads. That is the exact class of silent wrong text the node is
// supposed to replace, so the checker names it.
func TestRaiseMessageCannotReadLocalOrOutput(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"local", "order {{ .local.id }} missing", true},
		{"output", "order {{ .output.id }} missing", true},
		{"no spaces", "order {{.local.id}} missing", true},
		{"input is fine", "order {{ .input.order_id }} missing", false},
		{"prose is fine", "local and output variables are not available here", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{"raise": map[string]any{"code": "other", "message": tc.msg}}
			fs, err := Step("raise", cfg, nil)
			if err != nil {
				t.Fatalf("Step = %v", err)
			}
			found := false
			for _, f := range fs {
				if f.Path == "raise.message" && f.Severity == SeverityWarning {
					found = true
				}
			}
			if found != tc.want {
				t.Errorf("warning = %v, want %v (findings %v)", found, tc.want, codes(fs))
			}
		})
	}
}

func TestRaiseStepFindings(t *testing.T) {
	valid := map[string]any{"raise": map[string]any{"code": "not_found", "message": "order not found"}}
	if fs, err := Step("raise", valid, nil); err != nil || len(fs) != 0 {
		t.Fatalf("Step(valid) = %v, %v; want no findings", codes(fs), err)
	}

	// config.inputs is read by the message template, so raise must not be in the inputs-ignoring set.
	withInputs := map[string]any{
		"inputs": map[string]any{"x": map[string]any{"type": "string", "source_type": "flow", "source_key": "x"}},
		"raise":  map[string]any{"code": "other", "message": "boom"},
	}
	if fs, err := Step("raise", withInputs, nil); err != nil || len(fs) != 0 {
		t.Fatalf("Step(with inputs) = %v, %v; want no findings", codes(fs), err)
	}

	cases := []struct {
		name string
		cfg  map[string]any
		path string
	}{
		{"no block", map[string]any{}, "raise"},
		{"no code", map[string]any{"raise": map[string]any{"message": "x"}}, "raise.code"},
		{"reserved code", map[string]any{"raise": map[string]any{"code": "config", "message": "x"}}, "raise.code"},
		{"unknown code", map[string]any{"raise": map[string]any{"code": "bogus", "message": "x"}}, "raise.code"},
		{"no message", map[string]any{"raise": map[string]any{"code": "other"}}, "raise.message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, err := Step("raise", tc.cfg, nil)
			if err != nil {
				t.Fatalf("Step = %v", err)
			}
			found := false
			for _, f := range fs {
				if f.Severity == SeverityError && f.Code == CodeIncomplete && f.Path == tc.path {
					found = true
				}
			}
			if !found {
				t.Errorf("findings = %v, want an incomplete error at %s", codes(fs), tc.path)
			}
		})
	}
}
