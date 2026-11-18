package agent

import (
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
)

// 常驻工具（shell / browser）的成本提示：只在被授予它们时出现，且必须给「一次性优先 + 用完收掉」的判据。
// 断言用关键词而非整句：文案可以改，判据（先试 exec / 用完 kill|close）不能丢。
//
// 反例同样重要：只授一次性工具的 agent 不该看到这条 —— 提示词里每多一条无关规则，
// 模型就多一次「我是不是不该用工具」的犹豫。
func TestCapabilityNotesResidentTools(t *testing.T) {
	newImpl := func(allowed map[string]bool) *agentImpl {
		return &agentImpl{
			instance: &models.Agent{Status: models.StatusNormal},
			toolCtx:  &tools.Context{AllowedTools: allowed},
		}
	}

	shell := newImpl(map[string]bool{"exec": true, "shell": true}).capabilityNotes()
	for _, want := range []string{"常驻进程", "exec", "kill"} {
		if !strings.Contains(shell, want) {
			t.Errorf("授权 shell 时应提示常驻成本与收尾动作（缺 %q）:\n%s", want, shell)
		}
	}

	browser := newImpl(map[string]bool{"browser": true}).capabilityNotes()
	for _, want := range []string{"常驻进程", "close"} {
		if !strings.Contains(browser, want) {
			t.Errorf("授权 browser 时应提示常驻成本与收尾动作（缺 %q）:\n%s", want, browser)
		}
	}

	plain := newImpl(map[string]bool{"exec": true, "read_file": true}).capabilityNotes()
	if strings.Contains(plain, "常驻进程") {
		t.Errorf("未授权 shell / browser 时不该出现常驻工具提示:\n%s", plain)
	}
}
