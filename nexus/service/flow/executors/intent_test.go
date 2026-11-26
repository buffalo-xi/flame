package executors

import (
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// The intent node's config contract: no config, no branch, or branches without ids are all rejected before any
// LLM call is made (which is also why these cases need no gateway).
func TestIntentExecutorConfigErrors(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]any
		wantMsg string
	}{
		{"no config", nil, "intent node is missing its configuration"},
		{"no children", map[string]any{}, "intent node configures no branch"},
		{"empty children", map[string]any{"children": []any{}}, "intent node configures no branch"},
		{"children without id", map[string]any{"children": []any{map[string]any{"label": "x"}}}, "failed to parse the intent branch config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", "intent", tc.config)
			ctx := newExecContext()
			nr := newExecResult(node, ctx)
			if err := (&IntentExecutor{}).Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail for a malformed intent config")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, tc.wantMsg) {
				t.Errorf("ErrMsg = %q, want it to contain %q", nr.ErrMsg, tc.wantMsg)
			}
		})
	}
}

// The fixed intent / index outputs are merged in, because the classifier writes them whether or not the node
// declares them; a declaration the author made wins.
func TestMergeIntentOutputs(t *testing.T) {
	merged := mergeIntentOutputs(map[string]any{
		"outputs": map[string]any{"custom": map[string]any{"type": "string"}},
	})
	if got, _ := merged["intent"].(map[string]any)["type"]; got != "string" {
		t.Errorf("intent type = %v, want string", got)
	}
	if got, _ := merged["index"].(map[string]any)["type"]; got != "number" {
		t.Errorf("index type = %v, want number", got)
	}
	if _, ok := merged["custom"]; !ok {
		t.Errorf("merged = %v, want the declared output kept", merged)
	}

	// An explicit declaration is not overwritten.
	merged = mergeIntentOutputs(map[string]any{
		"outputs": map[string]any{"intent": map[string]any{"type": "object"}},
	})
	if got, _ := merged["intent"].(map[string]any)["type"]; got != "object" {
		t.Errorf("intent type = %v, want the declared object", got)
	}
}

func TestParseIntentBranches(t *testing.T) {
	branches := parseIntentBranches([]any{
		map[string]any{"id": "b1", "label": "Refund", "description": "the user wants money back"},
		map[string]any{"id": "b2"},
		"not a map",
		map[string]any{"label": "no id"},
	})
	if len(branches) != 2 {
		t.Fatalf("parsed %d branches, want 2 (entries without an id are dropped)", len(branches))
	}
	if branches[0].label != "Refund" || branches[0].description != "the user wants money back" {
		t.Errorf("branch[0] = %#v, want the label and description kept", branches[0])
	}
	if branches[1].label != "b2" {
		t.Errorf("branch[1].label = %q, want the id as the fallback label", branches[1].label)
	}
}

// The prompt tells the model the branch list and the answer format; the user's own system prompt is rendered
// first with the node inputs.
func TestBuildIntentMessages(t *testing.T) {
	ctx := newExecContext()
	nr := newExecResult(newExecNode("2", "intent", map[string]any{}), ctx)
	branches := []intentBranchConfig{
		{id: "b1", label: "Refund", description: "money back"},
		{id: "b2", label: "Other"},
	}

	msgs, err := buildIntentMessages(nr, map[string]any{},
		map[string]any{"system_prompt": "You route tickets about {{ .input.topic }}."},
		branches, map[string]any{"topic": "billing"})
	if err != nil {
		t.Fatalf("buildIntentMessages = %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "system" {
		t.Fatalf("messages = %#v, want a single system message", msgs)
	}
	body := msgs[0].Content
	for _, want := range []string{
		"You route tickets about billing.",
		"0. Refund - money back",
		"1. Other",
		`{"index": <number>}`,
		`{"index": -1}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt is missing %q:\n%s", want, body)
		}
	}

	// Without a system_prompt the list and format still stand on their own.
	msgs, err = buildIntentMessages(nr, map[string]any{}, map[string]any{}, branches, nil)
	if err != nil {
		t.Fatalf("buildIntentMessages = %v", err)
	}
	if !strings.Contains(msgs[0].Content, "Available intents") {
		t.Errorf("prompt = %q, want the branch list", msgs[0].Content)
	}

	// A prompt that does not render is a config error the author has to fix.
	if _, err := buildIntentMessages(nr, map[string]any{},
		map[string]any{"system_prompt": "{{"}, branches, nil); err == nil ||
		!strings.Contains(err.Error(), "system_prompt") {
		t.Errorf("broken prompt = %v, want it to name the template", err)
	}
}

// The classifier's answer is model output: a code fence, a bare number, or prose with a JSON object inside are
// all tolerated, and anything unusable falls back to the default branch (-1).
func TestParseIntentIndex(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"json object", `{"index": 2}`, 2},
		{"json with -1", `{"index": -1}`, -1},
		{"bare number", "1", 1},
		{"fenced json", "```json\n{\"index\": 0}\n```", 0},
		{"fenced bare number", "```\n1\n```", 1},
		{"prose around the object", `I think {"index": 1} fits best`, 1},
		{"object without index", `{"other": 1}`, -1},
		{"null index", `{"index": null}`, -1},
		{"garbage", "no idea", -1},
		{"empty", "", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseIntentIndex(tc.content, 3); got != tc.want {
				t.Errorf("parseIntentIndex(%q) = %d, want %d", tc.content, got, tc.want)
			}
		})
	}
}

func TestStripCodeFence(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"```json\n{}\n```", "{}"},
		{"```\n{}\n```", "{}"},
		// A one-line fence has no newline to split on, so only the trailing backticks are stripped; model output
		// always puts the payload on its own line, so this stays an accepted limitation rather than a bug.
		{"```json{}```", "```json{}"},
	}
	for _, tc := range cases {
		if got := stripCodeFence(tc.in); got != tc.want {
			t.Errorf("stripCodeFence(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
