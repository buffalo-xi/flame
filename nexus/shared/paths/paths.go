// Package paths holds the directory conventions for tenant data in storage (the source of truth).
//
// Definition domain (shared across users and sessions, maintained by the meta tools):
//
//	agent            : tenants/<schema>/agents/<agent>
//	tenant skills    : tenants/<schema>/skills/<skill>
//	private skills   : tenants/<schema>/agents/<agent>/skills/<skill>
//
// Runtime domain (the venue an agent works in, created and destroyed with a session or a single call; that is the "workspace"):
//
//	user-level files  : tenants/<schema>/users/<user>/files
//	session directory : tenants/<schema>/users/<user>/sessions/<session>
//	sessionless venue : tenants/<schema>/users/<user>/runs/<run> (falling back to tenants/<schema>/runs/<run>)
//
// Why the two domains are strictly separate is explained in docs/workspace-layout-and-supervisor-power.md §1.
//
// The package is deliberately not called workspace: that word now means the runtime working directory (the one mounted at /workspace in
// the sandbox), whereas this package owns **all** directory conventions (definition domain + runtime domain), hence paths.
//
// The directories were renamed from workspace/ (agent definitions) and shared-skills/ (tenant shared skills) to agents/ and skills/; the
// one-off migration of the old layout has been completed and its code has been removed from the repository.
//
// This package used to be service/agent/tools/workspace.go and moved to a neutral location because:
//   - the platform side also has to address tenants/<schema>/{agents,skills,...} by the same convention when provisioning a tenant, and
//     the platform side may not import nexus/service/**
//   - the admin console (nexus/admin/*) already used it (SafePath/AgentsKey/ValidName), which was "the admin side depending backwards on
//     the service layer" - corrected at the same time
//
// service/agent/tools/paths.go keeps the same symbol names as forwarding functions, so no existing call site has to change.
// It depends only on shared/storage and the standard library, and carries no business semantics.
package paths

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"flame/nexus/shared/storage"
)

// TenantsSubdir is the fixed subdirectory holding tenant data under the storage root.
const TenantsSubdir = "tenants"

// AgentsSubdir is the definition domain: one directory per agent (previously "workspace").
const AgentsSubdir = "agents"

// AgentSkillsSubdir is the subdirectory holding an agent's private skills (inside the agent directory).
//
// Note that it has **the same value but a different level** as TenantSkillsSubdir:
// agents/<agent>/skills/<skill> versus skills/<skill>. The parent directory tells the two apart, so do not mix them up because the names
// look alike.
const AgentSkillsSubdir = "skills"

// TenantSkillsSubdir is the definition domain: the tenant-level shared skill directory (previously "shared-skills", i.e. the skills whose
// agent_id is NULL).
const TenantSkillsSubdir = "skills"

// ToolsSubdir is the definition domain: the tenant-level custom tool directory (tools/<toolName>/<entry>).
//
// It sits alongside TenantSkillsSubdir: a script is definition-domain content (on disk like SKILL.md, editable by the file tools, able to
// go into git), and only its metadata lives in the database ({schema}.custom_tools).
const ToolsSubdir = "tools"

// SkillEntryFile is a skill's entry file name (the only file in a skill directory with a runtime convention).
// This is the source of truth: the runtime reads the skill body through it, and both the admin API (admin/skills/files.go) and the file
// tools refuse to **delete** it (editing its content is allowed) - otherwise an empty skill would be left registered in the database with
// no body.
const SkillEntryFile = "SKILL.md"

// AgentEntryFile is an agent's entry file name (the only file in the definition root with a runtime convention).
// This is the source of truth: loadSystemPrompt / create_agent / read_agent all read it at run time, and the admin agent creation and the
// frontend file tree follow it too.
//
// It is **deliberately not a DB field** (the agents.entry_file column has been removed): the entry name is fixed, and making it
// configurable only misleads ("configured but has no effect") - compare SkillEntryFile.
const AgentEntryFile = "AGENT.md"

// UsersSubdir is the runtime domain: the user directory.
const UsersSubdir = "users"

// FilesSubdir holds user-level shared files (uploads / artefacts kept across sessions).
const FilesSubdir = "files"

// SessionsSubdir is the session working directory (the file tools' default root = exec's cwd).
const SessionsSubdir = "sessions"

