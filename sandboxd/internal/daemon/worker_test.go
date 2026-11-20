package daemon

import (
	"path/filepath"
	"testing"
)

// TestResolveCodehostBinary pins down the normalisation contract for the codehost binary path.
// The core point is "an in-container absolute path must be passed through verbatim": filepath.Abs is platform
// dependent and on Windows would turn /usr/local/bin/codehost into d:\usr\..., making docker / nsjail exec fail.
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
