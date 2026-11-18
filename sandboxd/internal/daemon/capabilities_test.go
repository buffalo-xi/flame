package daemon

import (
	"reflect"
	"testing"
)

// code_langs 的判据必须是「已建池的语言」——它是工作流编辑器与创建工作流的
// agent 唯一的选语言依据：比实际服务的集合**宽**，就会出现「编辑器里有、跑起来
// unsupported_lang」；比它**窄**，则白白藏起可用的语言。
func TestCodeLangsFollowsPools(t *testing.T) {
	s := &Server{pools: map[string]*slotPool{"lua": nil, "python": nil}}
	want := []string{"lua", "python"} // 顺序稳定（字典序），消费方的下拉不必每次重排

	if got := s.codeLangs(); !reflect.DeepEqual(got, want) {
		t.Errorf("codeLangs() = %v, want %v", got, want)
	}

	// 顶层 langs 里有、但没开双工池的语言不该出现（只跑技能脚本的语言就是这种）。
	s.cfg = &Config{Langs: map[string]LangDef{"ruby": {Command: "ruby"}}}
	if got := s.codeLangs(); !reflect.DeepEqual(got, want) {
		t.Errorf("未建池的语言不该出现在 code_langs: %v", got)
	}
}
