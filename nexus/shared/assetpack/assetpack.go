// Package assetpack is the **pure logic layer** of cross-tenant asset distribution: packing and
// restoring files, redacting snapshots, suggesting names.
//
// Why a copy instead of a cross-tenant reference: see docs/asset-sharing.md §1 — entities live
// in their own schema and files live under tenants/<schema>/…, and cross-tenant reads are
// blocked in both places, so copying is the only option.
//
// This package covers only the parts that do not touch the DB or HTTP, which keeps it easy to
// unit test; the callers (admin / platform) are responsible for:
//   - reading the entity row, generating new uuids, rebuilding grants and bindings (DB layer)
//   - the state machine and review of the snapshot row (platform.shared_assets)
//
// One distribution touches three storage locations (the source of truth is shared/paths):
//
//	source   tenants/<source>/skills|tools|agents/<name>/       owned by the source tenant
//	snapshot platform-assets/<assetID>/<build>/                owned by the platform
//	install  tenants/<target>/skills|tools|agents/<name>/       owned by the target tenant
//
// The snapshot is the sole source for distribution: install copies from the snapshot and never
// reads the source tenant, so content already listed stays installable after the source tenant
// is disabled or deleted. All three locations hold copies of the same tree with the relative
// layout preserved verbatim.
package assetpack

import (
	"errors"
	"fmt"
)

// Kind is the type of a distributable unit.
type Kind string

const (
	KindSkill    Kind = "skill"
	KindTool     Kind = "custom_tool"
	KindAgent    Kind = "agent"
	KindWorkflow Kind = "workflow"
)

// Valid reports whether this is a known kind (same set as the CHECK constraint on
// platform.shared_assets).
func (k Kind) Valid() bool {
	switch k {
	case KindSkill, KindTool, KindAgent, KindWorkflow:
		return true
	}
	return false
}

// EntryFile is the entry file of this kind (it must exist in the snapshot, otherwise the content
// counts as incomplete).
//
// A custom tool has no fixed entry: TOOL.json's entry field decides, the caller reads it out and
// passes it as PackFiles' entry argument, and this method returns the empty string.
//
// A workflow's definition lives in one JSONB row of {schema}.flows and has no directory on disk;
// listing serialises the whole row into FLOW.json as the entry file so the same file pipeline can
// be reused (see workflow.go).
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

// Limits is the size gate applied while packing (it keeps one asset from filling platform
// storage).
//
// The defaults assume "definition domain = text plus a few resource files": SKILL.md / AGENT.md /
// scripts / templates are tens of KB, but a skill often ships icons, fonts, screenshots or demo
// data, so the per-file and total ceilings are sized to hold those. The gate is still necessary:
// it stops "dump a dataset or a build output tree in here", not normal assets.
type Limits struct {
	MaxFiles    int   // ceiling on the file count
	MaxFileSize int64 // ceiling per file, in bytes
	MaxTotal    int64 // ceiling on the total, in bytes
}

// DefaultLimits is the default gate: 2000 files / 64 MiB per file / 128 MiB in total.
func DefaultLimits() Limits {
	return Limits{MaxFiles: 2000, MaxFileSize: 64 << 20, MaxTotal: 128 << 20}
}

// withDefaults fills zero-valued fields with the defaults, so an accidental Limits{} does not
// mean "nothing is allowed".
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

// FileEntry is one entry of the snapshot manifest.
//
// It records path / size / sha256 and **no content**: the content lives in storage, the manifest
// goes into the DB (manifest.files), and it is used to validate before install, to show "what
// does this asset carry" on the review page, and to detect a tampered snapshot on restore.
type FileEntry struct {
	Path   string `json:"path"`             // relative to the asset root, /-separated
	Size   int64  `json:"size"`             // bytes
	SHA256 string `json:"sha256,omitempty"` // hex digest
}

// ToolDep declares one dependency on a tool grant (**it only declares, it does not resolve into
// an entity**).
//
// Ref is the string form used by the grant table (`custom:<name>` / a group name / a built-in
// tool name); install looks it up in the target tenant and, when it is missing, drops it and
// records a warning — a missing tool must not make the whole asset uninstallable.
type ToolDep struct {
	Type string `json:"type"` // group | tool
	Ref  string `json:"ref"`
	// GrantKind only means something for agents (required / allow / deny): a skill's tool
	// dependencies express "needs" and have no grant kind. An empty value is treated as allow.
	GrantKind string `json:"grant_kind,omitempty"`
}

