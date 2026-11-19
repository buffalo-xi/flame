// Package skeleton 生成新建 agent / skill 时写入定义域入口文件的默认正文。
//
// 为什么单独一层：
//   - 骨架是**内容**，不是界面文案。它只在「新建且没写正文」时生成一次，
//     之后就变成用户自己的 AGENT.md / SKILL.md，各语言各自演进 ——
//     因此刻意不做「读取时按语言翻译」，否则版本快照、资产包、全局变量校验的语义都会漂。
//   - 语言只是生成参数：系统仍是一套（租户 / 版本 / 授权都不按语言分叉）。
//   - 新增语言只需在下面的注册表里加一项，调用方（admin handlers）不用改。
//
// 骨架主语言是英文（Default = LangEN），中文是第二语言。
package skeleton

import "strings"

// Lang 是骨架语言。只认主语言，不认地区（zh-TW / zh-Hans 都归 LangZH）。
type Lang string

const (
	LangEN Lang = "en"
	LangZH Lang = "zh-CN"

	// Default 未指定或暂不支持的语言都落到它。
	Default = LangEN
)

// Normalize 把任意语言标签归一到注册表里的语言。
//
//	""、"fr"、"ja"          → Default（英文）
//	"en"、"en-US"、"EN_us"  → LangEN
//	"zh"、"zh-CN"、"zh_CN"、"zh-Hans" → LangZH
//
// 不认识的语言回落英文而不是报错：新建实体拿到一份可读的英文骨架，
// 比拿到空文件或 500 更接近用户预期。
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

// AgentMD 新建 agent 的入口文件正文。
func AgentMD(lang Lang, name, desc string) string {
	if fn, ok := agentSkeletons[lang]; ok {
		return fn(name, desc)
	}
	return agentSkeletons[Default](name, desc)
}

// SkillMD 新建 skill 的入口文件正文。
func SkillMD(lang Lang, name, desc string) string {
	if fn, ok := skillSkeletons[lang]; ok {
		return fn(name, desc)
	}
	return skillSkeletons[Default](name, desc)
}

// 注册表：新增语言在这里各加一项即可（Lang 常量 + 两个函数）。
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

func skillMDZh(name, desc string) string {
	return title(name, desc) +
		"## 何时使用\n\n（描述这个技能适用于什么场景）\n\n" +
		"## 技能内容\n\n（写给模型看的说明、步骤或规范）\n"
}
