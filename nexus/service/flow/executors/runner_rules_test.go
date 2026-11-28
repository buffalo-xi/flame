package executors

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// The entry arguments' required list is enforced at the one construction point every entry goes through (agent tool, RPC /
// MCP call, nested flow), so a missing value is a caller error instead of a config error inside whichever node reads it.
func TestCheckRequiredArguments(t *testing.T) {
	declaration := map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}, "days": map[string]any{"type": "number"}},
		"required":   []any{"city", "days"},
	}
	if err := checkRequiredArguments(declaration, map[string]any{"city": "上海", "days": float64(3)}); err != nil {
		t.Fatalf("checkRequiredArguments = %v, want nil when every required value is present", err)
	}
	err := checkRequiredArguments(declaration, map[string]any{"city": "上海"})
	if err == nil || !strings.Contains(err.Error(), "days") {
		t.Fatalf("err = %v, want the missing parameter named", err)
	}
	// An explicit null is not a value: the node would read nil and fail later.
	if err := checkRequiredArguments(declaration, map[string]any{"city": nil, "days": float64(3)}); err == nil || !strings.Contains(err.Error(), "city") {
		t.Fatalf("err = %v, want a nil value treated as missing", err)
	}
	// No declaration, or one without a required list: nothing to enforce.
	if err := checkRequiredArguments(nil, map[string]any{}); err != nil {
		t.Fatalf("checkRequiredArguments = %v, want nil without a declaration", err)
	}
	if err := checkRequiredArguments(map[string]any{"properties": map[string]any{}}, map[string]any{}); err != nil {
		t.Fatalf("checkRequiredArguments = %v, want nil without a required list", err)
	}
}

// The check runs before the definition is parsed and before any limit is resolved, so a missing value never reaches a node.
func TestNewFlowRejectsMissingRequiredArgument(t *testing.T) {
	inst := &models.Flow{
		ID:        "f1",
		Name:      "demo",
		Diagram:   xsql.JSONB{Data: json.RawMessage(`{"nodes":[{"id":"1","type":"start","data":{"label":"开始"}}],"edges":[]}`)},
		Arguments: xsql.JSONB{Data: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)},
	}
	_, err := NewFlow(inst, map[string]interface{}{}, "", "run-1", "t0", "u1", context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing required argument") {
		t.Fatalf("NewFlow = %v, want the missing argument refused before the run", err)
	}
}

// on_error is the only way a flow author can react to a failure, so its parsing is the contract: retry / skip /
// handler / post each carry the codes and patterns that decide whether they fire.
func TestParseOnErrorConfig(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"on_error": map[string]any{
			"retry": map[string]any{
				"codes":    []any{"timeout", "busy"},
				"patterns": []any{"%slow%"},
				"max":      float64(3),
				"delay_ms": float64(500),
			},
			"skip": map[string]any{"codes": []any{"notfound"}},
			"handler": map[string]any{
				"codes":  []any{"other"},
				"script": "return 'skip'",
			},
			"post": map[string]any{
				"set_outputs": map[string]any{"status": "failed"},
				"handler":     map[string]any{"lang": "template", "script": "{}"},
			},
		},
	})
	cfg := parseOnErrorConfig(node)

	if cfg.Retry == nil || cfg.Retry.Max != 3 || cfg.Retry.DelayMs != 500 {
		t.Fatalf("Retry = %#v, want max 3 / delay 500", cfg.Retry)
	}
	if len(cfg.Retry.Codes) != 2 || cfg.Retry.Codes[0] != "timeout" || cfg.Retry.Patterns[0] != "%slow%" {
		t.Errorf("Retry rules = %#v, want the codes and patterns", cfg.Retry)
	}
	if cfg.Skip == nil || len(cfg.Skip.Codes) != 1 || cfg.Skip.Codes[0] != "notfound" {
		t.Errorf("Skip = %#v, want the configured code", cfg.Skip)
	}
	if cfg.Handler == nil || cfg.Handler.Script != "return 'skip'" {
		t.Fatalf("Handler = %#v, want the script", cfg.Handler)
	}
	// The language defaults to lua: a handler written for the script node is a lua script unless it says so.
	if cfg.Handler.Lang != "lua" {
		t.Errorf("Handler.Lang = %q, want lua", cfg.Handler.Lang)
	}
	if cfg.Post == nil || cfg.Post.SetOutputs["status"] != "failed" {
		t.Fatalf("Post = %#v, want the set_outputs", cfg.Post)
	}
	if cfg.Post.Handler == nil || cfg.Post.Handler.Lang != "template" {
		t.Errorf("Post.Handler = %#v, want the configured language", cfg.Post.Handler)
	}
}

