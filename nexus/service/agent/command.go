package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/service/agent/core"
	"flame/nexus/service/agent/tools"
)

// commandArgKey is the run argument carrying a parsed "/<name> <args>" invocation.
//
// It is a reserved key (the leading underscore keeps it out of the tenant's namespace) filled by the chat entry point after it
// has already validated that the skill exists and is user-invocable. The agent layer re-resolves it against its own
// visibility surface anyway: the value arrives through the run arguments and can also be forged by an external caller, so
// it is treated as "the user's intent", never as an authorization.
const commandArgKey = "_command"

// WithCommand records a "/<name> <rest>" invocation in the run arguments.
//
// The chat entry point calls it after it has validated that the skill exists and is user-invocable (so a wrong pick gets a
// proper error code instead of a failure inside the run). It is a function rather than a public constant so the reserved key
// stays private to this package.
func WithCommand(args map[string]any, name, rest string) {
	if args == nil || name == "" {
		return
	}
	args[commandArgKey] = map[string]any{"name": name, "args": rest}
}

// ParseCommand splits a "/<name> <rest>" message into the command name and its arguments.
//
// ok is false when the text is not a command shape at all. That includes a bare "/" and anything whose first segment is not
// a valid skill name: "/home/user", "/tmp/x" and "3/5" are ordinary text, because a chat message that happens to start with
// a slash must not be swallowed. Whether the name really names a skill is a separate question - the caller resolves it, and
// an unknown name falls back to ordinary text as well.
func ParseCommand(text string) (name, rest string, ok bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") {
		return "", "", false
	}
	body := t[1:]
	if i := strings.IndexAny(body, " \t\r\n"); i >= 0 {
		name, rest = body[:i], strings.TrimSpace(body[i:])
	} else {
		name = body
	}
	if !tools.ValidName(name) {
		return "", "", false
	}
	return name, rest, true
}

// applyCommand rewrites the message the model sees when the entry point sent a "/name args" invocation, and pre-activates
// the skill's tool dependencies.
//
// Three things happen here, and the third is the one that is easy to forget:
//   - the skill is resolved through the agent's own visibility criterion (the same Get as read_skill), so a forged command
//     can never reach a skill this agent could not already read;
//   - the body and the user's arguments replace _user_message (the stored record keeps the original "/name args" text);
//   - MarkSkillLoaded registers the skill with its dependency snapshot, so refreshToolDefs - which runs right after - puts
//     the declared tools into the schema before the first inference. Without it the user picks a skill and its tools never
//     appear, which reads as "the command did nothing".
//
// Anything that does not resolve cleanly (unknown name, not user-invocable, deprecated) leaves the message untouched: the
// turn proceeds as ordinary text rather than failing because of a leading slash.
func (a *agentImpl) applyCommand(ctx context.Context) {
	if a.toolCtx == nil || a.toolCtx.Skills == nil {
		return
	}
	raw, _ := a.args[commandArgKey].(map[string]any)
	name, _ := raw["name"].(string)
	rest, _ := raw["args"].(string)
	if name == "" {
		return
	}
	detail, err := a.toolCtx.Skills.Get(ctx, tools.SkillRef{Name: name})
	if err != nil || detail == nil || !detail.UserInvocable {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The user invoked the skill \"/%s\" directly. Follow its SKILL.md below and carry out the task with the "+
		"arguments given.", detail.Name)
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(detail.Content))
	if rest != "" {
		b.WriteString("\n\nArguments: ")
		b.WriteString(rest)
	}
	a.args["_user_message"] = b.String()

	a.toolCtx.MarkSkillLoaded(detail.ID, detail.Name, detail.ToolDeps)
}

