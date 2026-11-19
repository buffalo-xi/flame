// Package assetpack 跨租户资源分发的**纯逻辑层**：文件打包 / 还原、快照脱敏、命名建议。
//
// 为什么是「复制」（而不是跨租户引用）见 docs/asset-sharing.md §1：实体住在各自的
// schema 里、文件住在 tenants/<schema>/… 下，跨租户读在两处都被拦住，所以只能拷。
//
// 本包只做与 DB、HTTP 无关的部分，便于单测；调用方（admin / platform）负责：
//   - 读实体行、生成新 uuid、重建授权与绑定（DB 层）
//   - 快照行的状态机与审核（platform.shared_assets）
//
// 一次分发涉及三个存储位置（真源见 shared/paths）：
//
//	源      tenants/<source>/skills|tools|agents/<name>/        归源租户
//	快照    platform-assets/<assetID>/<build>/                归平台
//	安装    tenants/<target>/skills|tools|agents/<name>/        归目标租户
//
// 快照是分发的唯一真源：安装只从快照拷，不回源租户读 —— 源租户停用/删除后，
// 已经上架出去的内容仍然装得上。三处都是同一棵目录树的副本，路径相对关系原样保留。
package assetpack

import (
	"errors"
	"fmt"
)

// Kind 可分发单元的类型。
type Kind string

const (
	KindSkill    Kind = "skill"
	KindTool     Kind = "custom_tool"
	KindAgent    Kind = "agent"
	KindWorkflow Kind = "workflow"
)

// Valid 是否是已知类型（与 platform.shared_assets 的 CHECK 约束同口径）。
func (k Kind) Valid() bool {
	switch k {
	case KindSkill, KindTool, KindAgent, KindWorkflow:
		return true
	}
	return false
}

// EntryFile 该类型的入口文件（快照里必须存在，否则视为内容不完整）。
//
// 自定义工具没有固定入口：入口由 TOOL.json 的 entry 字段决定，调用方读出来传进
// PackFiles 的 entry 参数，这里返回空串。
//
// 工作流的定义在 {schema}.flows 一行 JSONB 里，盘上没有实体目录；上架时整行序列化成
// FLOW.json 当入口文件，借此复用同一条文件管线（见 workflow.go）。
func (k Kind) EntryFile() string {
	switch k {
	case KindSkill:
		return "SKILL.md"
	case KindAgent:
		return "AGENT.md"
	case KindWorkflow:
		return WorkflowFileName
	}
	return ""
}

// Limits 打包时的体积闸（防止一个资产把平台存储撑爆）。
//
// 缺省值按「定义域 = 文本 + 少量资源文件」定：SKILL.md / AGENT.md / 脚本 / 模板本体是几十 KB
// 量级，但技能常带图标、字体、截图或演示数据，所以单文件与合计给到能容纳这些资源的量级。
// 闸仍然必要：它挡的是「把数据集或构建产物整目录塞进来」，而不是限制正常资产。
type Limits struct {
	MaxFiles    int   // 文件数上限
	MaxFileSize int64 // 单文件上限（字节）
	MaxTotal    int64 // 合计上限（字节）
}

// DefaultLimits 缺省闸值：2000 个文件 / 单文件 64 MiB / 合计 128 MiB。
func DefaultLimits() Limits {
	return Limits{MaxFiles: 2000, MaxFileSize: 64 << 20, MaxTotal: 128 << 20}
}

// withDefaults 把零值字段补成缺省，避免误传 Limits{} 变成「一切都不许」。
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxFiles <= 0 {
		l.MaxFiles = d.MaxFiles
	}
	if l.MaxFileSize <= 0 {
		l.MaxFileSize = d.MaxFileSize
	}
	if l.MaxTotal <= 0 {
		l.MaxTotal = d.MaxTotal
	}
	return l
}

// FileEntry 快照清单里的一项。
//
// 只记 path / size / sha256，**不存内容**：内容在 storage 里，清单进 DB（manifest.files），
// 用于安装前校验、审核页展示「这个资产带了什么」、以及还原时发现快照被改动。
type FileEntry struct {
	Path   string `json:"path"`             // 相对资产根目录，/ 分隔
	Size   int64  `json:"size"`             // 字节
	SHA256 string `json:"sha256,omitempty"` // 十六进制摘要
}

// ToolDep 对工具授权的一项依赖声明（**只声明，不解析成实体**）。
//
// Ref 是授权表里的字符串口径（`custom:<name>` / 组名 / 内置工具名），安装时按它去
// 目标租户里找；找不到就丢弃并记 warning —— 不能因为一个工具缺失就让整个资产装不上。
type ToolDep struct {
	Type string `json:"type"` // group | tool
	Ref  string `json:"ref"`
	// GrantKind 只对 agent 有意义（required / allow / deny）：技能的工具依赖只表达
	// 「需要」，没有授权种类。空值按 allow 处理。
	GrantKind string `json:"grant_kind,omitempty"`
}