func TestParseOnErrorConfigAbsent(t *testing.T) {
	for _, node := range []*flow.Node{
		newExecNode("2", "code", map[string]any{}),
		{ID: "3", Type: "code", Data: &flow.NodeData{}},
	} {
		cfg := parseOnErrorConfig(node)
		if cfg.Retry != nil || cfg.Skip != nil || cfg.Handler != nil || cfg.Post != nil {
			t.Errorf("parseOnErrorConfig(%v) = %#v, want an empty config", node.ID, cfg)
		}
	}
	// A node without data at all is the same as "nothing configured".
	if cfg := parseOnErrorConfig(&flow.Node{ID: "4"}); cfg.Retry != nil {
		t.Errorf("cfg = %#v, want empty", cfg)
	}
}

func TestParseStringSlice(t *testing.T) {
	if got := parseStringSlice(nil); got != nil {
		t.Errorf("parseStringSlice(nil) = %v, want nil", got)
	}
	if got := parseStringSlice("x"); got != nil {
		t.Errorf("parseStringSlice(string) = %v, want nil", got)
	}
	got := parseStringSlice([]any{"a", 1, nil, "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("parseStringSlice = %v, want a and b only", got)
	}
}

// An `other` code is the catch-all a script raises itself, so the patterns decide whether it matches; any concrete
// code ignores the patterns.
func TestMatchCodes(t *testing.T) {
	cases := []struct {
		name     string
		codes    []string
		errCode  string
		errMsg   string
		patterns []string
		want     bool
	}{
		{"concrete code matches", []string{"script"}, "script", "boom", nil, true},
		{"concrete code ignores patterns", []string{"script"}, "script", "boom", []string{"%nomatch%"}, true},
		{"no match", []string{"script"}, "config", "boom", nil, false},
		{"other with a matching pattern", []string{"other"}, "other", "database exploded", []string{"%exploded%"}, true},
		{"other with a non-matching pattern", []string{"other"}, "other", "database exploded", []string{"%timeout%"}, false},
		{"other without patterns", []string{"other"}, "other", "anything", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchCodes(tc.codes, tc.errCode, tc.errMsg, tc.patterns); got != tc.want {
				t.Errorf("matchCodes = %v, want %v", got, tc.want)
			}
		})
	}
}

// The pattern syntax: % at the start means "ends with", % at the end means "starts with", both means "contains".
func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern string
		msg     string
		want    bool
	}{
		{"%timeout%", "read timeout while calling the API", true},
		{"%timeout%", "connection refused", false},
		{"%timeout", "read timeout", true},
		{"%timeout", "timeout happened first", false},
		{"rate limit%", "rate limit exceeded", true},
		{"rate limit%", "sudden rate limit", false},
		{"rate limit", "the rate limit was hit", true},
		{"rate limit", "nothing here", false},
	}
	for _, tc := range cases {
		if got := matchPattern(tc.msg, tc.pattern); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.msg, tc.pattern, got, tc.want)
		}
	}
}

