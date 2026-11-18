// Package paths 租户数据在存储里的目录约定（真源）。
//
// 定义域（跨用户、跨会话共用，由元工具维护）：
//
//	agent       ：tenants/<schema>/agents/<agent>
//	租户共享技能 ：tenants/<schema>/skills/<skill>
//	私有技能     ：tenants/<schema>/agents/<agent>/skills/<skill>
//
// 运行域（agent 干活时的场地，随会话或一次调用产生与销毁；即「工作区」）：
//
//	用户级文件   ：tenants/<schema>/users/<user>/files
//	会话工作目录  ：tenants/<schema>/users/<user>/sessions/<session>
//	无会话临时场地：tenants/<schema>/users/<user>/runs/<run>（兜底 tenants/<schema>/runs/<run>）
//
// 两域严格分开的理由见 docs/workspace-layout-and-supervisor-power.md §1。
//
// 包名刻意不叫 workspace：那个词现在专指运行域工作目录（映射进沙箱 /workspace 的那个），
// 而本包管的是**全部**目录约定（定义域 + 运行域），故取名 paths。
//
// 目录曾由 workspace/（agent 定义）、shared-skills/（租户共享技能）更名为
// agents/、skills/；旧布局的一次性迁移已执行完毕，迁移代码已从仓库摘除。
//
// 本包原为 service/agent/tools/workspace.go，现挪到中立位置，原因：
//   - 平台侧开通租户时也要按同一约定寻址 tenants/<schema>/{agents,skills,...}，
//     而平台侧不允许 import nexus/service/**
//   - 控制台管理端（nexus/admin/*）本来就在用它（SafePath/AgentsKey/ValidName），
//     属于"管理端反向依赖 service 层"，一并扶正
//
// service/agent/tools/paths.go 保留同名符号做转发，现有调用点无需改动。
// 只依赖 shared/storage 与标准库，无业务语义。
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

// TenantsSubdir 租户数据在存储根下的固定子目录名。
const TenantsSubdir = "tenants"

// AgentsSubdir 定义域：每个 agent 一个目录（原 "workspace"）。
const AgentsSubdir = "agents"

// AgentSkillsSubdir agent 私有技能子目录名（位于 agent 目录内）。
//
// 注意它与 TenantSkillsSubdir 的**取值相同、层级不同**：
// agents/<agent>/skills/<skill> 与 skills/<skill>。两者都由它方的父目录区分，
// 不要因为名字相近而混用。
const AgentSkillsSubdir = "skills"

// TenantSkillsSubdir 定义域：租户级共享技能目录名（原 "shared-skills"，
// 即 agent_id 为 NULL 的 skill）。
const TenantSkillsSubdir = "skills"

// ToolsSubdir 定义域：租户级自定义工具目录名（tools/<toolName>/<entry>）。
//
// 与 TenantSkillsSubdir 平级：脚本是"定义域"内容（和 SKILL.md 一样放盘上、可被
// 文件工具编辑、可进 git），元数据才在库里（{schema}.custom_tools）。
const ToolsSubdir = "tools"

// SkillEntryFile skill 的入口文件名（技能目录下唯一有运行时约定的文件）。
// 真源在此：运行时按它读技能正文；管理端接口（admin/skills/files.go）与文件工具
// 都禁止**删除**它（允许改内容），否则会留下「登记在库但没有正文」的空技能。
const SkillEntryFile = "SKILL.md"

// AgentEntryFile agent 的入口文件名（定义域根下唯一有运行时约定的文件）。
// 真源在此：运行时 loadSystemPrompt / create_agent / read_agent 都按它读，
// 管理端建 agent 与前端文件树也以它为准。
//
// **刻意不做成 DB 字段**（原 agents.entry_file 列已删除）：入口名固定，
// 做成可配置只会造成「配了却不生效」的误导（对照 SkillEntryFile）。
const AgentEntryFile = "AGENT.md"

// UsersSubdir 运行域：用户目录。
const UsersSubdir = "users"

// FilesSubdir 用户级共享文件（上传件 / 跨会话保留的产物）。
const FilesSubdir = "files"

// SessionsSubdir 会话工作目录（文件工具的默认根 = exec 的 cwd）。
const SessionsSubdir = "sessions"

// RunsSubdir 无 session 调用的临时工作目录（随一次调用产生与销毁）。
const RunsSubdir = "runs"

// namePattern agent/skill 名字只允许字母、数字、横线（不能以横线开头）。
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)

// ValidName 校验 agent/skill 名字合法。
func ValidName(name string) bool {
	return name != "" && len(name) <= 64 && namePattern.MatchString(name)
}

