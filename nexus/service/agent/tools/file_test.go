package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flame/nexus/shared/quota"
	"flame/nexus/shared/storage"
)

// TestQuotaFreedMeasuresFilesAndDirectories: delete_file has to report the bytes it gave back, so the counter does not keep
// the workspace looking full after a cleanup.
func TestQuotaFreedMeasuresFilesAndDirectories(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()
	for p, n := range map[string]int{"ws/s/file.txt": 120, "ws/s/dir/a.txt": 30, "ws/s/dir/sub/b.txt": 12} {
		if err := st.WriteFile(ctx, p, make([]byte, n)); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	if got := quotaFreed(ctx, st, "ws/s/file.txt"); got != 120 {
		t.Fatalf("quotaFreed(file) = %d, want 120", got)
	}
	if got := quotaFreed(ctx, st, "ws/s/dir"); got != 42 {
		t.Fatalf("quotaFreed(dir) = %d, want 42", got)
	}
	if got := quotaFreed(ctx, st, "ws/s/missing"); got != 0 {
		t.Fatalf("quotaFreed(missing) = %d, want 0", got)
	}
}

// TestQuotaExceededErrorKeepsCause: the model-visible message must name the files the pre-check already deleted, while the
// error must stay unwrappable (handlers rely on errors.As).
func TestQuotaExceededErrorKeepsCause(t *testing.T) {
	base := &quota.ExceededError{Scope: "session", Metric: "bytes", Used: 2048, Limit: 1024}
	wrapped := quotaExceededError(base, []string{"old.txt", "notes/a.md"})
	if !errors.Is(wrapped, base) {
		t.Fatalf("errors.Is = false, want the cause to survive wrapping")
	}
	for _, want := range []string{"old.txt", "notes/a.md", "full"} {
		if !strings.Contains(wrapped.Error(), want) {
			t.Fatalf("message %q does not mention %q", wrapped.Error(), want)
		}
	}

	// No evictions: the original error is returned untouched
	if got := quotaExceededError(base, nil); got != error(base) {
		t.Fatalf("expected the original error, got %v", got)
	}
}

// stubAgents lets the cross-domain write gate (checkWrite) read the target agent's status; delete_file only uses Get.
type stubAgents struct{ status int }

func (s stubAgents) Create(context.Context, string, string, string, bool) (string, error) {
	return "", nil
}

func (s stubAgents) List(context.Context) ([]AgentInfo, error) { return nil, nil }

func (s stubAgents) Exists(context.Context, string) (bool, error) { return true, nil }

func (s stubAgents) Get(context.Context, string) (*AgentInfo, error) {
	return &AgentInfo{ID: "aid", Name: "poet", Status: s.status}, nil
}

func (s stubAgents) Update(context.Context, string, string, string) (string, error) { return "", nil }

func (s stubAgents) SetUnrestricted(context.Context, string, bool) error { return nil }

func (s stubAgents) SetOutputLanguage(context.Context, string, string) error { return nil }

func (s stubAgents) SetSupervisor(context.Context, string, bool) error { return nil }

func (s stubAgents) Delete(context.Context, string) error { return nil }

// newFileCtx builds the smallest usable Context: Storage points at a temp directory and the Agents stub returns a fixed status.
func newFileCtx(t *testing.T, agentName string, status int) (*Context, storage.Storage) {
	t.Helper()
	st := storage.NewFilesystemStorage(t.TempDir())
	return &Context{
		Schema:    "t0",
		AgentName: agentName,
		AgentID:   "self",
		// The identity criterion is an attribute, not the name: using "main" for the super agent in a test means setting it
		// explicitly
		IsSupervisor: agentName == "main",
		UserID:       "u1",
		WorkKey:      "tenants/t0/users/u1/sessions/s1",
		Storage:      st,
		Agents:       stubAgents{status: status},
	}, st
}

func runDelete(t *testing.T, c *Context, p string) error {
	t.Helper()
	_, err := (&fileDeleteTool{}).Execute(context.Background(), c, map[string]any{"path": p})
	return err
}

func mustWrite(t *testing.T, st storage.Storage, key string) {
	t.Helper()
	if err := st.WriteFile(context.Background(), key, []byte("x")); err != nil {
		t.Fatal(err)
	}
}

func exists(t *testing.T, st storage.Storage, key string) bool {
	t.Helper()
	ex, err := st.Exists(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// Inside the workspace: deleting a directory removes it recursively (a non-super agent taking the ordinary workspace path).
func TestDeleteFileRecursesInWorkspace(t *testing.T) {
	c, st := newFileCtx(t, "joker", 1)
	deep := c.StoragePath("sub/deep/a.md")
	mustWrite(t, st, deep)

	if err := runDelete(t, c, "sub"); err != nil {
		t.Fatalf("delete sub = %v, want nil", err)
	}
	if exists(t, st, deep) {
		t.Error("sub/deep/a.md should be deleted together with the directory")
	}
	if exists(t, st, c.StoragePath("sub")) {
		t.Error("the sub directory should be gone")
	}
}

// Inside the workspace: a single file is still deletable (regression).
func TestDeleteFileSingleFile(t *testing.T) {
	c, st := newFileCtx(t, "joker", 1)
	key := c.StoragePath("a.md")
	mustWrite(t, st, key)

	if err := runDelete(t, c, "a.md"); err != nil {
		t.Fatalf("delete a.md = %v, want nil", err)
	}
	if exists(t, st, key) {
		t.Error("a.md should be gone")
	}
}

// The super agent crossing domains: deleting a subtree inside an entity removes the whole tree.
func TestDeleteFileRecursesAcrossRoot(t *testing.T) {
	c, st := newFileCtx(t, "main", 1)
	deep := "tenants/t0/agents/poet/sub/deep/a.md"
	mustWrite(t, st, deep)

	if err := runDelete(t, c, "/agents/poet/sub"); err != nil {
		t.Fatalf("super agent deleting a subtree recursively = %v, want nil", err)
	}
	if exists(t, st, deep) {
		t.Error("the subtree content should have been deleted recursively")
	}
}

// The super agent must not delete an entity's root directory either (recursive everywhere is not the same as crossing D6's
// boundary).
func TestDeleteFileRejectsEntityRoot(t *testing.T) {
	c, st := newFileCtx(t, "main", 1)
	agentMd := "tenants/t0/agents/poet/AGENT.md"
	mustWrite(t, st, agentMd)

	if err := runDelete(t, c, "/agents/poet"); err == nil {
		t.Fatal("deleting an agent's entity root should be refused")
	}
	if !exists(t, st, agentMd) {
		t.Error("a refused delete must have no side effect")
	}
}

// A deprecated target is always refused (the status gate outranks the delete action).
func TestDeleteFileRejectsDeprecatedTarget(t *testing.T) {
	c, st := newFileCtx(t, "main", 3)
	deep := "tenants/t0/agents/poet/sub/a.md"
	mustWrite(t, st, deep)

	if err := runDelete(t, c, "/agents/poet/sub"); err == nil {
		t.Fatal("a deprecated target should refuse the delete")
	}
	if !exists(t, st, deep) {
		t.Error("a refused delete must have no side effect")
	}
}

// The skills container layer (agents/{a}/skills) is not a writable target; the error says a skill name is missing.
func TestDeleteFileRejectsSkillsContainer(t *testing.T) {
	c, st := newFileCtx(t, "main", 1)
	mustWrite(t, st, "tenants/t0/agents/poet/skills/x/SKILL.md")

	if err := runDelete(t, c, "/agents/poet/skills"); err == nil {
		t.Fatal("the skills container layer should be refused")
	}
}

// Deleting a missing path is an idempotent success (the same rule as S3; the contract is pinned so it cannot regress to strict
// mode).
func TestDeleteFileMissingPathIsIdempotent(t *testing.T) {
	c, _ := newFileCtx(t, "joker", 1)

	if err := runDelete(t, c, "nope.md"); err != nil {
		t.Fatalf("deleting a missing path should succeed idempotently, got %v", err)
	}
}

// SKILL.md is a skill's entry file and cannot be deleted on its own under root addressing: doing so would leave an empty skill
// registered in the database with no body (which degrades silently to empty content at run time). The same rule as the admin
// side's isSkillEntry.
func TestDeleteFileRejectsSkillEntryOwn(t *testing.T) {
	c := newOwnSkillCtx(t, "joker", 1, map[string]*SkillInfo{
		"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: 1},
	})
	entry := c.TenantPath("agents/joker/skills/foo/SKILL.md")
	mustWrite(t, c.Storage, entry)

	if err := runDelete(t, c, "/agents/joker/skills/foo/SKILL.md"); err == nil {
		t.Fatal("deleting SKILL.md on its own should be refused")
	}
	if !exists(t, c.Storage, entry) {
		t.Error("a refused delete must have no side effect")
	}

	// Attached files are unaffected (that is exactly what was opened up here)
	extra := c.TenantPath("agents/joker/skills/foo/docs/a.md")
	mustWrite(t, c.Storage, extra)
	if err := runDelete(t, c, "/agents/joker/skills/foo/docs/a.md"); err != nil {
		t.Errorf("an attached file should be deletable, got %v", err)
	}
	if exists(t, c.Storage, extra) {
		t.Error("the attached file should be gone")
	}
}

// The super agent is protected by the SKILL.md rule too - all three entry points (admin side / super agent / non-super agent)
// share one criterion.
func TestDeleteFileRejectsSkillEntryForSupervisor(t *testing.T) {
	c := newOwnSkillCtx(t, "main", 1, nil)
	c.Agents = stubAgentsByName{byName: map[string]*AgentInfo{
		"main": {ID: "main-id", Name: "main", Status: 1},
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
	}}
	c.Skills = stubSkillsForWrite{byOwner: map[string]map[string]*SkillInfo{
		"poet-id": {"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: 1}},
	}}
	entry := c.TenantPath("agents/poet/skills/foo/SKILL.md")
	mustWrite(t, c.Storage, entry)

	if err := runDelete(t, c, "/agents/poet/skills/foo/SKILL.md"); err == nil {
		t.Fatal("the super agent deleting SKILL.md on its own should be refused too")
	}
	if !exists(t, c.Storage, entry) {
		t.Error("a refused delete must have no side effect")
	}

	// A whole skill directory is still deletable (the meta-tool scenario is elsewhere, but a subtree inside an entity must pass)
	sub := c.TenantPath("agents/poet/skills/foo/docs")
	mustWrite(t, c.Storage, c.TenantPath("agents/poet/skills/foo/docs/a.md"))
	if err := runDelete(t, c, "/agents/poet/skills/foo/docs"); err != nil {
		t.Errorf("a subtree inside an entity should be deletable, got %v", err)
	}
	if exists(t, c.Storage, sub) {
		t.Error("the subtree should have been deleted recursively")
	}
}