// on_error never applies to control flow, to a cancellation, or to the two codes that mean "the diagram / the
// platform is wrong" — those are returned to the caller untouched.
func TestShouldBypassOnError(t *testing.T) {
	base := func(status flow.NodeStatus) (*flow.NodeResult, *flow.ExecContext) {
		ctx := newExecContext()
		nr := newExecResult(newExecNode("2", "code", map[string]any{}), ctx)
		nr.Status = status
		return nr, ctx
	}

	t.Run("control signal", func(t *testing.T) {
		nr, ctx := base(flow.StatusSuccess)
		bypass, err := shouldBypassOnError(SignalBreak, nr, ctx)
		if !bypass || !errors.Is(err, SignalBreak) {
			t.Errorf("bypass = %v err = %v, want the signal returned untouched", bypass, err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		nr, ctx := base(flow.StatusCancelled)
		c, cancel := context.WithCancel(context.Background())
		cancel()
		ctx.StdCtx = c
		bypass, err := shouldBypassOnError(errors.New("anything"), nr, ctx)
		if !bypass || !errors.Is(err, context.Canceled) {
			t.Errorf("bypass = %v err = %v, want the cancellation", bypass, err)
		}
	})

	for _, code := range []string{flow.ErrCodeInternal, flow.ErrCodeConfig} {
		t.Run(code, func(t *testing.T) {
			nr, ctx := base(flow.StatusFailed)
			bypass, _ := shouldBypassOnError(&ExecError{Code: code, Msg: "x"}, nr, ctx)
			if !bypass {
				t.Errorf("%s must bypass on_error", code)
			}
		})
	}

	t.Run("a non-failed status bypasses", func(t *testing.T) {
		nr, ctx := base(flow.StatusCancelled)
		if bypass, _ := shouldBypassOnError(errors.New("x"), nr, ctx); !bypass {
			t.Error("a node that is not Failed must not run on_error")
		}
	})

	t.Run("a real failure does not bypass", func(t *testing.T) {
		nr, ctx := base(flow.StatusFailed)
		bypass, err := shouldBypassOnError(&ExecError{Code: flow.ErrCodeScript, Msg: "boom"}, nr, ctx)
		if bypass || err != nil {
			t.Errorf("bypass = %v err = %v, want on_error to run", bypass, err)
		}
	})
}

// Post handling runs after the error was decided: it writes the configured outputs and may run one script whose
// result lands on declared variables.
func TestApplyPost(t *testing.T) {
	t.Run("nil config is a no-op", func(t *testing.T) {
		nr, _ := newPostResult(t, map[string]any{"fixed": map[string]any{"type": "string"}})
		applyPost(nr, nil)
		if got, _ := nr.OutputVars["fixed"].(string); got != "" {
			t.Errorf("fixed = %q, want untouched", got)
		}
	})

	t.Run("set_outputs writes declared variables", func(t *testing.T) {
		nr, _ := newPostResult(t, map[string]any{"fixed": map[string]any{"type": "string"}})
		applyPost(nr, &PostConfig{SetOutputs: map[string]any{"fixed": "patched"}})
		if got, _ := nr.OutputVars["fixed"].(string); got != "patched" {
			t.Errorf("fixed = %v, want patched", nr.OutputVars["fixed"])
		}
	})

	t.Run("handler result is dispatched", func(t *testing.T) {
		nr, _ := newPostResult(t, map[string]any{"fixed": map[string]any{"type": "string"}})
		applyPost(nr, &PostConfig{Handler: &PostScript{
			Lang:   "template",
			Script: `{"fixed": "from the handler"}`,
		}})
		if got, _ := nr.OutputVars["fixed"].(string); got != "from the handler" {
			t.Errorf("fixed = %v, want the handler's value", nr.OutputVars["fixed"])
		}
		if nr.PostError != "" {
			t.Errorf("PostError = %q, want none", nr.PostError)
		}
	})

	t.Run("handler failure is recorded, not fatal", func(t *testing.T) {
		nr, _ := newPostResult(t, map[string]any{"fixed": map[string]any{"type": "string"}})
		applyPost(nr, &PostConfig{Handler: &PostScript{Lang: "template", Script: "{{"}})
		if nr.PostError == "" {
			t.Error("PostError is empty, want the handler failure recorded")
		}
		if !strings.Contains(strings.Join(nr.RunLogs, "\n"), "post-processing script") {
			t.Errorf("logs = %v, want the failure logged", nr.RunLogs)
		}
	})

	t.Run("an undeclared handler variable is recorded", func(t *testing.T) {
		nr, _ := newPostResult(t, map[string]any{"fixed": map[string]any{"type": "string"}})
		applyPost(nr, &PostConfig{Handler: &PostScript{Lang: "template", Script: `{"nope": 1}`}})
		if !strings.Contains(nr.PostError, "undeclared variable") {
			t.Errorf("PostError = %q, want the dispatch failure", nr.PostError)
		}
	})

	// A set_outputs key that is not declared in outputs cannot be written. That is a post failure, not a curiosity:
	// downstream then reads the type default while the author believes the marker is there.
	t.Run("an undeclared set_outputs key is recorded", func(t *testing.T) {
		nr, _ := newPostResult(t, map[string]any{"fixed": map[string]any{"type": "string"}})
		applyPost(nr, &PostConfig{SetOutputs: map[string]any{"missing": "x"}})
		if !strings.Contains(nr.PostError, "failed to write post output missing") {
			t.Errorf("PostError = %q, want the write failure", nr.PostError)
		}
		if got, _ := nr.OutputVars["fixed"].(string); got != "" {
			t.Errorf("fixed = %q, want untouched", got)
		}
	})

	// Two failures must both survive: the second one used to overwrite PostError, and then the record shows only the
	// script while the unwritten output silently stays missing.
	t.Run("a write failure and a script failure are both kept", func(t *testing.T) {
		nr, _ := newPostResult(t, map[string]any{"fixed": map[string]any{"type": "string"}})
		applyPost(nr, &PostConfig{
			SetOutputs: map[string]any{"missing": "x"},
			Handler:    &PostScript{Lang: "template", Script: "{{"},
		})
		if !strings.Contains(nr.PostError, "failed to write post output missing") {
			t.Errorf("PostError = %q, want the write failure kept", nr.PostError)
		}
		if !strings.Contains(nr.PostError, "template") {
			t.Errorf("PostError = %q, want the script failure appended", nr.PostError)
		}
		if n := strings.Count(nr.PostError, "; "); n != 1 {
			t.Errorf("PostError = %q, want both reasons joined once", nr.PostError)
		}
	})
}

// The retired {{ .global_vars.X }} form fails at load time with an actionable message: left in, the template would
// render "<no value>" and be very hard to trace.
func TestRejectLegacyGlobalRefs(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"code": map[string]any{
			"script": "hi {{ .global_vars.user_name }}",
		},
	})
	node.Data.Label = "Greeting"
	err := rejectLegacyGlobalRefs([]*flow.Node{node})
	if err == nil {
		t.Fatal("rejectLegacyGlobalRefs must reject the old form")
	}
	for _, want := range []string{"Greeting", "user_name", "{{ .input.<name> }}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}

	// The whitespace-trim marker is the same reference.
	trim := newExecNode("3", "code", map[string]any{"code": map[string]any{"script": `{{- .global_vars.rate }}`}})
	if name, ok := legacyGlobalRefIn(trim.Data.Config); !ok || name != "rate" {
		t.Errorf("legacyGlobalRefIn(- form) = %q, %v, want rate", name, ok)
	}

	// Clean configs, and the nodes without a config, pass.
	clean := []*flow.Node{
		nil,
		{ID: "4", Type: "code", Data: &flow.NodeData{}},
		newExecNode("5", "code", map[string]any{"code": map[string]any{"script": "{{ .input.user_name }}"}}),
	}
	if err := rejectLegacyGlobalRefs(clean); err != nil {
		t.Errorf("rejectLegacyGlobalRefs(clean) = %v, want nil", err)
	}
}

func TestLegacyGlobalRefIn(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
		ok   bool
	}{
		{"string", "{{ .global_vars.a }}", "a", true},
		{"nested map", map[string]any{"x": map[string]any{"y": "{{ .global_vars.b }}"}}, "b", true},
		{"slice", []any{"plain", "{{ .global_vars.c }}"}, "c", true},
		{"no reference", "{{ .input.a }}", "", false},
		{"not a container", 42, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, ok := legacyGlobalRefIn(tc.v)
			if name != tc.want || ok != tc.ok {
				t.Errorf("legacyGlobalRefIn(%#v) = %q, %v, want %q, %v", tc.v, name, ok, tc.want, tc.ok)
			}
		})
	}
}

