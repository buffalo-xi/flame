package provision

import (
	"io/fs"
	"strings"
	"testing"
	"unicode"

	"flame/nexus/shared/models"
)

// systemEntityDirs are the definition-domain directories of the "system entities" inside the template.
//
// Derived from protectedAgents / protectedSkills in shared/models/protected.go - deliberately not a second literal list:
// AGENTS.md makes that Go file the single source of truth, and a copied list would silently stop covering a newly added
// system entity.
// These files ship with the product and go straight into the model context, so they must be English (see AGENTS.md at the
// repository root, "system definition-domain files and built-in tool copy must be English"). Add them here when adding to the
// protected list.
func systemEntityDirs() []string {
	dirs := make([]string, 0, len(models.ProtectedAgentNames())+len(models.ProtectedSkillNames()))
	for _, name := range models.ProtectedAgentNames() {
		dirs = append(dirs, "template/files/agents/"+name)
	}
	for _, name := range models.ProtectedSkillNames() {
		dirs = append(dirs, "template/files/skills/"+name)
	}
	return dirs
}

// TestSystemEntityFilesAreEnglish turns "system entity files must be English" into a CI gate.
//
// It only checks the template (the embed FS), not the tenant directories under tenants/: tenant-built agents / skills follow
// the user's language, and historical version snapshots must not be rewritten either. What this test guards against is Chinese
// creeping back in during a bulk translation or a later change (the prompt language changes and model behaviour forks with the
// locale).
func TestSystemEntityFilesAreEnglish(t *testing.T) {
	matched := 0
	dirs := systemEntityDirs()
	err := fs.WalkDir(templateAssets, "template/files", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !underAnyDir(path, dirs) {
			return nil
		}
		matched++
		b, err := fs.ReadFile(templateAssets, path)
		if err != nil {
			return err
		}
		line := 1
		for _, r := range string(b) {
			if r == '\n' {
				line++
				continue
			}
			if unicode.Is(unicode.Han, r) {
				t.Errorf("%s:%d contains Chinese: the definition-domain files of system entities must be English (see AGENTS.md)", path, line)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk the template: %v", err)
	}
	// A renamed / deleted directory must not pass silently, or the gate is worthless
	if matched == 0 {
		t.Fatalf("matched no system entity files; check whether systemEntityDirs is out of date")
	}
}

func underAnyDir(path string, dirs []string) bool {
	for _, d := range dirs {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
}
