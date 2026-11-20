package agent

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
)

// TestParseCommand pins the shape of a command: only a leading "/" followed by a valid skill name counts, so a chat message
// that happens to contain a path or a fraction is not swallowed.
func TestParseCommand(t *testing.T) {
	cases := []struct {
		in   string
		name string
		rest string
		ok   bool
	}{
		{"/daily-report", "daily-report", "", true},
		{"/daily-report today", "daily-report", "today", true},
		{"  /daily-report   今天  ", "daily-report", "今天", true},
		{"/a\tb", "a", "b", true},
		{"/", "", "", false},
		{"", "", "", false},
		{"hello /daily-report", "", "", false},
		{"/tmp/x", "", "", false},
		{"3/5", "", "", false},
		{"/under_score", "", "", false},
		{"/-leading-hyphen", "", "", false},
	}
	for _, tc := range cases {
		name, rest, ok := ParseCommand(tc.in)
		if ok != tc.ok || name != tc.name || rest != tc.rest {
			t.Errorf("ParseCommand(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, name, rest, ok, tc.name, tc.rest, tc.ok)
		}
	}
}

// TestCommandSkillsNarrows: the list handed to the chat window is whatever the agent can already see, minus the skills that are
// not user-invocable and minus the deprecated ones. The function must never invent an entry.
func TestCommandSkillsNarrows(t *testing.T) {
	list := []tools.SkillInfo{
		{Name: "invocable", UserInvocable: true},
		{Name: "model-only", UserInvocable: false},
		{Name: "deprecated", UserInvocable: true, Status: int(models.StatusDeprecated)},
	}
	got := commandSkills(list)
	if len(got) != 1 || got[0].Name != "invocable" {
		t.Fatalf("commandSkills = %+v, want only the invocable, non-deprecated skill", got)
	}
	if out := commandSkills(nil); out == nil || len(out) != 0 {
		t.Fatalf("commandSkills(nil) = %v, want an empty non-nil slice", out)
	}
}

// stubCommandSkills embeds the interface: the command path only ever calls Get, so the rest stays unimplemented (and unused).
type stubCommandSkills struct {
	tools.SkillsManager
	detail *tools.SkillDetail
}

func (s *stubCommandSkills) Get(_ context.Context, ref tools.SkillRef) (*tools.SkillDetail, error) {
	if s.detail == nil || s.detail.Name != ref.Name {
		return nil, nil
	}
	return s.detail, nil
}

// TestApplyCommandInjectsAndActivates: a hit rewrites what the model sees and registers the skill with its dependency snapshot,
// so the very next refreshToolDefs puts the declared tools into the schema.
func TestApplyCommandInjectsAndActivates(t *testing.T) {
	skills := &stubCommandSkills{detail: &tools.SkillDetail{
		SkillInfo: tools.SkillInfo{ID: "skill-1", Name: "daily-report", UserInvocable: true},
		Content:   "Write the daily report.",
		ToolDeps:  []string{"local:daily-report"},
	}}
	tc := &tools.Context{Skills: skills}
	a := &agentImpl{toolCtx: tc, args: map[string]interface{}{}}
	WithCommand(a.args, "daily-report", "today")

	a.applyCommand(context.Background())

	msg, _ := a.args["_user_message"].(string)
	if !strings.Contains(msg, "/daily-report") || !strings.Contains(msg, "Write the daily report.") {
		t.Fatalf("_user_message = %q, want the command header and the skill body", msg)
	}
	if !strings.Contains(msg, "Arguments: today") {
		t.Fatalf("_user_message = %q, want the arguments appended", msg)
	}
	if _, ok := tc.LoadedSkills()["skill-1"]; !ok {
		t.Fatal("the skill was not activated: refreshToolDefs would never see its tool dependencies")
	}
	if !tc.ConsumeSkillsDirty() {
		t.Fatal("activation did not mark the tool set dirty")
	}
}

// TestApplyCommandIgnoresUnusable: a skill that exists but is not user-invocable, and a name that resolves to nothing, both leave
// the message untouched - the turn proceeds as ordinary text instead of failing because of a leading slash.
func TestApplyCommandIgnoresUnusable(t *testing.T) {
	cases := []struct {
		name   string
		detail *tools.SkillDetail
	}{
		{"not user-invocable", &tools.SkillDetail{SkillInfo: tools.SkillInfo{ID: "s", Name: "quiet", UserInvocable: false}}},
		{"unknown", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skills := &stubCommandSkills{detail: tc.detail}
			ctx := &tools.Context{Skills: skills}
			a := &agentImpl{toolCtx: ctx, args: map[string]interface{}{"_user_message": "/quiet hi"}}
			WithCommand(a.args, "quiet", "hi")

			a.applyCommand(context.Background())

			if msg, _ := a.args["_user_message"].(string); msg != "/quiet hi" {
				t.Fatalf("_user_message = %q, want it untouched", msg)
			}
			if len(ctx.LoadedSkills()) != 0 {
				t.Fatal("an unusable command must not activate anything")
			}
		})
	}
}

