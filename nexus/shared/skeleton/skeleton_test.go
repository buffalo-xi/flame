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
		"zh-TW":     LangZH, // only the primary language counts: the region takes no part in the choice
		"fr":        LangEN, // an unregistered language falls back to the default (English)
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
			t.Errorf("the English skeleton is missing %q:\n%s", want, en)
		}
	}
	zh := AgentMD(LangZH, "poet", "写诗")
	for _, want := range []string{"# poet", "写诗", "## 角色", "## 工作方式"} {
		if !strings.Contains(zh, want) {
			t.Errorf("the Chinese skeleton is missing %q:\n%s", want, zh)
		}
	}
}

func TestSkillMDSections(t *testing.T) {
	en := SkillMD(LangEN, "demo", "")
	for _, want := range []string{"# demo", "## When to use", "## What it contains"} {
		if !strings.Contains(en, want) {
			t.Errorf("the English skill skeleton is missing %q:\n%s", want, en)
		}
	}
	zh := SkillMD(LangZH, "demo", "")
	for _, want := range []string{"# demo", "## 何时使用", "## 技能内容"} {
		if !strings.Contains(zh, want) {
			t.Errorf("the Chinese skill skeleton is missing %q:\n%s", want, zh)
		}
	}
}

// An unregistered language must match the default byte for byte, so a skeleton is never half English and half empty.
func TestUnknownLangFallsBackToDefault(t *testing.T) {
	if got, want := AgentMD(Lang("fr"), "a", "b"), AgentMD(Default, "a", "b"); got != want {
		t.Errorf("an unknown language did not fall back to the default:\n got=%q\nwant=%q", got, want)
	}
	if got, want := SkillMD(Lang("ja"), "a", "b"), SkillMD(Default, "a", "b"); got != want {
		t.Errorf("an unknown language did not fall back to the default:\n got=%q\nwant=%q", got, want)
	}
}
