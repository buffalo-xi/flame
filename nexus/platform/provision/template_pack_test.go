package provision

import (
	"io/fs"
	"sort"
	"strings"
	"testing"

	"flame/nexus/shared/models"
)

// TestTemplatePackMatchesProtectedList ties together the three places that must agree: the protection list in
// shared/models/protected.go, the files/ tree a new tenant gets, and the seed rows.
//
// Drift in either direction produces a broken tenant: "protected but not packaged" gives a delete-protection list naming a
// skill that does not exist, and "packaged but not protected" ships a preinstalled skill that the tenant may delete with no
// way to get it back. Both are silent today, because the only other test in this package (template_language_test.go) only
// looks at files that already exist.
func TestTemplatePackMatchesProtectedList(t *testing.T) {
	seeds := readSeed(t)

	skills := models.ProtectedSkillNames()
	if len(skills) == 0 {
		t.Fatal("protectedSkills is empty; the protection gate would be meaningless")
	}
	for _, name := range skills {
		assertTemplateFile(t, "template/files/skills/"+name+"/SKILL.md")
		assertSeedRow(t, seeds, "skills", name)
	}

	agents := models.ProtectedAgentNames()
	if len(agents) == 0 {
		t.Fatal("protectedAgents is empty; the protection gate would be meaningless")
	}
	for _, name := range agents {
		assertTemplateFile(t, "template/files/agents/"+name+"/AGENT.md")
		assertSeedRow(t, seeds, "agents", name)
	}

	// Reverse direction: the pack must not ship anything that is not on the protection list. A tenant's own extra skills
	// live under data/tenants/<schema>/, never in the template.
	assertExactDirs(t, "template/files/skills", skills)
	assertExactDirs(t, "template/files/agents", agents)
}

func readSeed(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(templateAssets, "template/seed.sql")
	if err != nil {
		t.Fatalf("failed to read template/seed.sql: %v", err)
	}
	return string(b)
}

func assertTemplateFile(t *testing.T, path string) {
	t.Helper()
	if _, err := fs.Stat(templateAssets, path); err != nil {
		t.Errorf("%s is missing: the protected list and the template pack are out of sync", path)
	}
}

func assertSeedRow(t *testing.T, seeds, table, name string) {
	t.Helper()
	if !strings.Contains(seeds, "INSERT INTO "+table) {
		t.Errorf("seed.sql has no INSERT INTO %s; the preinstalled assets would not exist in a new tenant", table)
		return
	}
	// seed.sql writes the name as a quoted literal, e.g. ('a0000000-...', 'skill-authoring', ...
	if !strings.Contains(seeds, "'"+name+"'") {
		t.Errorf("seed.sql has no row for %q: it is protected but never seeded", name)
	}
}

func assertExactDirs(t *testing.T, root string, want []string) {
	t.Helper()
	entries, err := fs.ReadDir(templateAssets, root)
	if err != nil {
		t.Fatalf("failed to read %s: %v", root, err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			got = append(got, e.Name())
		}
	}
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		t.Errorf("%s ships [%s], want exactly the protected list [%s]",
			root, strings.Join(got, ", "), strings.Join(sorted, ", "))
	}
}