// AgentsKey 返回 agent 定义域根目录相对存储根的逻辑路径（/ 分隔，供 Storage 调用）。
//
// 名字刻意避开 "Workspace"：那个词现在专指运行域工作目录（session / runs），与定义域
// 是两回事（对照 Context.Workspace 与 Context.AgentRoot）。
func AgentsKey(schema, name string) string {
	return path.Join(TenantsSubdir, schema, AgentsSubdir, name)
}

// ResolveAgentDir 返回 agent 定义域本地绝对路径：{root}/tenants/{schema}/agents/{name}。
func ResolveAgentDir(rootDir, schema, name string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, AgentsSubdir, name)
}

// SkillsKey 返回租户级共享 skill 根目录相对存储根的逻辑路径（/ 分隔，供 Storage 调用）。
func SkillsKey(schema string) string {
	return path.Join(TenantsSubdir, schema, TenantSkillsSubdir)
}

// SkillPath 返回某个租户级 skill 的 SKILL.md 相对存储根的路径。
func SkillPath(schema, name string) string {
	return path.Join(SkillsKey(schema), name, SkillEntryFile)
}

// AgentSkillPath 返回某个 agent 私有 skill 的 SKILL.md 相对存储根的路径：
// tenants/{schema}/agents/{agentName}/skills/{name}/SKILL.md
func AgentSkillPath(schema, agentName, name string) string {
	return path.Join(AgentsKey(schema, agentName), AgentSkillsSubdir, name, SkillEntryFile)
}

// ResolveSkillsDir 返回租户级共享 skill 根目录的本地绝对路径。
func ResolveSkillsDir(rootDir, schema string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, TenantSkillsSubdir)
}

// ToolsKey 返回租户级自定义工具根目录相对存储根的逻辑路径（供 Storage 调用）。
func ToolsKey(schema string) string {
	return path.Join(TenantsSubdir, schema, ToolsSubdir)
}

// PlatformAssetsSubdir 平台级资源分发的**快照**根目录（不属于任何租户）。
//
// 一次上架 = 一份快照：platform-assets/<assetID>/<build>/<相对路径>，
// 内容是源租户某个定义域目录（skills/<name>、tools/<name>、agents/<name>）的镜像。
// 安装时从这里拷到目标租户，**不回源租户读** —— 跨租户读被 Context.StoragePath 的
// 租户内断言拦住，且源租户停用/删除后就装不了了。
//
// 这个前缀刻意放在 tenants/ 之外：
//   - 不在任何沙箱挂载点内（/workspace、/agent、tools/ 都不覆盖它）；
//   - safePath 拒绝 ..，租户侧的文件工具天然到不了这里。
//
// 方案见 docs/asset-sharing.md。
const PlatformAssetsSubdir = "platform-assets"

// PlatformAssetKey 返回某资产某快照快照目录的相对 key：platform-assets/<assetID>/<build>。
func PlatformAssetKey(assetID string, build int) string {
	return path.Join(PlatformAssetsSubdir, assetID, strconv.Itoa(build))
}

// ResolveToolsDir 返回租户级自定义工具根目录的本地绝对路径（沙箱挂载源）。
func ResolveToolsDir(rootDir, schema string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, ToolsSubdir)
}

// UserFilesKey 返回用户级文件目录相对存储根的路径（上传件 / 跨会话产物）。
func UserFilesKey(schema, userID string) string {
	return path.Join(TenantsSubdir, schema, UsersSubdir, userID, FilesSubdir)
}

// UserSessionKey 返回会话工作目录相对存储根的路径（文件工具默认根、exec 的 cwd）。
func UserSessionKey(schema, userID, sessionID string) string {
	return path.Join(TenantsSubdir, schema, UsersSubdir, userID, SessionsSubdir, sessionID)
}

// RunsKey 返回无 session 调用的临时工作目录相对存储根的路径（租户级兜底，无 user 时用）。
func RunsKey(schema, runID string) string {
	return path.Join(TenantsSubdir, schema, RunsSubdir, runID)
}

// UserRunsKey 返回某用户下无 session 调用临时目录相对存储根的路径。
//
// 有 user 时优先用它（运行域按 user 隔离）；只有连 user 都拿不到时才退到租户级 RunsKey。
func UserRunsKey(schema, userID, runID string) string {
	return path.Join(TenantsSubdir, schema, UsersSubdir, userID, RunsSubdir, runID)
}

// ResolveUserSessionDir 返回会话工作目录的本地绝对路径。
//
// 它是「文件工具默认根」与「exec 挂 /workspace 的宿主源」在本地存储下的落地。
func ResolveUserSessionDir(rootDir, schema, userID, sessionID string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, UsersSubdir, userID, SessionsSubdir, sessionID)
}

// ResolveUserRunsDir 返回某用户下无 session 调用临时目录的本地绝对路径。
func ResolveUserRunsDir(rootDir, schema, userID, runID string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, UsersSubdir, userID, RunsSubdir, runID)
}

