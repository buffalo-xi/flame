package skeleton

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]Lang{
		"":          LangEN,
		"en":        LangEN,
		"EN":        LangEN,
		"en-US":     LangEN,
		"en_us":     LangEN,
		"zh":        LangZH,
		"zh-CN":     LangZH,
		"zh_CN":     LangZH,
		"zh-Hans":   LangZH,
		"zh-TW":     LangZH, // 只认主语言：地区不参与选择
		"fr":        LangEN, // 未注册语言回落默认（英文）
		"  zh-cn  ": LangZH,
	}
	for raw, want := range cases {
		if got := Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestAgentMDSections(t *testing.T) {
	en := AgentMD(LangEN, "poet", "writes poems")
	for _, want := range []string{"# poet", "writes poems", "## Role", "## How it works"} {
		if !strings.Contains(en, want) {
			t.Errorf("英文骨架缺少 %q：\n%s", want, en)
		}
	}
	zh := AgentMD(LangZH, "poet", "写诗")
	for _, want := range []string{"# poet", "写诗", "## 角色", "## 工作方式"} {
		if !strings.Contains(zh, want) {
			t.Errorf("中文骨架缺少 %q：\n%s", want, zh)
		}
	}
}

func TestSkillMDSections(t *testing.T) {
	en := SkillMD(LangEN, "demo", "")
	for _, want := range []string{"# demo", "## When to use", "## What it contains"} {
		if !strings.Contains(en, want) {
			t.Errorf("英文技能骨架缺少 %q：\n%s", want, en)
		}
	}
	zh := SkillMD(LangZH, "demo", "")
	for _, want := range []string{"# demo", "## 何时使用", "## 技能内容"} {
		if !strings.Contains(zh, want) {
			t.Errorf("中文技能骨架缺少 %q：\n%s", want, zh)
		}
	}
}

// 未注册语言必须与默认语言逐字一致，避免出现「半截英文半截空」的骨架。
func TestUnknownLangFallsBackToDefault(t *testing.T) {
	if got, want := AgentMD(Lang("fr"), "a", "b"), AgentMD(Default, "a", "b"); got != want {
		t.Errorf("未知语言未回落到默认：\n got=%q\nwant=%q", got, want)
	}
	if got, want := SkillMD(Lang("ja"), "a", "b"), SkillMD(Default, "a", "b"); got != want {
		t.Errorf("未知语言未回落到默认：\n got=%q\nwant=%q", got, want)
	}
}