// dispatchCommand runs a "/name args" command whose skill declares command_dispatch=tool, without involving the model.
//
// handled=false means this run is not such a command (ordinary text, or a skill that only injects its body), and Stream
// proceeds normally. handled=true means the run ends here: the tool's output becomes the answer, so the entry point still
// sees TEXT_MESSAGE_CONTENT followed by the usual RUN_FINISHED.
//
// The call goes through ExecuteTool like any other, so the grant gate and the capability check still apply: dispatch bypasses
// the model, never the authorization. A failure is reported (RUN_ERROR at the entry point) rather than falling back to the
// model - a silent fallback would make "this command is broken" look like "this command works, slowly".
func (a *agentImpl) dispatchCommand(ctx context.Context) (bool, error) {
	if a.toolCtx == nil || a.toolCtx.Skills == nil {
		return false, nil
	}
	raw, _ := a.args[commandArgKey].(map[string]any)
	name, _ := raw["name"].(string)
	rest, _ := raw["args"].(string)
	if name == "" {
		return false, nil
	}
	detail, err := a.toolCtx.Skills.Get(ctx, tools.SkillRef{Name: name})
	if err != nil || detail == nil || !detail.UserInvocable {
		return false, nil
	}
	if detail.CommandDispatch != "tool" || strings.TrimSpace(detail.CommandTool) == "" {
		return false, nil
	}

	// The target is normally a grant ref (it is picked from the skill's tool dependencies), so it has to be resolved to the
	// runtime tool name before the grant gate can judge it - the gate speaks in names, and feeding the ref through unchanged
	// made a tool the agent really holds look ungranted.
	target, granted := a.commandTargetName(strings.TrimSpace(detail.CommandTool))
	if !granted {
		// The grant gate is part of the contract, so fail with a sentence that says where to fix it. ExecuteTool would refuse
		// just as well, but "tool X is not granted to agent Y" leaves the user guessing whether the skill or the agent is
		// misconfigured - and the answer is always the agent: a skill's declaration never grants anything.
		msg := fmt.Sprintf("command /%s dispatches to %s, but agent %s does not hold that tool in this run - a tool dependency "+
			"declared by a skill is not a grant, so grant it in the agent's tool configuration",
			detail.Name, target, a.toolCtx.AgentName)
		a.SetError(msg)
		return true, errors.New(msg)
	}

	// The argument shape is the OpenClaw one, fixed so a target tool can be written against it: the raw sentence the user
	// typed, the command name as typed, and the skill name.
	args := map[string]any{
		"command":     rest,
		"commandName": "/" + detail.Name,
		"skillName":   detail.Name,
	}
	result, execErr := tools.ExecuteTool(ctx, a.toolCtx, target, args)
	if execErr != nil {
		msg := fmt.Sprintf("command /%s failed: %v", detail.Name, execErr)
		a.SetError(msg)
		return true, errors.New(msg)
	}
	text := dispatchResultText(result)
	a.pushEvent(core.Event{Type: core.EventText, Delta: text})
	a.SetResult(text)
	return true, nil
}

// commandTargetName resolves a command's configured target to the tool name this run dispatches to.
//
// The field is picked from the skill's tool dependencies, so it normally holds a grant ref (builtin:exec / custom:greet),
// while ExecuteTool and AllowedTools speak in tool names (exec / custom__greet). A hand-typed name is accepted unchanged -
// the UI hint offers both, and a name bound in this run is unambiguous.
//
// ok=false means the resolved name is not held in this run; the returned string is still the most informative form of what
// was configured, so the caller can report it. The ref map only translates, it never authorizes: AllowedTools decides.
func (a *agentImpl) commandTargetName(target string) (name string, ok bool) {
	name = target
	if mapped, ok := a.toolNamesByRef[target]; ok {
		name = mapped
	}
	if a.toolCtx == nil || a.toolCtx.AllowedTools == nil {
		// No grant gate in this context (callers without grant semantics): there is nothing to resolve against, so the value
		// travels as a name and ExecuteTool reports it if no such tool exists.
		return name, true
	}
	return name, a.toolCtx.AllowedTools[name]
}

// dispatchResultText renders a tool result as the answer the user sees: a plain string passes through untouched, everything
// else is JSON so no field is lost.
func dispatchResultText(result any) string {
	if s, ok := result.(string); ok {
		return s
	}
	return marshalToolResult(result, nil)
}
