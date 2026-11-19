package agent

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// The reply-language rule must be injected unconditionally and must sit at the end of the system prompt.
//
// What it guards against is the regression a user notices first once the primary language switches to English: with tool descriptions /
// AGENT.md / skill bodies all in English, the model readily mistakes **the prompt's language** for **the reply language** and answers a
// Chinese question in English. So the rule is independent of AGENT.md's content, this run's grants and the sandbox state - missing it is
// a silent regression.
func TestLoadSystemPromptPinsResponseLanguageRule(t *testing.T) {
	root := t.TempDir()
	st := storage.NewFilesystemStorage(root)
	ctx := &tools.Context{
		Schema:         "t0",
		AgentName:      "poet",
		RootDir:        root,
		DefinitionMode: tools.ModeDraft,
		Storage:        st,
	}
	// AGENT.md deliberately states no language convention: the rule must be there even by default
	entry := ctx.AgentPath(paths.AgentEntryFile)
	if err := st.WriteFile(context.Background(), entry, []byte("# 角色\n\n你是一位诗人。\n")); err != nil {
		t.Fatalf("write AGENT.md = %v", err)
	}

	a := &agentImpl{instance: &models.Agent{Name: "poet"}, toolCtx: ctx}
	prompt, err := a.loadSystemPrompt()
	if err != nil {
		t.Fatalf("loadSystemPrompt = %v", err)
	}
	if !strings.Contains(prompt, "Reply in the language the user writes in") {
		t.Errorf("missing the reply-language rule (the model would take the prompt language for the reply language):\n%s", prompt)
	}
	// Last: it constrains everything above it (AGENT.md and the capability / sandbox notes included)
	if !strings.HasSuffix(prompt, responseLanguageRule) {
		t.Errorf("the reply-language rule must be the last thing in the system prompt:\n%s", prompt)
	}
	// An explicit language in AGENT.md wins, and the rule must say so, or it would pull a single-language agent back
	if !strings.Contains(responseLanguageRule, "AGENT.md") {
		t.Error("the rule must state that an explicit setting in AGENT.md wins")
	}
}