// PackOptions 打包选项（零值 = 整棵目录全都打）。
type PackOptions struct {
	// SkipDirs 跳过的子目录（相对路径前缀，如 "skills"）。
	SkipDirs []string

	// Trees 额外挂进包里的目录树：智能体 / 技能依赖的**自定义工具本体**不在自己的目录下，
	// 但仍要跟着同一个包走，否则装到目标租户只有一条指向不存在工具的授权行。
	Trees []PackTree
}

// BundledToolsPrefix 包内附件前缀：files/_deps/custom_tools/<name>/**。
//
// 下载下来看是可见的（不是隐藏目录），安装时按这个前缀把附件摘出来装成自定义工具，
// 不会混进 agent / skill 自己的目录。
const BundledToolsPrefix = "_deps/custom_tools"

// PackTree 一份附加内容：把 SrcDir 整棵拷进包里 DstPrefix 下，清单路径也带该前缀。
type PackTree struct {
	SrcDir    string
	DstPrefix string
	// Required 为 true 时 SrcDir 不存在直接报错 —— 依赖声明了要带本体，缺了就不该静默放过。
	Required bool
}

// Deps 资产的依赖闭包声明。
type Deps struct {
	Tools         []ToolDep `json:"tools,omitempty"`          // skill / agent 的授权依赖
	Skills        []string  `json:"skills,omitempty"`         // agent 绑定的租户级技能名
	PrivateSkills []string  `json:"private_skills,omitempty"` // agent 自带的私有技能名
	// Agents agent 之间「我能调用谁」的依赖（只有名字，授权行不随包走）。
	// agent_relations 是 uuid 关系、跨租户无法重映射，所以不迁移；
	// 但「这个智能体要调谁」是装上之前就该核对的信息 —— 没有它，只有运行期 call_agent
	// 失败才发现目标租户里压根没有那个智能体（见 admin/share 的 Inspect）。
	Agents   []string `json:"agents,omitempty"`
	Requires []string `json:"requires,omitempty"` // 能力要求，如 sandbox.exec
	// GlobalVars 资产引用到的全局变量名（去重、升序）。
	//
	// 包里只有这些**名字**，没有值 —— 这是「导出导入不把值写死在资产里」的关键：
	// 装到目标租户后由对方在「设置 → 全局变量」填一次，所有引用同时生效。
	// 装上之前用它做预检：缺哪个变量直接列出来，而不是等运行期节点失败才发现。
	// 旧包没有这个字段 = 无依赖（与"确实不引用"同义，不做额外推断）。
	GlobalVars []string `json:"global_vars,omitempty"`
	Egress     bool     `json:"egress,omitempty"` // 是否需要出网
}

// Manifest 快照的元信息（进 DB 的 manifest 列）。
type Manifest struct {
	Kind  Kind        `json:"kind"`
	Name  string      `json:"name"`            // 源实体名（安装时可改名）
	Entry string      `json:"entry,omitempty"` // 入口文件（skill/agent 固定，tool 取自 TOOL.json）
	Deps  Deps        `json:"deps"`
	Files []FileEntry `json:"files"`
	// Spec manifest / payload 的语义版本（见 ManifestSpec）。老包没有这个字段 = 1。
	Spec int `json:"spec,omitempty"`
	// Digest 内容指纹（见 StampManifest）：判断「两份包是不是同一份内容」。
	// 老包没有 = 空串，此时只做「未知」处理，不参与任何强制判断。
	Digest string `json:"digest,omitempty"`
}

// TotalSize 清单里所有文件合计字节数。
func (m Manifest) TotalSize() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// 打包/还原时的错误。调用方用 errors.Is 判，映射成 400（资产内容不合法）而不是 500。
var (
	// ErrNoFiles 源目录不存在或为空。
	ErrNoFiles = errors.New("the asset directory is empty")
	// ErrTooManyFiles 文件数超限。
	ErrTooManyFiles = errors.New("too many files")
	// ErrFileTooLarge 单文件超限。
	ErrFileTooLarge = errors.New("a single file is too large")
	// ErrTotalTooLarge 合计体积超限。
	ErrTotalTooLarge = errors.New("the total size is too large")
	// ErrMissingEntry 缺入口文件。
	ErrMissingEntry = errors.New("the entry file is missing")
	// ErrChecksum 快照内容与清单不符（存储被改动或写入不完整）。
	ErrChecksum = errors.New("the snapshot does not match the manifest")
)

// warn 造一条给前端看的提示（与契约 warnings、目录 issues 同一口径：中文、指名到文件/字段）。
func warn(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