// session_id is a real switch (it turns on session memory), so unlike the advisory language tag it is validated: a
// non-string or a malformed value fails the run instead of being ignored.
func TestExtractSessionID(t *testing.T) {
	const validUUID = "0193a5f0-1111-7abc-8def-0123456789ab"

	t.Run("absent leaves the arguments alone", func(t *testing.T) {
		in := map[string]any{"topic": "x"}
		sid, clean, err := extractSessionID(in)
		if sid != "" || err != nil {
			t.Fatalf("sid = %q err = %v, want disabled", sid, err)
		}
		if clean["topic"] != "x" {
			t.Errorf("clean = %v, want the business arguments untouched", clean)
		}
	})

	t.Run("non-string is rejected", func(t *testing.T) {
		if _, _, err := extractSessionID(map[string]any{"session_id": 42}); err == nil ||
			!strings.Contains(err.Error(), "must be a string") {
			t.Errorf("error = %v, want a type error", err)
		}
	})

	t.Run("blank means disabled and is stripped", func(t *testing.T) {
		in := map[string]any{"session_id": "   ", "topic": "x"}
		sid, clean, err := extractSessionID(in)
		if sid != "" || err != nil {
			t.Fatalf("sid = %q err = %v, want disabled", sid, err)
		}
		if _, ok := clean["session_id"]; ok {
			t.Error("session_id was not stripped from the business arguments")
		}
		if _, ok := in["session_id"]; !ok {
			t.Error("the caller's map was modified")
		}
	})

	t.Run("malformed UUID is rejected", func(t *testing.T) {
		if _, _, err := extractSessionID(map[string]any{"session_id": "not-a-uuid"}); err == nil ||
			!strings.Contains(err.Error(), "not a valid UUID") {
			t.Errorf("error = %v, want a UUID error", err)
		}
	})

	t.Run("valid UUID is returned and stripped", func(t *testing.T) {
		sid, clean, err := extractSessionID(map[string]any{"session_id": validUUID, "topic": "x"})
		if err != nil || sid != validUUID {
			t.Fatalf("sid = %q err = %v, want the UUID", sid, err)
		}
		if _, ok := clean["session_id"]; ok {
			t.Error("session_id was not stripped")
		}
		if clean["topic"] != "x" {
			t.Errorf("clean = %v, want the business arguments kept", clean)
		}
	})
}

func newPostResult(t *testing.T, outputs map[string]any) (*flow.NodeResult, *flow.ExecContext) {
	t.Helper()
	ctx := newExecContext()
	nr := newExecResult(newExecNode("2", "code", map[string]any{}), ctx)
	nr.InitOutputVars(outputs)
	return nr, ctx
}
