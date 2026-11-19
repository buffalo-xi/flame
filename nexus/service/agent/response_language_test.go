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

// 回复语言约定必须无条件注入，且排在 system prompt 末尾。
//
// 它防的是「主语言换成英语」后最容易被用户感知的退化：工具说明 / AGENT.md / 技能正文
// 都是英文时，模型容易把**提示词的语言**当成**回复的语言**，中文提问被英文回答。
// 所以这条约定与 AGENT.md 内容、本轮授权、沙箱状态都无关 —— 缺了就是静默退化。
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
	// AGENT.md 里刻意不写任何语言约定：缺省也必须带上那条规则
	entry := ctx.AgentPath(paths.AgentEntryFile)
	if err := st.WriteFile(context.Background(), entry, []byte("# 角色\n\n你是一位诗人。\n")); err != nil {
		t.Fatalf("写入 AGENT.md = %v", err)
	}

	a := &agentImpl{instance: &models.Agent{Name: "poet"}, toolCtx: ctx}
	prompt, err := a.loadSystemPrompt()
	if err != nil {
		t.Fatalf("loadSystemPrompt = %v", err)
	}
	if !strings.Contains(prompt, "Reply in the language the user writes in") {
		t.Errorf("缺少回复语言约定（模型会把提示词语言当成回复语言）:\n%s", prompt)
	}
	// 末尾：它约束的是前面全部内容（含 AGENT.md 与能力 / 沙箱说明）
	if !strings.HasSuffix(prompt, responseLanguageRule) {
		t.Errorf("回复语言约定应排在 system prompt 最后:\n%s", prompt)
	}
	// AGENT.md 的显式语言指定优先，约定里必须写明这一点，否则会把「只服务某语种的 agent」掰回来
	if !strings.Contains(responseLanguageRule, "AGENT.md") {
		t.Error("约定必须声明「AGENT.md 显式指定优先」")
	}
}
