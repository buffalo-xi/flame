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
		t.Fatalf("写入 %s 失败: %v", key, err)
	}
}

func mustRead(t *testing.T, st storage.Storage, key string) string {
	t.Helper()
	b, err := st.ReadFile(context.Background(), key)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", key, err)
	}
	return string(b)
}

// 打包：整棵目录被镜像，清单按 path 升序且 sha256 对得上。
func TestPackFilesMirrorsTreeAndHashes(t *testing.T) {
	st := newStorage(t)
	ctx := context.Background()
	mustWrite(t, st, "tenants/t1/skills/demo/SKILL.md", "# demo\n")
	mustWrite(t, st, "tenants/t1/skills/demo/scripts/run.py", "print(1)\n")

	files, err := PackFiles(ctx, st, "tenants/t1/skills/demo", "platform-assets/a1/1", "SKILL.md", DefaultLimits())
	if err != nil {
		t.Fatalf("PackFiles 失败: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("文件数 = %d, 期望 2", len(files))
	}
	if files[0].Path != "SKILL.md" || files[1].Path != "scripts/run.py" {
		t.Fatalf("清单顺序不对: %+v", files)
	}
	sum := sha256.Sum256([]byte("# demo\n"))
	if files[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 不符: %s", files[0].SHA256)
	}
	if got := mustRead(t, st, "platform-assets/a1/1/scripts/run.py"); got != "print(1)\n" {
		t.Fatalf("快照内容 = %q", got)
	}
}

func TestPackFilesRejectsEmptyAndMissingEntry(t *testing.T) {
	ctx := context.Background()

	t.Run("目录不存在", func(t *testing.T) {
		st := newStorage(t)
		_, err := PackFiles(ctx, st, "tenants/t1/skills/none", "platform-assets/a1/1", "", DefaultLimits())
		if !errors.Is(err, ErrNoFiles) {
			t.Fatalf("期望 ErrNoFiles，实际 %v", err)
		}
	})

	t.Run("缺入口文件", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "tenants/t1/skills/demo/a.txt", "a")
		_, err := PackFiles(ctx, st, "tenants/t1/skills/demo", "platform-assets/a1/1", "SKILL.md", DefaultLimits())
		if !errors.Is(err, ErrMissingEntry) {
			t.Fatalf("期望 ErrMissingEntry，实际 %v", err)
		}
	})
}

func TestPackFilesLimits(t *testing.T) {
	ctx := context.Background()

	t.Run("文件数超限", func(t *testing.T) {
		st := newStorage(t)
		for _, n := range []string{"a", "b", "c"} {
			mustWrite(t, st, "tenants/t1/tools/x/"+n, "1")
		}
		lim := Limits{MaxFiles: 2, MaxFileSize: 1 << 10, MaxTotal: 1 << 10}
		_, err := PackFiles(ctx, st, "tenants/t1/tools/x", "platform-assets/a1/1", "", lim)
		if !errors.Is(err, ErrTooManyFiles) {
			t.Fatalf("期望 ErrTooManyFiles，实际 %v", err)
		}
	})

	t.Run("单文件超限", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "tenants/t1/tools/x/big", "abcd")
		lim := Limits{MaxFiles: 10, MaxFileSize: 3, MaxTotal: 1 << 10}
		_, err := PackFiles(ctx, st, "tenants/t1/tools/x", "platform-assets/a1/1", "", lim)
		if !errors.Is(err, ErrFileTooLarge) {
			t.Fatalf("期望 ErrFileTooLarge，实际 %v", err)
		}
	})

	t.Run("合计超限", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "tenants/t1/tools/x/a", "123")
		mustWrite(t, st, "tenants/t1/tools/x/b", "123")
		lim := Limits{MaxFiles: 10, MaxFileSize: 1 << 10, MaxTotal: 5}
		_, err := PackFiles(ctx, st, "tenants/t1/tools/x", "platform-assets/a1/1", "", lim)
		if !errors.Is(err, ErrTotalTooLarge) {
			t.Fatalf("期望 ErrTotalTooLarge，实际 %v", err)
		}
	})
}

