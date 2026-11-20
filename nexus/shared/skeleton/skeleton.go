// Package skeleton generates the default body of the definition-domain entry file when a new agent / skill is created.
//
// Why a separate layer:
//   - a skeleton is **content**, not UI copy. It is generated once, at "create and no body written", and from then on it is
//     the user's own AGENT.md / SKILL.md, evolving separately per language — so there is deliberately no "translate at read
//     time", which would make version snapshots, asset packs and global variable validation drift.
//   - the language is merely a generation parameter: the system stays one (tenants / versions / grants never fork by
//     language).
//   - adding a language only needs one more entry in the registry below; the callers (admin handlers) do not change.
//
// The primary skeleton language is English (Default = LangEN); Chinese is the second language.
package skeleton

import "strings"

// Lang is a skeleton language. Only the primary language is recognised, not the region (zh-TW / zh-Hans both map to
// LangZH).
type Lang string

const (
	LangEN Lang = "en"
	LangZH Lang = "zh-CN"

	// Default is what an unspecified or not-yet-supported language falls back to.
	Default = LangEN
)

// Normalize maps any language tag onto a language in the registry.
//
//	"", "fr", "ja"          -> Default (English)
//	"en", "en-US", "EN_us"  -> LangEN
//	"zh", "zh-CN", "zh_CN", "zh-Hans" -> LangZH
//
// An unknown language falls back to English rather than raising an error: a new entity getting a readable English skeleton
// is closer to what a user expects than an empty file or a 500.
func Normalize(raw string) Lang {
	tag := strings.ToLower(strings.TrimSpace(raw))
	tag = strings.ReplaceAll(tag, "_", "-")
	if i := strings.Index(tag, "-"); i > 0 {
		tag = tag[:i]
	}
	switch tag {
	case "zh":
		return LangZH
	case "en":
		return LangEN
	}
	return Default
}

// AgentMD is the entry file body of a new agent.
func AgentMD(lang Lang, name, desc string) string {
	if fn, ok := agentSkeletons[lang]; ok {
		return fn(name, desc)
	}
	return agentSkeletons[Default](name, desc)
}

// SkillMD is the entry file body of a new skill.
func SkillMD(lang Lang, name, desc string) string {
	if fn, ok := skillSkeletons[lang]; ok {
		return fn(name, desc)
	}
	return skillSkeletons[Default](name, desc)
}

// The registry: adding a language means adding one entry here (the Lang constant plus two functions).
var (
	agentSkeletons = map[Lang]func(name, desc string) string{
		LangEN: agentMDEn,
		LangZH: agentMDZh,
	}
	skillSkeletons = map[Lang]func(name, desc string) string{
		LangEN: skillMDEn,
		LangZH: skillMDZh,
	}
)

func title(name, desc string) string {
	return "# " + name + "\n\n" + strings.TrimSpace(desc) + "\n\n"
}

func agentMDEn(name, desc string) string {
	return title(name, desc) +
		"## Role\n\n(Describe what this agent is responsible for and where its boundaries are.)\n\n" +
		"## How it works\n\n(Describe its workflow, constraints and output requirements.)\n"
}

// agentMDZh is generated content for a Chinese-language tenant and therefore stays Chinese (the body rules cover the
// system definition files, not tenant content).
func agentMDZh(name, desc string) string {
	return title(name, desc) +
		"## 角色\n\n（描述这个智能体的职责与能力边界）\n\n" +
		"## 工作方式\n\n（描述它的工作流程、约束与输出要求）\n"
}

func skillMDEn(name, desc string) string {
	return title(name, desc) +
		"## When to use\n\n(Describe the situations this skill applies to.)\n\n" +
		"## What it contains\n\n(Instructions, steps or rules written for the model.)\n"
}

// skillMDZh is generated content for a Chinese-language tenant and therefore stays Chinese.
func skillMDZh(name, desc string) string {
	return title(name, desc) +
		"## 何时使用\n\n（描述这个技能适用于什么场景）\n\n" +
		"## 技能内容\n\n（写给模型看的说明、步骤或规范）\n"
}
