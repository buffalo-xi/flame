package tools

import (
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// 目录约定的真源在 nexus/shared/paths，原因：
//   - 平台侧开通租户时也要按同一约定寻址 tenants/<schema>/{agents,skills,...}，
//     而平台侧不允许 import nexus/service/**
//   - 控制台管理端（nexus/admin/*）本来就在用这些函数（SafePath/AgentsKey/ValidName），
//     属于"管理端反向依赖 service 层"，一并扶正
//
// 本文件保留同名符号做转发，现有调用点（本包内 file.go/exec.go/meta.go/tool.go 与
// admin/agents、admin/skills、admin/flows、service/agent/*）零改动。

// 目录名常量：转发自 paths，供本包内使用（如 Context.StoragePath 的越界断言）。
// 需要其它子目录名时按需再加，避免留下指代不清的死常量。
const (
	tenantsSubdir = paths.TenantsSubdir
	// skillEntryFile skill 入口文件名（转发自 paths，见其注释）。
	skillEntryFile = paths.SkillEntryFile
	// agentEntryFile agent 入口文件名（转发自 paths，见其注释）。
	agentEntryFile = paths.AgentEntryFile
	// agentSkillsSubdir agent 私有技能目录名（转发自 paths）。
	agentSkillsSubdir = paths.AgentSkillsSubdir
)

// ValidName 校验 agent/skill 名字合法。
func ValidName(name string) bool { return paths.ValidName(name) }

// AgentsKey 返回 agent 定义域相对存储根的逻辑路径（/ 分隔，供 Storage 调用）。
func AgentsKey(schema, name string) string { return paths.AgentsKey(schema, name) }

// ResolveAgentDir 返回 agent 定义域本地绝对路径：{root}/tenants/{schema}/agents/{name}。
func ResolveAgentDir(rootDir, schema, name string) string {
	return paths.ResolveAgentDir(rootDir, schema, name)
}

// SkillsKey 返回租户级共享 skill 根目录相对存储根的逻辑路径（/ 分隔，供 Storage 调用）。
func SkillsKey(schema string) string { return paths.SkillsKey(schema) }

// SkillPath 返回某个租户级 skill 的 SKILL.md 相对存储根的路径。
func SkillPath(schema, name string) string { return paths.SkillPath(schema, name) }

// AgentSkillPath 返回某个 agent 私有 skill 的 SKILL.md 相对存储根的路径。
func AgentSkillPath(schema, agentName, name string) string {
	return paths.AgentSkillPath(schema, agentName, name)
}

// ResolveSkillsDir 返回租户级共享 skill 根目录的本地绝对路径。
func ResolveSkillsDir(rootDir, schema string) string {
	return paths.ResolveSkillsDir(rootDir, schema)
}

// AgentVersionKey 第 n 版 agent 的版本目录（tenants/<schema>/versions/agents/<name>/v<n>）。
func AgentVersionKey(schema, name string, n int) string {
	return paths.VersionKey(schema, paths.VersionAgents, name, n)
}

// SkillVersionKey 第 n 版租户级技能的版本目录。
func SkillVersionKey(schema, name string, n int) string {
	return paths.VersionKey(schema, paths.VersionSkills, name, n)
}

// ToolVersionKey 第 n 版自定义工具的版本目录。
func ToolVersionKey(schema, name string, n int) string {
	return paths.VersionKey(schema, paths.VersionTools, name, n)
}

// RenameAgentDir 改名时同步重命名 agent 定义域目录（源不存在则跳过）。
// 走 storage 抽象，local / nfs / s3 通用。
func RenameAgentDir(st storage.Storage, schema, oldName, newName string) error {
	return paths.RenameAgentDir(st, schema, oldName, newName)
}

// RemoveAgentDir 删除某个 agent 的定义域目录（不存在则跳过）。
// 走 storage 抽象，local / nfs / s3 通用。
func RemoveAgentDir(st storage.Storage, schema, name string) error {
	return paths.RemoveAgentDir(st, schema, name)
}

// SafePath 校验并规范化相对工作目录的路径（导出版本，供跨包使用）。
func SafePath(p string) (string, error) { return paths.SafePath(p) }

// SafePathEx 校验并规范化路径；allowRootAddr 为 true 时允许 "/" 前缀（从租户根算）。
// 本函数只管语法，能访问租户根下的哪些路径由 gate.go 的闸门决定。
func SafePathEx(p string, allowRootAddr bool) (string, bool, error) {
	return paths.SafePathEx(p, allowRootAddr)
}

// safePath 包内简写（file.go / exec.go 使用）。
func safePath(p string) (string, error) { return paths.SafePath(p) }
