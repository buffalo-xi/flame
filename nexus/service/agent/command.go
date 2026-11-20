package agent

import (
	"context"
	"fmt"
	"strings"

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