// ResolveRunsDir 返回租户级无 session 调用临时目录的本地绝对路径（无 user 时兜底）。
func ResolveRunsDir(rootDir, schema, runID string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, RunsSubdir, runID)
}

// RenameAgentDir 改名时同步重命名 agent 定义域目录（源不存在则跳过）。
// 走 storage 抽象，local / nfs / s3 通用。
func RenameAgentDir(st storage.Storage, schema, oldName, newName string) error {
	if st == nil || oldName == newName {
		return nil
	}
	return st.Rename(context.Background(), AgentsKey(schema, oldName), AgentsKey(schema, newName))
}

// RemoveAgentDir 删除某个 agent 的定义域目录（不存在则跳过）。
// 走 storage 抽象，local / nfs / s3 通用。
func RemoveAgentDir(st storage.Storage, schema, name string) error {
	if st == nil {
		return nil
	}
	return st.DeletePrefix(context.Background(), AgentsKey(schema, name))
}

// SafePath 校验并规范化相对运行域工作目录的路径（safePath 的导出快照）。
// 供跨包使用，如管理端的定义域文件接口、平台侧开通租户。
func SafePath(p string) (string, error) { return safePath(p) }

// SafePathEx 校验并规范化路径，allowRootAddr 为 true 时允许以 "/" 开头
// 表示"从租户根算"（如 agents/poet/AGENT.md）。
//
// 注意这里只管**语法**，不管**权限**：「能不能用 "/" 前缀」与「能以 "/" 前缀访问
// 租户根下的哪些路径」是两件事，后者由调用方的闸门决定 —— 见
// service/agent/tools/gate.go 的 checkReadOwn / checkWrite（超级智能体全租户）与
// checkWriteOwn（非超级智能体仅自己的私有 skill 目录可写）；非超级智能体读侧见 checkReadOwn
// （自己的定义目录整棵可读）。
//
// 返回 (相对路径, 是否从租户根算, error)。路径仍拒绝盘符、.. 与穿透，
// 而"必须落在本租户内"由 Context.StoragePath / Context.TenantPath 的断言兜底。
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
// 版本域（发布快照）
// ============================================================================
//
// 版本域与草稿域严格分开、平级：
//
//	草稿      tenants/<schema>/{agents|skills|tools}/<name>
//	第 n 版   tenants/<schema>/versions/{agents|skills|tools}/<name>/v<n>
//
// 「没版本号的目录就是草稿」这句话要成立，草稿目录就必须只装草稿内容。把版本目录
// 放进实体目录里，每个「走一遍草稿」的操作（dirty 比对 / 打包 / 载入 / 文件树）
// 都要额外记一条排除表，而且快照自己会套娃（v3 里套 v2 套 v1）。
//
// 刻意**不加点开头**（不叫 .versions）：它是正式数据，必须被备份 / rsync 正常带走；
// 点开头会让不少备份配置静默跳过它。可达性不靠隐藏：
//   - 写侧：gate.go 的路径 switch 没有 versions 分支，落 default 直接拒绝；
//   - 读侧：超级智能体的读不经 checkReadOwn，所以 gate.go 的 resolveRead 里有一条
//     显式断言（isVersionDomain）—— 版本历史可能含已回滚掉的口令与废弃技能正文；
//   - 文件接口的根都是实体目录，也寻址不到这里。
//
// 私有技能没有独立版本号：它的目录在 agent 根目录之内，版本号就是所属 agent 的，
// 内容落在 versions/agents/<agent>/v<n>/skills/<skill>/（详见 docs/terminology.md）。

// VersionsSubdir 版本域在租户根下的目录名。
const VersionsSubdir = "versions"

// VersionKind 版本域的实体类型（同时是 versions/ 下的子目录名）。
type VersionKind string

const (
	VersionAgents VersionKind = "agents"
	VersionSkills VersionKind = "skills" // 只含租户级技能；私有技能随所属 agent
	VersionTools  VersionKind = "tools"
)

// VersionsRoot 返回租户版本域的根：tenants/<schema>/versions。
func VersionsRoot(schema string) string {
	return path.Join(TenantsSubdir, schema, VersionsSubdir)
}

// VersionEntityKey 返回某实体版本集合的目录：tenants/<schema>/versions/<kind>/<name>。
//
// 键是实体的**当前名字**（不是 id）：改名时必须把整棵版本目录一起搬（见 MoveEntityStorage），
// 版本行里的 name 只是「发布时叫什么」的历史事实，不能用它拼路径。
func VersionEntityKey(schema string, kind VersionKind, name string) string {
	return path.Join(VersionsRoot(schema), string(kind), name)
}