// TestDispatchCommandOnlyHandlesToolDispatch: a skill without a dispatch target falls through, so the run takes the injection
// path and the model still does the work.
func TestDispatchCommandOnlyHandlesToolDispatch(t *testing.T) {
	skills := &stubCommandSkills{detail: &tools.SkillDetail{
		SkillInfo: tools.SkillInfo{ID: "s1", Name: "daily-report", UserInvocable: true},
		Content:   "body",
	}}
	a := &agentImpl{toolCtx: &tools.Context{Skills: skills}, args: map[string]interface{}{}}
	WithCommand(a.args, "daily-report", "today")

	handled, err := a.dispatchCommand(context.Background())
	if handled || err != nil {
		t.Fatalf("dispatchCommand = (%v, %v), want (false, nil) when command_dispatch is not tool", handled, err)
	}
}

// TestDispatchCommandReportsFailure: dispatch goes through ExecuteTool, so a tool the agent has no grant for fails the command
// instead of silently falling back to the model (which would make a broken command look like a slow one).
func TestDispatchCommandReportsFailure(t *testing.T) {
	skills := &stubCommandSkills{detail: &tools.SkillDetail{
		SkillInfo: tools.SkillInfo{
			ID: "s1", Name: "daily-report", UserInvocable: true,
			CommandDispatch: "tool", CommandTool: "local:daily-report",
		},
	}}
	a := &agentImpl{
		toolCtx: &tools.Context{Skills: skills, AllowedTools: map[string]bool{}},
		args:    map[string]interface{}{},
	}
	WithCommand(a.args, "daily-report", "today")

	handled, err := a.dispatchCommand(context.Background())
	if !handled || err == nil {
		t.Fatalf("dispatchCommand = (%v, %v), want handled with an error", handled, err)
	}
	if a.GetError() == "" {
		t.Fatal("the failure must be recorded, otherwise the entry point reports RUN_FINISHED with no answer")
	}
	if !strings.Contains(a.GetError(), "does not hold that tool") || !strings.Contains(a.GetError(), "local:daily-report") {
		t.Fatalf("error = %q, want it to name the configured target and say where to add the grant", a.GetError())
	}
}

// TestCommandTargetName: the configured target is a grant ref when it comes from the dependency list and a tool name when it
// is typed by hand, so both forms have to resolve to the name the runtime dispatches to. Everything else stays unbound, which
// is what turns into the "does not hold that tool" message.
func TestCommandTargetName(t *testing.T) {
	a := &agentImpl{
		toolCtx: &tools.Context{AllowedTools: map[string]bool{"exec": true, "custom__greet": true}},
		toolNamesByRef: map[string]string{
			"builtin:exec": "exec",
			"custom:greet": "custom__greet",
			"local:report": "local__report", // not bound: the ref resolves but the tool is not in AllowedTools
		},
	}
	cases := []struct {
		in      string
		name    string
		granted bool
	}{
		{"builtin:exec", "exec", true}, // ref form: picked from the dependency list
		{"exec", "exec", true},         // name form: typed by hand
		{"custom:greet", "custom__greet", true},
		{"local:report", "local__report", false}, // the ref exists but the tool is not held in this run
		{"builtin:browser", "builtin:browser", false},
	}
	for _, tc := range cases {
		name, granted := a.commandTargetName(tc.in)
		if name != tc.name || granted != tc.granted {
			t.Errorf("commandTargetName(%q) = (%q, %v), want (%q, %v)", tc.in, name, granted, tc.name, tc.granted)
		}
	}

	// No grant gate (callers without grant semantics): the value travels unchanged instead of being reported as ungranted.
	open := &agentImpl{}
	if name, granted := open.commandTargetName("builtin:exec"); name != "builtin:exec" || !granted {
		t.Errorf("commandTargetName without a gate = (%q, %v), want the value passed through", name, granted)
	}
}

