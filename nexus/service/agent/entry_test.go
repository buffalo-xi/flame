package agent

import "testing"

// 入口层用这两个函数标记「这次读草稿还是读当前版本」。
// 键名一旦漂移，正式入口就会悄悄退回草稿 —— 所以连键名一起钉住。
func TestDefinitionModeArguments(t *testing.T) {
	args := map[string]interface{}{"args": map[string]any{}}
	SetReleaseMode(args)
	if got := args[DefinitionModeKey]; got != "current" {
		t.Fatalf("SetReleaseMode 写入 %v，want %q（键 %q）", got, "current", DefinitionModeKey)
	}
	SetDraftMode(args)
	if got := args[DefinitionModeKey]; got != "draft" {
		t.Fatalf("SetDraftMode 写入 %v，want %q", got, "draft")
	}
	// nil map 不 panic：入口层参数可能为 nil（例如纯内部调用）
	SetReleaseMode(nil)
	SetDraftMode(nil)
}