// VersionKey 返回某实体第 n 版的目录：versions/<kind>/<name>/v<n>。
func VersionKey(schema string, kind VersionKind, name string, n int) string {
	return path.Join(VersionEntityKey(schema, kind, name), "v"+strconv.Itoa(n))
}

// ResolveVersionsRoot 返回版本域的宿主绝对路径。
func ResolveVersionsRoot(rootDir, schema string) string {
	return filepath.Join(rootDir, TenantsSubdir, schema, VersionsSubdir)
}

// ResolveVersionDir 返回某实体第 n 版的宿主绝对路径（沙箱挂载源 / 直接读盘用）。
func ResolveVersionDir(rootDir, schema string, kind VersionKind, name string, n int) string {
	return filepath.Join(ResolveVersionsRoot(rootDir, schema), string(kind), name, "v"+strconv.Itoa(n))
}

// EntityDraftKey 返回实体草稿目录的存储相对 key。
//
// 只服务版本相关的搬运/清理，所以对 kind 与 name 都做校验：这些调用的后果是
// 删/搬一整棵目录，拿一个 ".." 或未知名进来就是数据事故。
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

// MoveEntityStorage 实体改名时把「版本域 + 草稿域」一起搬走。
//
// 顺序是版本域在前、草稿在后，失败时回滚已搬的那一半：
//   - 版本域搬不动而草稿搬了 → 历史版本挂在旧名下，按当前 name 拼路径就找不到了；
//   - 草稿搬不动而版本域搬了 → 同上，只是反方向。
//
// 调用方约定：本函数返回错误时**不要**再提交库里的改名（盘是内容，库是索引）。
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
		return fmt.Errorf("同步版本目录失败（%s → %s）: %w", verOld, verNew, err)
	}
	if err := st.Rename(ctx, draftOld, draftNew); err != nil {
		// 版本域回滚：宁可两边都停在旧名，也不要一半新一半旧
		if rerr := st.Rename(ctx, verNew, verOld); rerr != nil {
			return fmt.Errorf("搬草稿目录失败: %w；版本目录回滚也失败（%s → %s）: %v",
				err, verNew, verOld, rerr)
		}
		return fmt.Errorf("搬草稿目录失败（版本目录已回滚）: %w", err)
	}
	return nil
}

// RenamePrivateSkillDirs 私有技能改名时，把它在**每个 agent 版本目录**里的子目录一起改名。
//
// 私有技能没有独立版本号，目录落在 versions/agents/<owner>/v<n>/skills/<name>：
//   - 只搬草稿会留下「当前版本里还是旧名」的目录，正式运行按当前 name 拼路径 → 读不到；
//   - 历史版本也要搬：回滚到旧版本后库里的技能名仍是新名，读路径同样按新名拼。
//
// 某个版本目录里没有这个技能时 Rename 是幂等的（源不存在返回 nil）。
// 任何一步失败都回滚已搬的部分 —— 宁可全部停在旧名，也不要一半新一半旧。
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
	// agent 从未发布过就没有 versions/<owner> 这棵目录：List 在文件系统上会返回 ENOENT，
	// 但那只是「没有历史版本要搬」，不是失败（S3 上前缀为空本就返回空列表，别让两端分叉）。
	if ok, eerr := st.Exists(ctx, base); eerr != nil {
		return fmt.Errorf("检查 agent 版本目录失败（%s）: %w", base, eerr)
	} else if !ok {
		return nil
	}
	entries, err := st.List(ctx, base)
	if err != nil {
		return fmt.Errorf("列举 agent 版本目录失败（%s）: %w", base, err)
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
			return fmt.Errorf("搬私有技能版本目录失败（%s → %s）: %w", from, to, err)
		}
		moved = append(moved, skillsDir)
	}
	return nil
}

// DeleteEntityStorage 删除实体时清掉「版本域 + 草稿域」两个前缀（幂等，不存在返回 nil）。
//
// 私有技能**不**走这里：它没有独立版本目录，内容随所属 agent 的版本目录一起删。
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
		return fmt.Errorf("清理版本目录失败: %w", err)
	}
	if err := st.DeletePrefix(ctx, draft); err != nil {
		return fmt.Errorf("清理草稿目录失败: %w", err)
	}
	return nil
}

// safePath 校验并规范化相对工作目录的路径，杜绝穿越与绝对路径。
// 返回正斜杠分隔的相对路径（空串表示工作目录根）。
func safePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	// Windows 盘符（如 C:）
	if len(p) >= 2 && p[1] == ':' {
		return "", errors.New("absolute path not allowed")
	}
	slash := filepath.ToSlash(p)
	// 绝对路径
	if strings.HasPrefix(slash, "/") {
		return "", errors.New("absolute path not allowed")
	}
	// 逐段拒绝 ..
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
