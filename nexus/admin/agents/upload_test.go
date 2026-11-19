package agents

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// Name cleaning for upload targets: browsers may send a path, and control characters
// and over-long names have to be stopped too.
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
		{"../", ""}, // path.Base("../") = "..", which would collapse the target to the workspace root - must be rejected
	}
	for _, c := range cases {
		if got := sanitizeUploadName(c.raw); got != c.want {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", c.raw, got, c.want)
		}
	}

	// Over-long name: keep the extension, cut the stem to the limit
	long := strings.Repeat("文", 200) + ".md"
	got := sanitizeUploadName(long)
	if len([]rune(got)) > 120 {
		t.Errorf("length %d exceeds 120: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, ".md") {
		t.Errorf("extension was cut off: %q", got)
	}
}

// No overwriting on name clashes: repeated names become -2, -3; the caller picks the
// target directory (chat passes uploads, the workspace panel passes the current directory).
func TestUniqueUploadRel(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	wsKey := paths.UserSessionKey("t0", "u1", "s1")
	ctx := context.Background()

	if got := uniqueUploadRel(ctx, st, wsKey, uploadDir, "a.txt"); got != "uploads/a.txt" {
		t.Fatalf("first upload target = %q", got)
	}
	if err := st.WriteFile(ctx, wsKey+"/uploads/a.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := uniqueUploadRel(ctx, st, wsKey, uploadDir, "a.txt"); got != "uploads/a-2.txt" {
		t.Errorf("repeated upload target = %q, want uploads/a-2.txt", got)
	}
	if err := st.WriteFile(ctx, wsKey+"/uploads/a-2.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := uniqueUploadRel(ctx, st, wsKey, uploadDir, "a.txt"); got != "uploads/a-3.txt" {
		t.Errorf("repeated upload target again = %q, want uploads/a-3.txt", got)
	}

	// A directory picked by the workspace panel: the target follows it and does not collide
	// with same-named files under uploads/
	if got := uniqueUploadRel(ctx, st, wsKey, "outputs", "a.txt"); got != "outputs/a.txt" {
		t.Errorf("explicit-directory target = %q, want outputs/a.txt", got)
	}
}

// Attachment list: only existing relative paths under uploads/ count; traversal and
// missing files are skipped silently.
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
		t.Errorf("a nil storage should return an empty string, got %q", got)
	}
	if got := attachmentPrompt(st, "t0", "u1", "s1", nil); got != "" {
		t.Errorf("no attachments should return an empty string, got %q", got)
	}
	if got := attachmentPrompt(st, "t0", "", "s1", []string{"uploads/ok.md"}); got != "" {
		t.Errorf("no owning user should return an empty string, got %q", got)
	}

	got := attachmentPrompt(st, "t0", "u1", "s1", []string{
		"uploads/ok.md",          // valid
		"uploads/missing.md",     // does not exist
		"AGENT.md",               // not under uploads/
		"../escape.txt",          // traversal
		"/etc/passwd",            // absolute path (SafePath strips the leading / but it is still outside uploads/)
		"uploads/../../etc/x.md", // traversal
	})
	if !strings.Contains(got, "- uploads/ok.md") {
		t.Fatalf("valid attachment was not listed: %q", got)
	}
	for _, bad := range []string{"missing.md", "AGENT.md", "escape.txt", "passwd", "etc/x.md"} {
		if strings.Contains(got, bad) {
			t.Errorf("should not list %q: %q", bad, got)
		}
	}
}
