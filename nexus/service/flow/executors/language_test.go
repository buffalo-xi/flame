package executors

import (
	"context"
	"testing"

	agentcore "flame/nexus/service/agent/core"
)

// TestExtractOutputLanguage: the run arguments carry the pin an entry point took from params._meta.language, ctx carries
// the caller's already-resolved language. Both end up as the builtin variable `language`, but only the pin is forwarded
// to a callee - a resolved default that travels would override the next callee's own default.
func TestExtractOutputLanguage(t *testing.T) {
	cases := []struct {
		name       string
		args       map[string]any
		ctx        context.Context
		wantTag    string
		wantPinned bool
	}{
		{"nothing configured", map[string]any{"a": 1}, context.Background(), "", false},
		{"arguments are a pin", map[string]any{agentcore.OutputLanguageKey: "zh-CN"}, context.Background(), "zh-CN", true},
		{"ctx hint stays informational", map[string]any{"a": 1}, agentcore.WithLanguageHint(context.Background(), "ja"), "ja", false},
		{"ctx pin is a pin", map[string]any{"a": 1}, agentcore.WithPinnedLanguage(context.Background(), "ja"), "ja", true},
		{"arguments win over ctx", map[string]any{agentcore.OutputLanguageKey: "zh-CN"}, agentcore.WithPinnedLanguage(context.Background(), "en"), "zh-CN", true},
		{"a malformed tag is not an error", map[string]any{agentcore.OutputLanguageKey: "not a tag!"}, context.Background(), "", false},
		{"a non-string value is ignored", map[string]any{agentcore.OutputLanguageKey: 42}, context.Background(), "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tag, pinned, args := extractOutputLanguage(tc.args, tc.ctx)
			if tag != tc.wantTag {
				t.Errorf("tag = %q, want %q", tag, tc.wantTag)
			}
			if pinned != tc.wantPinned {
				t.Errorf("pinned = %v, want %v", pinned, tc.wantPinned)
			}
			// The reserved key is control intent: it must never stay in the business arguments, where it would become a
			// model-visible {{args._output_language}}.
			if _, leaked := args[agentcore.OutputLanguageKey]; leaked {
				t.Errorf("the reserved key leaked into the business arguments: %v", args)
			}
			// Everything else survives.
			if _, hadA := tc.args["a"]; hadA {
				if args["a"] != tc.args["a"] {
					t.Errorf("business argument a = %v, want %v", args["a"], tc.args["a"])
				}
			}
		})
	}
}

// TestExtractOutputLanguageDoesNotMutate: stripping returns a copy, so a caller's map (and the request body it came from)
// keeps its key.
func TestExtractOutputLanguageDoesNotMutate(t *testing.T) {
	args := map[string]any{agentcore.OutputLanguageKey: "zh-CN"}
	if _, _, _ = extractOutputLanguage(args, context.Background()); args[agentcore.OutputLanguageKey] != "zh-CN" {
		t.Fatalf("the original arguments were modified: %v", args)
	}
}
