package agents

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// 上传落点的文件名清洗：浏览器可能带路径，控制字符与超长名也要挡住。
func TestSanitizeUploadName(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"report.md", "report.md"},
		{"C:\\Users\\me\\报表 2026.xlsx", "报表 2026.xlsx"},
		{"dir/sub/a.txt", "a.txt"},
		{"  空格名.txt  ", "空格名.txt"},
		{"a\x00b\x01.txt", "ab.txt"},
		{"", ""},
		{".", ""},
		{"../", ""}, // path.Base("../") = ".."，会让落点塌回工作区根，必须拒
	}
	for _, c := range cases {
		if got := sanitizeUploadName(c.raw); got != c.want {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", c.raw, got, c.want)
		}
	}

	// 超长名：保留扩展名，主名截到上限内
	long := strings.Repeat("文", 200) + ".md"
	got := sanitizeUploadName(long)
	if len([]rune(got)) > 120 {
		t.Errorf("长度 %d 超过 120：%q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, ".md") {
		t.Errorf("扩展名被截掉：%q", got)
	}
}

// 同名不覆盖：同名依次变成 -2、-3；落点目录由调用方给（chat 传 uploads，工作区面板传当前目录）。
func TestUniqueUploadRel(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	wsKey := paths.UserSessionKey("t0", "u1", "s1")
	ctx := context.Background()

	if got := uniqueUploadRel(ctx, st, wsKey, uploadDir, "a.txt"); got != "uploads/a.txt" {
		t.Fatalf("首次上传落点 = %q", got)
	}
	if err := st.WriteFile(ctx, wsKey+"/uploads/a.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := uniqueUploadRel(ctx, st, wsKey, uploadDir, "a.txt"); got != "uploads/a-2.txt" {
		t.Errorf("重复上传落点 = %q, want uploads/a-2.txt", got)
	}
	if err := st.WriteFile(ctx, wsKey+"/uploads/a-2.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := uniqueUploadRel(ctx, st, wsKey, uploadDir, "a.txt"); got != "uploads/a-3.txt" {
		t.Errorf("再次重复落点 = %q, want uploads/a-3.txt", got)
	}

	// 工作区面板指定目录：落点跟着走，且与 uploads/ 里的同名文件互不影响
	if got := uniqueUploadRel(ctx, st, wsKey, "outputs", "a.txt"); got != "outputs/a.txt" {
		t.Errorf("指定目录落点 = %q, want outputs/a.txt", got)
	}
}

// 附件清单：只认 uploads/ 下真实存在的相对路径，越界与不存在的静默跳过。
func TestAttachmentPrompt(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	wsKey := paths.UserSessionKey("t0", "u1", "s1")
	ctx := context.Background()
	if err := st.WriteFile(ctx, wsKey+"/uploads/ok.md", []byte("# hi")); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile(ctx, wsKey+"/AGENT.md", []byte("# agent")); err != nil {
		t.Fatal(err)
	}

	if got := attachmentPrompt(nil, "t0", "u1", "s1", []string{"uploads/ok.md"}); got != "" {
		t.Errorf("storage 为 nil 时应返回空串, got %q", got)
	}
	if got := attachmentPrompt(st, "t0", "u1", "s1", nil); got != "" {
		t.Errorf("无附件时应返回空串, got %q", got)
	}
	if got := attachmentPrompt(st, "t0", "", "s1", []string{"uploads/ok.md"}); got != "" {
		t.Errorf("无归属用户时应返回空串, got %q", got)
	}

	got := attachmentPrompt(st, "t0", "u1", "s1", []string{
		"uploads/ok.md",          // 有效
		"uploads/missing.md",     // 不存在
		"AGENT.md",               // 不在 uploads/ 下
		"../escape.txt",          // 越界
		"/etc/passwd",            // 绝对路径（SafePath 会剥掉前导 /，但不在 uploads/ 下）
		"uploads/../../etc/x.md", // 越界
	})
	if !strings.Contains(got, "- uploads/ok.md") {
		t.Fatalf("有效附件未列出: %q", got)
	}
	for _, bad := range []string{"missing.md", "AGENT.md", "escape.txt", "passwd", "etc/x.md"} {
		if strings.Contains(got, bad) {
			t.Errorf("不该列出 %q：%q", bad, got)
		}
	}
}
