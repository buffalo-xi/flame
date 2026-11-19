package toolfile

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
)

// Entry is one scanned tool directory (the directory exists and the contract is usable).
type Entry struct {
	Name string
	Dir  string
	File *models.ToolFile
}

// ScanIssue is a directory or record that "looks like a tool but cannot be used as one".
//
// Why not skip silently: code sitting in the directory while the platform shows nothing is the shape of this
// step that gets complained about most (previously only what was in the table counted as a tool, and an orphan
// on disk produced no error at all). Now at least it can be listed.
type ScanIssue struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// Scan walks a tenant's tools/ directory and returns the usable tools plus one issue per problem.
//
// Only "a directory plus a valid TOOL.json" counts: the contract is declarative, and tenant code is never
// executed just to read a contract (a plugin may do that, because a plugin is written by the platform; here it
// may not).
func Scan(rootDir, schema string) ([]Entry, []ScanIssue) {
	if err := checkRoot(rootDir); err != nil {
		return nil, []ScanIssue{{Path: schema, Error: err.Error()}}
	}
	base := paths.ResolveToolsDir(rootDir, schema)
	dirs, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // tools/ was never created = no tools, not an error
		}
		return nil, []ScanIssue{{Path: base, Error: err.Error()}}
	}

	out := make([]Entry, 0, len(dirs))
	var issues []ScanIssue
	for _, d := range dirs {
		name := d.Name()
		if !d.IsDir() || strings.HasPrefix(name, ".") {
			continue // intermediate files such as .tmp and hidden directories do not take part
		}
		f, rerr := Read(rootDir, schema, name)
		if rerr != nil {
			issues = append(issues, ScanIssue{Path: name, Error: "failed to parse TOOL.json: " + rerr.Error()})
			continue
		}
		if f == nil {
			issues = append(issues, ScanIssue{Path: name, Error: "TOOL.json is missing (the tool contract cannot be determined)"})
			continue
		}
		if verr := f.Validate(name); verr != nil {
			issues = append(issues, ScanIssue{Path: name, Error: verr.Error()})
			continue
		}
		out = append(out, Entry{Name: name, Dir: filepath.Join(base, name), File: f})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Slice(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
	return out, issues
}