// PackOptions controls packing (the zero value packs the whole tree).
type PackOptions struct {
	// SkipDirs are subdirectories to skip (path prefixes such as "skills").
	SkipDirs []string

	// Trees are extra directory trees hung into the package: the **custom tool bodies** an agent
	// or skill depends on do not live in its own directory, yet they have to travel in the same
	// package, otherwise the target tenant ends up with a grant row pointing at a tool that does
	// not exist.
	Trees []PackTree
}

// BundledToolsPrefix is the in-package prefix for attachments: files/_deps/custom_tools/<name>/**.
//
// It is visible when the package is downloaded (not a hidden directory); install pulls these
// attachments out by prefix and installs them as custom tools, so they never mix into the agent's
// or skill's own directory.
const BundledToolsPrefix = "_deps/custom_tools"

// PackTree is one attached tree: copy SrcDir wholesale into DstPrefix inside the package, with the
// manifest paths carrying that prefix.
type PackTree struct {
	SrcDir    string
	DstPrefix string
	// Required makes a missing SrcDir an error — the dependency declared that the body travels
	// with the asset, so its absence must not pass silently.
	Required bool
}

// Deps declares an asset's dependency closure.
type Deps struct {
	Tools         []ToolDep `json:"tools,omitempty"`          // grant dependencies of a skill / agent
	Skills        []string  `json:"skills,omitempty"`         // tenant-level skill names bound to an agent
	PrivateSkills []string  `json:"private_skills,omitempty"` // private skill names carried by an agent
	// Agents are the "who can I call" dependencies between agents (names only; grant rows do not
	// travel with the package). agent_relations are uuid relations that cannot be remapped across
	// tenants, so they are not migrated; but "which agents does this one call" is information to
	// check before install — without it, a missing agent in the target tenant only shows up when
	// call_agent fails at runtime (see Inspect under admin/share).
	Agents   []string `json:"agents,omitempty"`
	Requires []string `json:"requires,omitempty"` // capability requirements, e.g. sandbox.exec
	// GlobalVars are the global variable names the asset references (deduplicated, sorted).
	//
	// The package holds only these **names**, no values — that is the point of "export and import
	// never hard-code a value into the asset": after install the target tenant fills them in once
	// under Settings → Global variables and every reference takes effect. Before install they
	// drive a pre-check that lists the missing variables instead of waiting for a node to fail at
	// runtime. An old package without this field means no dependencies (identical to "really
	// references none"), with no further inference.
	GlobalVars []string `json:"global_vars,omitempty"`
	Egress     bool     `json:"egress,omitempty"` // whether outbound network access is needed
}

// Manifest is the snapshot's metadata (stored in the manifest column).
type Manifest struct {
	Kind  Kind        `json:"kind"`
	Name  string      `json:"name"`            // source entity name (renameable at install time)
	Entry string      `json:"entry,omitempty"` // entry file (fixed for skill/agent, taken from TOOL.json for tool)
	Deps  Deps        `json:"deps"`
	Files []FileEntry `json:"files"`
	// Spec is the semantic version of the manifest / payload (see ManifestSpec). Old packages
	// without it are version 1.
	Spec int `json:"spec,omitempty"`
	// Digest is the content fingerprint (see StampManifest): it answers "are these two packages
	// the same content". Old packages have the empty string, which means "unknown" and takes part
	// in no forced decision.
	Digest string `json:"digest,omitempty"`
}

// TotalSize is the summed size of every file in the manifest.
func (m Manifest) TotalSize() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// Errors from packing and restoring. Callers test them with errors.Is and map them to 400
// (invalid asset content) rather than 500.
var (
	// ErrNoFiles means the source directory is missing or empty.
	ErrNoFiles = errors.New("the asset directory is empty")
	// ErrTooManyFiles means the file count is over the ceiling.
	ErrTooManyFiles = errors.New("too many files")
	// ErrFileTooLarge means one file is over the ceiling.
	ErrFileTooLarge = errors.New("a single file is too large")
	// ErrTotalTooLarge means the total size is over the ceiling.
	ErrTotalTooLarge = errors.New("the total size is too large")
	// ErrMissingEntry means the entry file is absent.
	ErrMissingEntry = errors.New("the entry file is missing")
	// ErrChecksum means the snapshot content disagrees with the manifest (storage was modified or
	// written incompletely).
	ErrChecksum = errors.New("the snapshot does not match the manifest")
)

// warn builds a diagnostic for the frontend (same conventions as contract warnings and directory
// issues: it names the offending file or field).
func warn(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
