package tools

import (
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// The single source of truth for the directory conventions is nexus/shared/paths, because:
//   - the platform side also addresses tenants/<schema>/{agents,skills,...} by the same convention when provisioning a
//     tenant, and the platform side may not import nexus/service/**
//   - the console admin side (nexus/admin/*) already uses these functions (SafePath/AgentsKey/ValidName), which was "the
//     admin side depending backwards on the service layer" and is now regularised
//
// This file keeps same-named symbols as forwarding wrappers, so existing call sites (file.go/exec.go/meta.go/tool.go in this
// package plus admin/agents, admin/skills, admin/flows, service/agent/*) need not change.

// Directory-name constants: forwarded from paths for use inside this package (e.g. the escape assertion in
// Context.StoragePath).
// Add more subdirectory names only when needed, to avoid leaving dead constants with an unclear meaning.
const (
	tenantsSubdir = paths.TenantsSubdir
	// skillEntryFile is the skill entry file name (forwarded from paths, see its comment).
	skillEntryFile = paths.SkillEntryFile
	// agentEntryFile is the agent entry file name (forwarded from paths, see its comment).
	agentEntryFile = paths.AgentEntryFile
	// agentSkillsSubdir is the agent private-skill directory name (forwarded from paths).
	agentSkillsSubdir = paths.AgentSkillsSubdir
)

// ValidName validates an agent / skill name.
func ValidName(name string) bool { return paths.ValidName(name) }

// AgentsKey returns the logical path of an agent's definition domain relative to the storage root (slash-separated, for
// Storage calls).
func AgentsKey(schema, name string) string { return paths.AgentsKey(schema, name) }

// ResolveAgentDir returns an agent's definition-domain absolute local path:
// {root}/tenants/{schema}/agents/{name}.
func ResolveAgentDir(rootDir, schema, name string) string {
	return paths.ResolveAgentDir(rootDir, schema, name)
}

// SkillsKey returns the logical path of the tenant-level shared skill root relative to the storage root (slash-separated,
// for Storage calls).
func SkillsKey(schema string) string { return paths.SkillsKey(schema) }

// SkillPath returns the path of a tenant-level skill's SKILL.md relative to the storage root.
func SkillPath(schema, name string) string { return paths.SkillPath(schema, name) }

// AgentSkillPath returns the path of an agent's private skill's SKILL.md relative to the storage root.
func AgentSkillPath(schema, agentName, name string) string {
	return paths.AgentSkillPath(schema, agentName, name)
}

// ResolveSkillsDir returns the absolute local path of the tenant-level shared skill root.
func ResolveSkillsDir(rootDir, schema string) string {
	return paths.ResolveSkillsDir(rootDir, schema)
}

// AgentVersionKey is the version directory of the n-th agent version (tenants/<schema>/versions/agents/<name>/v<n>).
func AgentVersionKey(schema, name string, n int) string {
	return paths.VersionKey(schema, paths.VersionAgents, name, n)
}

// SkillVersionKey is the version directory of the n-th tenant-level skill version.
func SkillVersionKey(schema, name string, n int) string {
	return paths.VersionKey(schema, paths.VersionSkills, name, n)
}

// ToolVersionKey is the version directory of the n-th custom tool version.
func ToolVersionKey(schema, name string, n int) string {
	return paths.VersionKey(schema, paths.VersionTools, name, n)
}

// RenameAgentDir renames an agent's definition-domain directory along with a rename (skipped when the source is missing).
// It goes through the storage abstraction, so local / nfs / s3 all work.
func RenameAgentDir(st storage.Storage, schema, oldName, newName string) error {
	return paths.RenameAgentDir(st, schema, oldName, newName)
}

// RemoveAgentDir deletes an agent's definition-domain directory (skipped when missing).
// It goes through the storage abstraction, so local / nfs / s3 all work.
func RemoveAgentDir(st storage.Storage, schema, name string) error {
	return paths.RemoveAgentDir(st, schema, name)
}

// SafePath validates and normalises a path relative to the working directory (an exported version for cross-package use).
func SafePath(p string) (string, error) { return paths.SafePath(p) }

// SafePathEx validates and normalises a path; when allowRootAddr is true a leading "/" is allowed (addressing from the tenant
// root).
// This function only handles syntax; which paths under the tenant root are reachable is decided by the gate in gate.go.
func SafePathEx(p string, allowRootAddr bool) (string, bool, error) {
	return paths.SafePathEx(p, allowRootAddr)
}

// safePath is the in-package shorthand (used by file.go / exec.go).
func safePath(p string) (string, error) { return paths.SafePath(p) }