// captureCommandTool records the arguments it receives, so the dispatch contract can be asserted.
type captureCommandTool struct {
	got map[string]any
}

func (c *captureCommandTool) Meta() tools.ToolMeta {
	return tools.ToolMeta{Name: "test__capture_command", Description: "test-only: records command dispatch arguments"}
}

func (c *captureCommandTool) Execute(_ context.Context, _ *tools.Context, args map[string]any) (any, error) {
	c.got = args
	return "report ready", nil
}

// TestDispatchCommandPassesOpenClawShape: dispatch hands the target tool {command, commandName, skillName} - the shape a tool
// must be written against - and publishes the tool's text as the answer.
func TestDispatchCommandPassesOpenClawShape(t *testing.T) {
	tool := &captureCommandTool{}
	tools.Register(tool)

	skills := &stubCommandSkills{detail: &tools.SkillDetail{
		SkillInfo: tools.SkillInfo{
			ID: "s1", Name: "daily-report", UserInvocable: true,
			CommandDispatch: "tool", CommandTool: "test__capture_command",
		},
	}}
	tc := &tools.Context{Skills: skills, AllowedTools: map[string]bool{"test__capture_command": true}}
	a := &agentImpl{toolCtx: tc, args: map[string]interface{}{}}
	WithCommand(a.args, "daily-report", "today please")

	handled, err := a.dispatchCommand(context.Background())
	if !handled || err != nil {
		t.Fatalf("dispatchCommand = (%v, %v), want handled without error", handled, err)
	}
	if tool.got["command"] != "today please" || tool.got["commandName"] != "/daily-report" ||
		tool.got["skillName"] != "daily-report" {
		t.Fatalf("tool args = %+v, want the {command, commandName, skillName} shape", tool.got)
	}
	if got := a.GetResult(); got != "report ready" {
		t.Fatalf("result = %q, want the tool output", got)
	}
}

// TestDispatchCommandAcceptsRefTarget: the target is picked from the skill's tool dependencies, so it arrives as a grant ref
// (builtin:...). Dispatch has to map it to the runtime name, otherwise a tool the agent really holds answers "not granted".
func TestDispatchCommandAcceptsRefTarget(t *testing.T) {
	tool := &captureCommandTool{}
	tools.Register(tool)

	skills := &stubCommandSkills{detail: &tools.SkillDetail{
		SkillInfo: tools.SkillInfo{
			ID: "s1", Name: "daily-report", UserInvocable: true,
			CommandDispatch: "tool", CommandTool: "builtin:test__capture_command",
		},
	}}
	tc := &tools.Context{
		Skills:       skills,
		AllowedTools: map[string]bool{"test__capture_command": true},
	}
	a := &agentImpl{
		toolCtx:        tc,
		args:           map[string]interface{}{},
		toolNamesByRef: map[string]string{"builtin:test__capture_command": "test__capture_command"},
	}
	WithCommand(a.args, "daily-report", "today please")

	handled, err := a.dispatchCommand(context.Background())
	if !handled || err != nil {
		t.Fatalf("dispatchCommand = (%v, %v), want the ref target resolved and the command executed", handled, err)
	}
	if got := a.GetResult(); got != "report ready" {
		t.Fatalf("result = %q, want the tool output", got)
	}
}

// TestWithCommandReservedKey: the key is internal, and an empty name or a nil map is a no-op (callers may pass a zero value).
func TestWithCommandReservedKey(t *testing.T) {
	args := map[string]interface{}{}
	WithCommand(args, "poet", "hi")
	if _, ok := args[commandArgKey]; !ok {
		t.Fatal("WithCommand did not write the reserved key")
	}
	WithCommand(args, "", "hi")
	WithCommand(nil, "poet", "hi")
}