// RunsSubdir is the temporary working directory for calls with no session (created and destroyed with a single call).
const RunsSubdir = "runs"

// namePattern: an agent / skill name may only contain letters, digits and hyphens (and may not start with a hyphen).
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)

// ValidName reports whether an agent / skill name is legal.
func ValidName(name string) bool {
	return name != "" && len(name) <= 64 && namePattern.MatchString(name)
}

// AgentsKey returns an agent definition root's logical path relative to the storage root (slash separated, for Storage calls).
//
// The name deliberately avoids "Workspace": that word now means the runtime working directory (session / runs), which is a different
// thing from the definition domain (compare Context.Workspace with Context.AgentRoot).
func AgentsKey(schema, name string) string {
	return path.Join(TenantsSubdir, schema, AgentsSubdir, name)
}

// ResolveAgentDir returns an agent definition root's local absolute path: {root}/tenants/{schema}/agents/{name}.
func ResolveAgentDir(rootDir, schema, name string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, AgentsSubdir, name)
}

// SkillsKey returns the tenant-level shared skill root's logical path relative to the storage root (slash separated, for Storage calls).
func SkillsKey(schema string) string {
	return path.Join(TenantsSubdir, schema, TenantSkillsSubdir)
}

// SkillPath returns the path of a tenant-level skill's SKILL.md relative to the storage root.
func SkillPath(schema, name string) string {
	return path.Join(SkillsKey(schema), name, SkillEntryFile)
}

// AgentSkillPath returns the path of an agent's private skill SKILL.md relative to the storage root:
// tenants/{schema}/agents/{agentName}/skills/{name}/SKILL.md
func AgentSkillPath(schema, agentName, name string) string {
	return path.Join(AgentsKey(schema, agentName), AgentSkillsSubdir, name, SkillEntryFile)
}

// ResolveSkillsDir returns the local absolute path of the tenant-level shared skill root.
func ResolveSkillsDir(rootDir, schema string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, TenantSkillsSubdir)
}

// ToolsKey returns the tenant-level custom tool root's logical path relative to the storage root (for Storage calls).
func ToolsKey(schema string) string {
	return path.Join(TenantsSubdir, schema, ToolsSubdir)
}

// PlatformAssetsSubdir is the **snapshot** root of platform-level asset distribution (it belongs to no tenant).
//
// One listing = one snapshot: platform-assets/<assetID>/<build>/<relative path>, a mirror of some definition-domain directory of the source
// tenant (skills/<name>, tools/<name>, agents/<name>).
// Installing copies from here into the target tenant and **never reads the source tenant back** - a cross-tenant read is blocked by
// Context.StoragePath's in-tenant assertion, and the source tenant might be suspended or deleted by then.
//
// The prefix is deliberately outside tenants/:
//   - it is inside no sandbox mount point (/workspace, /agent and tools/ do not cover it);
//   - safePath refuses .., so the tenant-side file tools cannot reach it in the first place.
//
// See docs/asset-sharing.md.
const PlatformAssetsSubdir = "platform-assets"

// PlatformAssetKey returns one snapshot directory's relative key: platform-assets/<assetID>/<build>.
func PlatformAssetKey(assetID string, build int) string {
	return path.Join(PlatformAssetsSubdir, assetID, strconv.Itoa(build))
}

// ResolveToolsDir returns the local absolute path of the tenant-level custom tool root (the sandbox mount source).
func ResolveToolsDir(rootDir, schema string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, ToolsSubdir)
}

// UserFilesKey returns the user-level file directory's path relative to the storage root (uploads / artefacts kept across sessions).
func UserFilesKey(schema, userID string) string {
	return path.Join(TenantsSubdir, schema, UsersSubdir, userID, FilesSubdir)
}

// UserSessionKey returns the session working directory's path relative to the storage root (the file tools' default root, exec's cwd).
func UserSessionKey(schema, userID, sessionID string) string {
	return path.Join(TenantsSubdir, schema, UsersSubdir, userID, SessionsSubdir, sessionID)
}

// RunsKey returns the temporary working directory for sessionless calls relative to the storage root (tenant-level fallback used when no
// user is available).
func RunsKey(schema, runID string) string {
	return path.Join(TenantsSubdir, schema, RunsSubdir, runID)
}