// 还原：缺文件进 missing 不报错；内容被改过则报 ErrChecksum。
func TestRestoreFiles(t *testing.T) {
	ctx := context.Background()

	t.Run("正常还原", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "src/SKILL.md", "# demo\n")
		files, err := PackFiles(ctx, st, "src", "snap/1", "SKILL.md", DefaultLimits())
		if err != nil {
			t.Fatalf("PackFiles 失败: %v", err)
		}
		missing, err := RestoreFiles(ctx, st, "snap/1", "dst", files)
		if err != nil || len(missing) != 0 {
			t.Fatalf("RestoreFiles = %v, missing=%v", err, missing)
		}
		if got := mustRead(t, st, "dst/SKILL.md"); got != "# demo\n" {
			t.Fatalf("目标内容 = %q", got)
		}
	})

	t.Run("清单里的文件缺失", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "snap/1/SKILL.md", "x")
		files := []FileEntry{{Path: "SKILL.md"}, {Path: "gone.md", Size: 3}}
		missing, err := RestoreFiles(ctx, st, "snap/1", "dst", files)
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		if len(missing) != 1 || missing[0] != "gone.md" {
			t.Fatalf("missing = %v", missing)
		}
	})

	t.Run("快照内容与清单不符", func(t *testing.T) {
		st := newStorage(t)
		mustWrite(t, st, "snap/1/SKILL.md", "改过")
		files := []FileEntry{{Path: "SKILL.md", SHA256: strings.Repeat("0", 64)}}
		if _, err := RestoreFiles(ctx, st, "snap/1", "dst", files); !errors.Is(err, ErrChecksum) {
			t.Fatalf("期望 ErrChecksum，实际 %v", err)
		}
	})
}

func TestStripSecrets(t *testing.T) {
	t.Run("工具清空配置值", func(t *testing.T) {
		payload := map[string]any{
			"name":          "weather",
			"config_fields": []any{map[string]any{"key": "token"}},
			"configs":       map[string]any{"token": "s3cr3t"},
		}
		out, warnings := StripSecrets(KindTool, payload)
		if cfg, _ := out["configs"].(map[string]any); len(cfg) != 0 {
			t.Fatalf("configs 未清空: %+v", out["configs"])
		}
		if len(warnings) != 1 {
			t.Fatalf("warnings = %v", warnings)
		}
		// 原 payload 不被改动
		if orig, _ := payload["configs"].(map[string]any); orig["token"] != "s3cr3t" {
			t.Fatalf("StripSecrets 改动了入参: %+v", payload["configs"])
		}
	})

	t.Run("agent config 里的凭据键被清空", func(t *testing.T) {
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
			t.Fatalf("model 不该被改: %v", cfg["model"])
		}
		if len(warnings) != 2 {
			t.Fatalf("warnings = %v", warnings)
		}
	})

	t.Run("skill 原样返回", func(t *testing.T) {
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
		{"Demo", []string{"demo"}, "Demo-2"}, // 名字比较不区分大小写，返回值保留原大小写
		{"", []string{"asset"}, "asset-2"},
	}
	for _, c := range cases {
		if got := SuggestName(c.base, c.taken); got != c.want {
			t.Fatalf("SuggestName(%q, %v) = %q, 期望 %q", c.base, c.taken, got, c.want)
		}
	}

	long := strings.Repeat("a", MaxNameLen)
	got := SuggestName(long, []string{long})
	if len(got) > MaxNameLen || !strings.HasSuffix(got, "-2") {
		t.Fatalf("超长名字的建议 = %q（len=%d）", got, len(got))
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("poet-2"); err != nil {
		t.Fatalf("合法名字被拒: %v", err)
	}
	for _, bad := range []string{"", "-x", "a b", "a/b", strings.Repeat("a", MaxNameLen+1)} {
		if err := ValidateName(bad); err == nil {
			t.Fatalf("非法名字 %q 通过了校验", bad)
		}
	}
}

// 工作流名不落目录，但会变成端点名与模型侧工具名（local__<name>），所以字符集与
// ValidateName 同口径：中文名能建出来、上架不了、授权不上，必须在入口就挡住。
func TestValidateWorkflowName(t *testing.T) {
	if err := ValidateWorkflowName("daily-sales-report"); err != nil {
		t.Fatalf("合法工作流名被拒: %v", err)
	}
	for _, bad := range []string{"", "-x", "每日简报", "a b", "a/b", strings.Repeat("a", MaxNameLen+1)} {
		if err := ValidateWorkflowName(bad); err == nil {
			t.Fatalf("非法工作流名 %q 通过了校验", bad)
		}
	}
	if err := ValidateWorkflowName("每日简报"); err == nil || !strings.Contains(err.Error(), "description field") {
		t.Fatalf("中文名报错应指出「中文写描述」: %v", err)
	}
}
