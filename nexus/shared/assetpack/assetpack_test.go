package assetpack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"flame/nexus/shared/storage"
)

func newStorage(t *testing.T) *storage.FilesystemStorage {
	t.Helper()
	return storage.NewFilesystemStorage(t.TempDir())
}

func mustWrite(t *testing.T, st storage.Storage, key, content string) {
	t.Helper()
	if err := st.WriteFile(context.Background(), key, []byte(content)); err != nil {
		t.Fatalf("failed to write %s: %v", key, err)
	}
}

func mustRead(t *testing.T, st storage.Storage, key string) string {
	t.Helper()
	b, err := st.ReadFile(context.Background(), key)
	if err != nil {
		t.Fatalf("failed to read %s: %v", key, err)
	}
	return string(b)
}

// Packing: the whole tree is mirrored, the manifest is sorted by path and the sha256 digests match.
func TestPackFilesMirrorsTreeAndHashes(t *testing.T) {
	st := newStorage(t)
	ctx := context.Background()
	mustWrite(t, st, "tenants/t1/skills/demo/SKILL.md", "# demo\n")
	mustWrite(t, st, "tenants/t1/skills/demo/scripts/run.py", "print(1)\n")

	files, err := PackFiles(ctx, st, "tenants/t1/skills/demo", "platform-assets/a1/1", "SKILL.md", DefaultLimits())
	if err != nil {
		t.Fatalf("PackFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("file count = %d, want 2", len(files))
	}
	if files[0].Path != "SKILL.md" || files[1].Path != "scripts/run.py" {
		t.Fatalf("manifest order is wrong: %+v", files)
	}
	sum := sha256.Sum256([]byte("# demo\n"))
	if files[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 mismatch: %s", files[0].SHA256)
	}
	if got := mustRead(t, st, "platform-assets/a1/1/scripts/run.py"); got != "print(1)\n" {
		t.Fatalf("snapshot content = %q", got)
	}
}

func TestPackFilesRejectsEmptyAndMissingEntry(t *testing.T) {
	ctx := context.Background()

	t.Run("missing directory", func(t *testing.T) {
		st := newStorage(t)
		_, err := PackFiles(ctx, st, "tenants/t1/skills/none", "platform-assets/a1/1", "", DefaultLimits())
		if !errors.Is(err, ErrNoFiles) {
			t.Fatalf("want ErrNoFiles, got %v", err)
		}
	})

	t.Run("missing entry file", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "tenants/t1/skills/demo/a.txt", "a")
		_, err := PackFiles(ctx, st, "tenants/t1/skills/demo", "platform-assets/a1/1", "SKILL.md", DefaultLimits())
		if !errors.Is(err, ErrMissingEntry) {
			t.Fatalf("want ErrMissingEntry, got %v", err)
		}
	})
}

func TestPackFilesLimits(t *testing.T) {
	ctx := context.Background()

	t.Run("too many files", func(t *testing.T) {
		st := newStorage(t)
		for _, n := range []string{"a", "b", "c"} {
			mustWrite(t, st, "tenants/t1/tools/x/"+n, "1")
		}
		lim := Limits{MaxFiles: 2, MaxFileSize: 1 << 10, MaxTotal: 1 << 10}
		_, err := PackFiles(ctx, st, "tenants/t1/tools/x", "platform-assets/a1/1", "", lim)
		if !errors.Is(err, ErrTooManyFiles) {
			t.Fatalf("want ErrTooManyFiles, got %v", err)
		}
	})

	t.Run("file too large", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "tenants/t1/tools/x/big", "abcd")
		lim := Limits{MaxFiles: 10, MaxFileSize: 3, MaxTotal: 1 << 10}
		_, err := PackFiles(ctx, st, "tenants/t1/tools/x", "platform-assets/a1/1", "", lim)
		if !errors.Is(err, ErrFileTooLarge) {
			t.Fatalf("want ErrFileTooLarge, got %v", err)
		}
	})

	t.Run("total too large", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "tenants/t1/tools/x/a", "123")
		mustWrite(t, st, "tenants/t1/tools/x/b", "123")
		lim := Limits{MaxFiles: 10, MaxFileSize: 1 << 10, MaxTotal: 5}
		_, err := PackFiles(ctx, st, "tenants/t1/tools/x", "platform-assets/a1/1", "", lim)
		if !errors.Is(err, ErrTotalTooLarge) {
			t.Fatalf("want ErrTotalTooLarge, got %v", err)
		}
	})
}