// UserRunsKey returns the path of a user's sessionless temporary directory relative to the storage root.
//
// It is preferred when a user is available (the runtime domain is isolated per user); only when even the user is unavailable does it fall
// back to the tenant-level RunsKey.
func UserRunsKey(schema, userID, runID string) string {
	return path.Join(TenantsSubdir, schema, UsersSubdir, userID, RunsSubdir, runID)
}

// ResolveUserSessionDir returns the local absolute path of a session working directory.
//
// It is where "the file tools' default root" and "the host source mounted at /workspace for exec" land on local storage.
func ResolveUserSessionDir(rootDir, schema, userID, sessionID string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, UsersSubdir, userID, SessionsSubdir, sessionID)
}

// ResolveUserRunsDir returns the local absolute path of a user's sessionless temporary directory.
func ResolveUserRunsDir(rootDir, schema, userID, runID string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, UsersSubdir, userID, RunsSubdir, runID)
}

// ResolveRunsDir returns the local absolute path of the tenant-level sessionless temporary directory (the fallback when no user is
// available).
func ResolveRunsDir(rootDir, schema, runID string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, RunsSubdir, runID)
}

// RenameAgentDir renames an agent definition directory together with a rename (skipped when the source does not exist).
// It goes through the storage abstraction, so local / nfs / s3 all work.
func RenameAgentDir(st storage.Storage, schema, oldName, newName string) error {
	if st == nil || oldName == newName {
		return nil
	}
	return st.Rename(context.Background(), AgentsKey(schema, oldName), AgentsKey(schema, newName))
}

// RemoveAgentDir deletes an agent's definition directory (skipped when it does not exist).
// It goes through the storage abstraction, so local / nfs / s3 all work.
func RemoveAgentDir(st storage.Storage, schema, name string) error {
	if st == nil {
		return nil
	}
	return st.DeletePrefix(context.Background(), AgentsKey(schema, name))
}

// SafePath validates and normalises a path relative to the runtime working directory (an exported snapshot of safePath).
// It is for cross-package use, such as the admin side's definition-domain file API or platform tenant provisioning.
func SafePath(p string) (string, error) { return safePath(p) }

// SafePathEx validates and normalises a path; with allowRootAddr true, a leading "/" is allowed and means "counted from the tenant root"
// (such as agents/poet/AGENT.md).
//
// Note that it only covers **syntax**, not **permissions**: "may the '/' prefix be used at all" and "which paths under the tenant root may
// be reached with it" are two different questions, and the latter is decided by the caller's gate - see checkReadOwn / checkWrite (the
// super agent, whole tenant) and checkWriteOwn (a non-super agent may write only its own private skill directories) in
// service/agent/tools/gate.go; for the non-super agent's read side see checkReadOwn (its whole definition directory is readable).
//
// It returns (relative path, whether it is counted from the tenant root, error). The path still refuses drive letters, .. and traversal,
// and "must stay inside this tenant" is enforced by Context.StoragePath / Context.TenantPath.
func SafePathEx(p string, allowRootAddr bool) (string, bool, error) {
	raw := strings.TrimSpace(filepath.ToSlash(p))
	fromRoot := allowRootAddr && strings.HasPrefix(raw, "/")
	if fromRoot {
		raw = strings.TrimPrefix(raw, "/")
	}
	clean, err := safePath(raw)
	if err != nil {
		return "", false, err
	}
	return clean, fromRoot, nil
}

// ============================================================================
// Version domain (release snapshots)
// ============================================================================
//
// The version domain and the draft domain are strictly separate and siblings:
//
//	draft       tenants/<schema>/{agents|skills|tools}/<name>
//	version n   tenants/<schema>/versions/{agents|skills|tools}/<name>/v<n>
//
// For "a directory with no version number is the draft" to hold, the draft directory must contain draft content only. Putting version
// directories inside an entity directory would force every "walk the draft" operation (dirty comparison / packaging / loading / the file
// tree) to carry an extra exclusion list, and the snapshots would nest inside each other (v3 containing v2 containing v1).
//
// It deliberately has **no leading dot** (it is not called .versions): this is real data and must be carried along by backups / rsync
// normally; a leading dot makes many backup configurations skip it silently. Unreachability does not rest on hiding:
//   - write side: gate.go's path switch has no versions branch, so it falls through to default and is refused;
//   - read side: the super agent's reads bypass checkReadOwn, so gate.go's resolveRead carries an explicit assertion (isVersionDomain) -
//     version history may contain secrets that were rolled back and the bodies of deprecated skills;
//   - the roots of the file API are entity directories, which cannot address this either.
//
// A private skill has no version number of its own: its directory sits inside the agent root, so its version number is the owning agent's,
// and its content lives in versions/agents/<agent>/v<n>/skills/<skill>/ (see docs/terminology.md for details).

