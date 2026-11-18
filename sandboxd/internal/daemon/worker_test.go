package daemon

import (
	"path/filepath"
	"testing"
)

// TestResolveCodehostBinary 钉住 codehost 二进制路径的归一化契约。
// 核心是「容器内绝对路径必须原样透传」：filepath.Abs 是平台相关的，在 Windows 上
// 会把 /usr/local/bin/codehost 转成 d:\usr\...，导致 docker/nsjail exec 失败。
func TestResolveCodehostBinary(t *testing.T) {
	t.Run("container absolute path passthrough", func(t *testing.T) {
		got, err := resolveCodehostBinary("/usr/local/bin/codehost")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/usr/local/bin/codehost" {
			t.Fatalf("got %q, want /usr/local/bin/codehost (must not go through filepath.Abs)", got)
		}
	})

	t.Run("bare command name", func(t *testing.T) {
		got, err := resolveCodehostBinary("codehost")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "codehost" {
			t.Fatalf("got %q, want codehost", got)
		}
	})

	t.Run("host relative path becomes absolute", func(t *testing.T) {
		rel := filepath.Join("bin", "codehost.exe")
		got, err := resolveCodehostBinary(rel)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("got %q, want absolute path", got)
		}
	})
}
