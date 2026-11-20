package flow

import (
	"strings"
	"testing"
)

// The builtin key set is an engine contract: once a script writes builtin.run_id, renaming it is a
// breaking change. This case pins the current key set — adding or removing a key requires updating
// builtinVars.js (the picker) in desktop and re-checking this function at the same time.
func TestBuiltinVarsKeys(t *testing.T) {
	ctx := &ExecContext{
		SessionID: "sess-1",
		RunID:     "run-1",
		UserID:    "user-1",
		Schema:    "t0",
		Language:  "zh-CN",
	}
	want := []string{"session_id", "run_id", "user_id", "schema", "flow_id", "language"}
	got := ctx.BuiltinVars()
	if len(got) != len(want) {
		t.Fatalf("key count = %d, want %d (adding or removing a key requires updating the frontend picker)", len(got), len(want))
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q (current: %v)", k, got)
		}
	}
	// Without a flow instance flow_id falls back to the empty string rather than panicking.
	if got["flow_id"] != "" {
		t.Errorf("flow_id = %#v, want the empty string", got["flow_id"])
	}
	if got["language"] != "zh-CN" {
		t.Errorf("language = %#v, want zh-CN", got["language"])
	}
}

// A declared variable always resolves; an empty value is "" (never nil) so scripts can test for
// emptiness uniformly across languages.
func TestGetVariablesBuiltin(t *testing.T) {
	ctx := &ExecContext{RunID: "run-1", UserID: "user-1", Schema: "t0"}
	defs := map[string]interface{}{
		"sid": map[string]interface{}{
			"source_type": "builtin",
			"source_key":  "session_id",
			"source_ref":  "",
		},
		"rid": map[string]interface{}{
			"source_type": "builtin",
			"source_key":  "run_id",
		},
	}

	values, err := ctx.GetVariables(defs, "")
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if v, ok := values["sid"]; !ok || v != "" {
		t.Errorf("sid = %#v without a session, want the empty string", v)
	}
	if values["rid"] != "run-1" {
		t.Errorf("rid = %#v, want run-1", values["rid"])
	}

	ctx.SessionID = "sess-1"
	values, err = ctx.GetVariables(defs, "")
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if values["sid"] != "sess-1" {
		t.Errorf("sid = %#v, want sess-1", values["sid"])
	}
}

// A misspelled key must fail loudly. Returning nil silently would let the script run on with an empty
// value, and the run log would merely look like "no value this time" — nearly impossible to attribute.
func TestGetVariablesBuiltinUnknownKey(t *testing.T) {
	ctx := &ExecContext{}
	_, err := ctx.GetVariables(map[string]interface{}{
		"x": map[string]interface{}{
			"source_type": "builtin",
			"source_key":  "sessionid", // the underscore is missing
		},
	}, "")
	if err == nil || !strings.Contains(err.Error(), "unknown builtin variable") {
		t.Fatalf("err = %v, want one containing unknown builtin variable", err)
	}
}

// A builtin the node never declared must not appear in the result — that is the premise of "explicit
// inclusion": most scripts do not need the run context and must not find extra keys in their input.
func TestGetVariablesWithoutBuiltinDeclaration(t *testing.T) {
	ctx := &ExecContext{SessionID: "sess-1", RunFlowVars: map[string]interface{}{"present": 1}}
	values, err := ctx.GetVariables(map[string]interface{}{
		"a": map[string]interface{}{
			"source_type": "flow",
			"source_key":  "present",
		},
	}, "")
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if _, ok := values["session_id"]; ok {
		t.Errorf("session_id appeared although it was not declared: %v", values)
	}
	if len(values) != 1 {
		t.Errorf("result key count = %d, want 1 (%v)", len(values), values)
	}
}