// VersionsSubdir is the directory name of the version domain under the tenant root.
const VersionsSubdir = "versions"

// VersionKind is the entity type in the version domain (and the subdirectory name under versions/).
type VersionKind string

const (
	VersionAgents VersionKind = "agents"
	VersionSkills VersionKind = "skills" // tenant-level skills only; a private skill follows its owning agent
	VersionTools  VersionKind = "tools"
)

// VersionsRoot returns the root of a tenant's version domain: tenants/<schema>/versions.
func VersionsRoot(schema string) string {
	return path.Join(TenantsSubdir, schema, VersionsSubdir)
}

// VersionEntityKey returns the directory holding one entity's versions: tenants/<schema>/versions/<kind>/<name>.
//
// The key is the entity's **current name** (not its id): a rename has to move the whole version directory along (see MoveEntityStorage),
// and the name stored on a version row is only the historical fact "what it was called when released", never used to build a path.
func VersionEntityKey(schema string, kind VersionKind, name string) string {
	return path.Join(VersionsRoot(schema), string(kind), name)
}

// VersionKey returns the directory of one entity's version n: versions/<kind>/<name>/v<n>.
func VersionKey(schema string, kind VersionKind, name string, n int) string {
	return path.Join(VersionEntityKey(schema, kind, name), "v"+strconv.Itoa(n))
}

// ResolveVersionsRoot returns the host absolute path of the version domain.
func ResolveVersionsRoot(rootDir, schema string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, VersionsSubdir)
}

// ResolveVersionDir returns the host absolute path of one entity's version n (a sandbox mount source / for reading the disk directly).
func ResolveVersionDir(rootDir, schema string, kind VersionKind, name string, n int) string {
	return filepath.Join(ResolveVersionsRoot(rootDir, schema), string(kind), name, "v"+strconv.Itoa(n))
}

// EntityDraftKey returns the storage key of an entity's draft directory.
//
// It only serves the version-related moves / cleanups, which is why both kind and name are validated: the consequence of these calls is
// deleting or moving a whole directory tree, so a ".." or an unknown kind slipping in is a data incident.
func EntityDraftKey(schema string, kind VersionKind, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid %s name: %q", string(kind), name)
	}
	switch kind {
	case VersionAgents:
		return AgentsKey(schema, name), nil
	case VersionSkills:
		return path.Join(SkillsKey(schema), name), nil
	case VersionTools:
		return path.Join(ToolsKey(schema), name), nil
	}
	return "", fmt.Errorf("unknown version kind: %q", string(kind))
}

// MoveEntityStorage moves "version domain + draft domain" together when an entity is renamed.
//
// The order is version domain first, draft second, rolling back the half already moved on failure:
//   - version domain moved but draft not -> history stays under the old name, and building paths from the current name misses it;
//   - draft moved but version domain not -> the same, just in the other direction.
//
// Caller contract: when this returns an error, do **not** commit the rename in the database (the disk is the content, the database is the
// index).
func MoveEntityStorage(st storage.Storage, schema string, kind VersionKind, oldName, newName string) error {
	if st == nil || oldName == newName {
		return nil
	}
	draftOld, err := EntityDraftKey(schema, kind, oldName)
	if err != nil {
		return err
	}
	draftNew, err := EntityDraftKey(schema, kind, newName)
	if err != nil {
		return err
	}

	ctx := context.Background()
	verOld := VersionEntityKey(schema, kind, oldName)
	verNew := VersionEntityKey(schema, kind, newName)
	if err := st.Rename(ctx, verOld, verNew); err != nil {
		return fmt.Errorf("failed to move the version directory (%s -> %s): %w", verOld, verNew, err)
	}
	if err := st.Rename(ctx, draftOld, draftNew); err != nil {
		// Roll the version domain back: better to have both sides on the old name than half new and half old
		if rerr := st.Rename(ctx, verNew, verOld); rerr != nil {
			return fmt.Errorf("failed to move the draft directory: %w; rolling the version directory back also failed (%s -> %s): %v",
				err, verNew, verOld, rerr)
		}
		return fmt.Errorf("failed to move the draft directory (the version directory has been rolled back): %w", err)
	}
	return nil
}

