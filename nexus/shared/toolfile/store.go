// Package toolfile reads and writes the custom tool contract file (TOOL.json).
//
// Division of labour:
//   - models.ToolFile: the pure data structure and the "contract -> table row" semantics (no filesystem)
//   - this package: the on-disk layout, atomic writes, and the degraded behaviour when a read fails
//
// A failed read always **degrades to the table** (Source="table" plus a warning) and never makes a tool
// disappear because of one bad file: the contract is a description, and a tool still runs without one; turning
// it into "unavailable" only makes people believe the tool is broken.
package toolfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
)

// Contract is the contract's source and warnings: the admin console shows "where does this contract come
// from" from it.
type Contract struct {
	Source   string   `json:"source"`             // file: from TOOL.json; table: fell back to the table definition
	Warnings []string `json:"warnings,omitempty"` // parse failure / name mismatch / missing entry and so on
}

// Dir is the host absolute path of a tool directory: {root}/tenants/{schema}/tools/{name}.
func Dir(rootDir, schema, name string) string {
	return filepath.Join(paths.ResolveToolsDir(rootDir, schema), name)
}

// Path is the host absolute path of the contract file.
func Path(rootDir, schema, name string) string {
	return filepath.Join(Dir(rootDir, schema, name), models.ToolFileName)
}

// Read reads the contract file. A missing file returns (nil, nil) — during the rollout a missing file is a
// normal state, not an error.
func Read(rootDir, schema, name string) (*models.ToolFile, error) {
	if err := checkRoot(rootDir); err != nil {
		return nil, err
	}
	if err := safeName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(Path(rootDir, schema, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return models.ParseToolFile(data)
}

// Write writes the contract file atomically: write a .tmp in the same directory, then rename.
//
// It does not overwrite in place: the execution side may be reading, and half a JSON document makes it decide
// the parse failed and fall back to the table — a one-second window in which the tool description suddenly
// turns stale is harder to trace than a failed write.
func Write(rootDir, schema, name string, f *models.ToolFile) error {
	if err := checkRoot(rootDir); err != nil {
		return err
	}
	if err := safeName(name); err != nil {
		return err
	}
	return WriteAt(Dir(rootDir, schema, name), f)
}

// ReadAt reads the contract file in **the given directory** (symmetric with WriteAt; a real run reads its
// definition from the version directory).
// A missing file returns (nil, nil): the caller decides whether "the version has no contract" is an error or
// something to skip.
func ReadAt(dir string) (*models.ToolFile, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("toolfile: empty dir")
	}
	data, err := os.ReadFile(filepath.Join(dir, models.ToolFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return models.ParseToolFile(data)
}

// WriteAt writes the contract into **the given directory** (for version snapshots: the directory is not the
// draft root, but the write conventions have to match).
//
// Why a snapshot needs it: a tool version must carry a valid TOOL.json of its own (the definition follows the
// file, and a missing file degrades to the table row). When the draft lacks one or it is broken, the release
// has to write the merged view into the version directory — and only the snapshot: the draft is left alone,
// because releasing should not quietly edit what the user is still working on.
func WriteAt(dir string, f *models.ToolFile) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("toolfile: empty dir")
	}
	data, err := models.MarshalToolFile(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, models.ToolFileName)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// WriteOf builds the contract from a table row and writes it (the single write path shared by the admin console
// and the meta tool).
//
// Both places that write a row (the admin API and the super agent's meta tool) must call it: the read path
// prefers the file, so writing only the table means the next read puts the old contract back and the user sees
// "saved successfully but nothing changed".
func WriteOf(rootDir, schema string, t *models.CustomTool) error {
	return Write(rootDir, schema, t.Name, models.ToolFileOf(t))
}

// Load applies the contract on disk to the table row and reports where it came from.
//
// Three degradations (none blocking): a parse failure, a name that disagrees with the directory, or an entry
// outside the tool directory all fall back to the table definition; a missing entry file is only a warning —
// execution (a trial run especially) gives a more precise error.
func Load(rootDir, schema string, t *models.CustomTool) Contract {
	f, err := Read(rootDir, schema, t.Name)
	if err != nil {
		return Contract{
			Source:   "table",
			Warnings: []string{fmt.Sprintf("failed to parse TOOL.json, fell back to the table definition: %v", err)},
		}
	}
	if f == nil {
		return Contract{Source: "table"}
	}
	if verr := f.Validate(t.Name); verr != nil {
		return Contract{
			Source:   "table",
			Warnings: []string{fmt.Sprintf("%v (fell back to the table definition; fix TOOL.json)", verr)},
		}
	}
	f.Apply(t)

	c := Contract{Source: "file"}
	entry := filepath.Join(Dir(rootDir, schema, t.Name), filepath.FromSlash(t.Entry))
	if _, serr := os.Stat(entry); serr != nil {
		c.Warnings = append(c.Warnings, fmt.Sprintf("the entry file declared by TOOL.json does not exist: %s", t.Entry))
	}
	return c
}

// EnsureAll writes contracts for tools that "have a table row but no file" (a migration entry point,
// idempotent).
//
// It is only called when the admin console opens the list: the read path never writes a file, so that "looking
// at it" cannot change the disk.
func EnsureAll(rootDir, schema string, list []models.CustomTool) (written int, errs []string) {
	for i := range list {
		t := &list[i]
		exists, err := hasFile(rootDir, schema, t.Name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", t.Name, err))
			continue
		}
		if exists {
			continue
		}
		if err := Write(rootDir, schema, t.Name, models.ToolFileOf(t)); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", t.Name, err))
			continue
		}
		written++
	}
	return written, errs
}

func hasFile(rootDir, schema, name string) (bool, error) {
	if err := safeName(name); err != nil {
		return false, err
	}
	st, err := os.Stat(Path(rootDir, schema, name))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !st.IsDir(), nil
}

// checkRoot requires the storage root to be configured.
//
// An empty rootDir turns Path into a **relative** path such as "tenants/t0/tools/...": a read would find a file
// of the same name in the working directory and a write would build a fake directory tree under the process
// cwd — far harder to trace than an outright error, so it is rejected here explicitly.
func checkRoot(rootDir string) error {
	if strings.TrimSpace(rootDir) == "" {
		return fmt.Errorf("the storage root is not initialised (agent.root_dir)")
	}
	return nil
}

// safeName requires a name to be exactly one directory level: separators and .. are rejected (otherwise Path
// can escape tools/).
//
// The table has a unique constraint on name, but the disk-writing functions are also called directly by the
// admin console and migrations, so this is the backstop.
func safeName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return fmt.Errorf("the tool name is empty")
	}
	if n != name || strings.ContainsAny(n, `/\`) || n == "." || n == ".." {
		return fmt.Errorf("invalid tool name %q (must be a single directory name)", name)
	}
	return nil
}
