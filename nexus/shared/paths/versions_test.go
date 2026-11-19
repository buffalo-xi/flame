package paths_test

import (
	"context"
	"testing"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// A version directory's key is the entity's **current name**, not its id: once the path shape is fixed, rename moves have to follow it.
func TestVersionKeys(t *testing.T) {
	if got, want := paths.VersionsRoot("t0"), "tenants/t0/versions"; got != want {
		t.Fatalf("VersionsRoot = %q, want %q", got, want)
	}
	if got, want := paths.VersionEntityKey("t0", paths.VersionTools, "calc"), "tenants/t0/versions/tools/calc"; got != want {
		t.Fatalf("VersionEntityKey = %q, want %q", got, want)
	}
	if got, want := paths.VersionKey("t0", paths.VersionAgents, "poet", 3), "tenants/t0/versions/agents/poet/v3"; got != want {
		t.Fatalf("VersionKey = %q, want %q", got, want)
	}
	// A private skill has no version number of its own: its content lives in the agent's version directory (this only pins the shape)
	if got, want := paths.VersionKey("t0", paths.VersionAgents, "poet", 2), "tenants/t0/versions/agents/poet/v2"; got != want {
		t.Fatalf("agent version dir = %q, want %q", got, want)
	}
}

// A rename must move "version domain + draft domain" together under the new name, leaving no debris under the old one.
func TestMoveEntityStorageMovesBothDomains(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()
	mustWrite(t, st, "tenants/t0/agents/poet/AGENT.md", "# poet")
	mustWrite(t, st, "tenants/t0/agents/poet/skills/verse/SKILL.md", "# verse")
	mustWrite(t, st, "tenants/t0/versions/agents/poet/v1/AGENT.md", "# v1")
	mustWrite(t, st, "tenants/t0/versions/agents/poet/v1/skills/verse/SKILL.md", "# v1 verse")

	if err := paths.MoveEntityStorage(st, "t0", paths.VersionAgents, "poet", "bard"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	for _, key := range []string{
		"tenants/t0/agents/bard/AGENT.md",
		"tenants/t0/agents/bard/skills/verse/SKILL.md",
		"tenants/t0/versions/agents/bard/v1/AGENT.md",
		"tenants/t0/versions/agents/bard/v1/skills/verse/SKILL.md",
	} {
		if ex, err := st.Exists(ctx, key); err != nil || !ex {
			t.Fatalf("%s should exist under the new name (err=%v)", key, err)
		}
	}
	for _, key := range []string{
		"tenants/t0/agents/poet",
		"tenants/t0/versions/agents/poet",
	} {
		if ex, _ := st.Exists(ctx, key); ex {
			t.Fatalf("%s must not be left behind", key)
		}
	}
}

// Both prefixes have to be cleared when an entity is recreated under the same name / deleted, or the new entity inherits the old history.
func TestDeleteEntityStorageClearsBothDomains(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()
	mustWrite(t, st, "tenants/t0/tools/calc/TOOL.json", "{}")
	mustWrite(t, st, "tenants/t0/versions/tools/calc/v1/TOOL.json", "{}")

	if err := paths.DeleteEntityStorage(st, "t0", paths.VersionTools, "calc"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	for _, key := range []string{"tenants/t0/tools/calc", "tenants/t0/versions/tools/calc"} {
		if ex, _ := st.Exists(ctx, key); ex {
			t.Fatalf("%s should have been cleared", key)
		}
	}

	// Idempotent: deleting again when the directory is gone is not an error
	if err := paths.DeleteEntityStorage(st, "t0", paths.VersionTools, "calc"); err != nil {
		t.Fatalf("a repeated delete should be idempotent: %v", err)
	}
}

// An invalid name is always refused: the consequence of these calls is moving or deleting a whole directory tree, which cannot rest on the
// caller's discipline.
func TestEntityStorageRejectsInvalidName(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	if err := paths.MoveEntityStorage(st, "t0", paths.VersionAgents, "..", "x"); err == nil {
		t.Fatal("MoveEntityStorage should refuse an invalid name")
	}
	if err := paths.DeleteEntityStorage(st, "t0", paths.VersionAgents, ".."); err == nil {
		t.Fatal("DeleteEntityStorage should refuse an invalid name")
	}
	if _, err := paths.EntityDraftKey("t0", paths.VersionKind("nope"), "x"); err == nil {
		t.Fatal("EntityDraftKey should refuse an unknown kind")
	}
}

func mustWrite(t *testing.T, st storage.Storage, key, content string) {
	t.Helper()
	if err := st.WriteFile(context.Background(), key, []byte(content)); err != nil {
		t.Fatalf("failed to write %s: %v", key, err)
	}
}