// RenamePrivateSkillDirs renames a private skill's subdirectory in **every agent version directory** when the skill is renamed.
//
// A private skill has no version number of its own, and its directory lives in versions/agents/<owner>/v<n>/skills/<name>:
//   - moving only the draft leaves directories still carrying the old name inside the current version, and a production run building the
//     path from the current name cannot read it;
//   - historical versions have to move too: after rolling back to an old version the skill's name in the database is still the new one,
//     and the read path builds from the current name as well.
//
// When a version directory does not contain that skill, Rename is idempotent (a missing source returns nil).
// Any failing step rolls back what was already moved - better to leave everything on the old name than half new and half old.
func RenamePrivateSkillDirs(st storage.Storage, schema, ownerName, oldName, newName string) error {
	if st == nil || ownerName == "" || oldName == "" || oldName == newName {
		return nil
	}
	for _, n := range []string{ownerName, oldName, newName} {
		if !ValidName(n) {
			return fmt.Errorf("invalid skill/agent name %q", n)
		}
	}

	ctx := context.Background()
	base := VersionEntityKey(schema, VersionAgents, ownerName)
	// An agent that was never released has no versions/<owner> directory at all: List returns ENOENT on a filesystem, but that only means
	// "there is no history to move", not a failure (on S3 an empty prefix returns an empty list anyway - do not let the two diverge).
	if ok, eerr := st.Exists(ctx, base); eerr != nil {
		return fmt.Errorf("failed to check the agent version directory (%s): %w", base, eerr)
	} else if !ok {
		return nil
	}
	entries, err := st.List(ctx, base)
	if err != nil {
		return fmt.Errorf("failed to list the agent version directory (%s): %w", base, err)
	}

	moved := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir || !strings.HasPrefix(e.Name, "v") {
			continue
		}
		skillsDir := path.Join(base, e.Name, AgentSkillsSubdir)
		from := path.Join(skillsDir, oldName)
		to := path.Join(skillsDir, newName)
		if err := st.Rename(ctx, from, to); err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				_ = st.Rename(ctx, path.Join(moved[i], newName), path.Join(moved[i], oldName))
			}
			return fmt.Errorf("failed to move the private skill version directory (%s -> %s): %w", from, to, err)
		}
		moved = append(moved, skillsDir)
	}
	return nil
}

// DeleteEntityStorage clears both "version domain + draft domain" prefixes when an entity is deleted (idempotent; a missing one returns
// nil).
//
// Private skills do **not** go through here: a private skill has no version directory of its own and its content is deleted together with
// the owning agent's version directory.
func DeleteEntityStorage(st storage.Storage, schema string, kind VersionKind, name string) error {
	if st == nil {
		return nil
	}
	draft, err := EntityDraftKey(schema, kind, name)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := st.DeletePrefix(ctx, VersionEntityKey(schema, kind, name)); err != nil {
		return fmt.Errorf("failed to clear the version directory: %w", err)
	}
	if err := st.DeletePrefix(ctx, draft); err != nil {
		return fmt.Errorf("failed to clear the draft directory: %w", err)
	}
	return nil
}

// safePath validates and normalises a path relative to the working directory, ruling out traversal and absolute paths.
// It returns a slash-separated relative path (an empty string means the working directory root).
func safePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	// Windows drive letter (such as C:)
	if len(p) >= 2 && p[1] == ':' {
		return "", errors.New("absolute path not allowed")
	}
	slash := filepath.ToSlash(p)
	// Absolute path
	if strings.HasPrefix(slash, "/") {
		return "", errors.New("absolute path not allowed")
	}
	// Refuse .. segment by segment
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".." {
			return "", errors.New("path escapes workspace")
		}
	}
	clean := path.Clean(slash)
	if clean == "." {
		return "", nil
	}
	return clean, nil
}