// Restore: a missing file goes into missing without an error; modified content reports ErrChecksum.
func TestRestoreFiles(t *testing.T) {
	ctx := context.Background()

	t.Run("normal restore", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "src/SKILL.md", "# demo\n")
		files, err := PackFiles(ctx, st, "src", "snap/1", "SKILL.md", DefaultLimits())
		if err != nil {
			t.Fatalf("PackFiles failed: %v", err)
		}
		missing, err := RestoreFiles(ctx, st, "snap/1", "dst", files)
		if err != nil || len(missing) != 0 {
			t.Fatalf("RestoreFiles = %v, missing=%v", err, missing)
		}
		if got := mustRead(t, st, "dst/SKILL.md"); got != "# demo\n" {
			t.Fatalf("target content = %q", got)
		}
	})

	t.Run("a file listed in the manifest is missing", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "snap/1/SKILL.md", "x")
		files := []FileEntry{{Path: "SKILL.md"}, {Path: "gone.md", Size: 3}}
		missing, err := RestoreFiles(ctx, st, "snap/1", "dst", files)
		if err != nil {
			t.Fatalf("must not fail: %v", err)
		}
		if len(missing) != 1 || missing[0] != "gone.md" {
			t.Fatalf("missing = %v", missing)
		}
	})

	t.Run("snapshot content disagrees with the manifest", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "snap/1/SKILL.md", "tampered")
		files := []FileEntry{{Path: "SKILL.md", SHA256: strings.Repeat("0", 64)}}
		if _, err := RestoreFiles(ctx, st, "snap/1", "dst", files); !errors.Is(err, ErrChecksum) {
			t.Fatalf("want ErrChecksum, got %v", err)
		}
	})
}

func TestStripSecrets(t *testing.T) {
	t.Run("tool config values are cleared", func(t *testing.T) {
		payload := map[string]any{
			"name":          "weather",
			"config_fields": []any{map[string]any{"key": "token"}},
			"configs":       map[string]any{"token": "s3cr3t"},
		}
		out, warnings := StripSecrets(KindTool, payload)
		if cfg, _ := out["configs"].(map[string]any); len(cfg) != 0 {
			t.Fatalf("configs was not cleared: %+v", out["configs"])
		}
		if len(warnings) != 1 {
			t.Fatalf("warnings = %v", warnings)
		}
		// The original payload is not modified
		if orig, _ := payload["configs"].(map[string]any); orig["token"] != "s3cr3t" {
			t.Fatalf("StripSecrets modified its input: %+v", payload["configs"])
		}
	})

	t.Run("credential keys inside agent config are cleared", func(t *testing.T) {
		payload := map[string]any{
			"name": "poet",
			"config": map[string]any{
				"model":   "gpt-4o",
				"api_key": "sk-xxx",
				"nested":  map[string]any{"db_password": "p", "note": "keep"},
			},
		}
		out, warnings := StripSecrets(KindAgent, payload)
		cfg := out["config"].(map[string]any)
		if cfg["api_key"] != "" {
			t.Fatalf("api_key = %v", cfg["api_key"])
		}
		nested := cfg["nested"].(map[string]any)
		if nested["db_password"] != "" || nested["note"] != "keep" {
			t.Fatalf("nested = %+v", nested)
		}
		if cfg["model"] != "gpt-4o" {
			t.Fatalf("model must be untouched: %v", cfg["model"])
		}
		if len(warnings) != 2 {
			t.Fatalf("warnings = %v", warnings)
		}
	})

	t.Run("skill is returned as is", func(t *testing.T) {
		payload := map[string]any{"name": "demo", "description": "d"}
		out, warnings := StripSecrets(KindSkill, payload)
		if len(warnings) != 0 || out["description"] != "d" {
			t.Fatalf("out=%+v warnings=%v", out, warnings)
		}
	})
}

func TestSuggestName(t *testing.T) {
	cases := []struct {
		base  string
		taken []string
		want  string
	}{
		{"demo", nil, "demo"},
		{"demo", []string{"demo"}, "demo-2"},
		{"demo", []string{"demo", "demo-2"}, "demo-3"},
		{"Demo", []string{"demo"}, "Demo-2"}, // names compare case-insensitively, the returned value keeps the original case
		{"", []string{"asset"}, "asset-2"},
	}
	for _, c := range cases {
		if got := SuggestName(c.base, c.taken); got != c.want {
			t.Fatalf("SuggestName(%q, %v) = %q, want %q", c.base, c.taken, got, c.want)
		}
	}

	long := strings.Repeat("a", MaxNameLen)
	got := SuggestName(long, []string{long})
	if len(got) > MaxNameLen || !strings.HasSuffix(got, "-2") {
		t.Fatalf("suggestion for an over-long name = %q (len=%d)", got, len(got))
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("poet-2"); err != nil {
		t.Fatalf("a valid name was rejected: %v", err)
	}
	for _, bad := range []string{"", "-x", "a b", "a/b", strings.Repeat("a", MaxNameLen+1)} {
		if err := ValidateName(bad); err == nil {
			t.Fatalf("the invalid name %q passed validation", bad)
		}
	}
}

// A workflow name writes no directory, but it becomes an endpoint name and a model-side tool name
// (local__<name>), so the character set matches ValidateName: a Chinese name can be created, cannot
// be listed, and cannot be granted — it has to be blocked at the entry point.
func TestValidateWorkflowName(t *testing.T) {
	if err := ValidateWorkflowName("daily-sales-report"); err != nil {
		t.Fatalf("a valid workflow name was rejected: %v", err)
	}
	for _, bad := range []string{"", "-x", "每日简报", "a b", "a/b", strings.Repeat("a", MaxNameLen+1)} {
		if err := ValidateWorkflowName(bad); err == nil {
			t.Fatalf("the invalid workflow name %q passed validation", bad)
		}
	}
	if err := ValidateWorkflowName("每日简报"); err == nil || !strings.Contains(err.Error(), "description field") {
		t.Fatalf("the error for a Chinese name should point at \"write the description\": %v", err)
	}
}
