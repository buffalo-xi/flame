package browserpool

import (
	"path/filepath"
	"testing"
)

// A relative driver path must be resolved against the startup cwd, not left relative: the driver is spawned with cwd =
// the session's workspace, so "bin/browserhost.exe" would be looked up inside the tenant's directory and every spawn
// would fail (the self-test still passes, because it runs from this process's cwd — that is what made the bug silent).
func TestNormalizePathsAbsolutizesDriverPath(t *testing.T) {
	c := &Config{
		WorkspaceRoot: "run/browserd/workspace",
		Browser: BrowserConfig{
			Binary:   filepath.Join("bin", "browserhost.exe"),
			ExecPath: filepath.Join("chrome", "chrome.exe"),
		},
	}
	c.applyDefaults()
	if err := c.normalizePaths(); err != nil {
		t.Fatalf("normalizePaths: %v", err)
	}
	for name, got := range map[string]string{
		"browser.binary":    c.Browser.Binary,
		"browser.exec_path": c.Browser.ExecPath,
	} {
		if !filepath.IsAbs(got) {
			t.Errorf("%s = %q, want an absolute path", name, got)
		}
	}
	if !filepath.IsAbs(c.WorkspaceRoot) || !filepath.IsAbs(c.Browser.ProfileRoot) {
		t.Errorf("directory roots must stay absolute, got %q / %q", c.WorkspaceRoot, c.Browser.ProfileRoot)
	}
}

// A bare command name is a PATH lookup and must not be turned into a path — defaultBinary is exactly that.
func TestNormalizePathsKeepsBareDriverName(t *testing.T) {
	c := &Config{Browser: BrowserConfig{Binary: "browserhost"}}
	c.applyDefaults()
	if err := c.normalizePaths(); err != nil {
		t.Fatalf("normalizePaths: %v", err)
	}
	if c.Browser.Binary != "browserhost" {
		t.Errorf("browser.binary = %q, want the bare name untouched", c.Browser.Binary)
	}
	if c.Browser.ExecPath != "" {
		t.Errorf("browser.exec_path = %q, want empty (autodetect) untouched", c.Browser.ExecPath)
	}
}

// A leading "/" names a path inside the pool container's rootfs: passing it through filepath.Abs would make it
// d:\usr\... on Windows, so it must be kept verbatim (the same trap as sandboxd's resolveCodehostBinary).
func TestAbsolutizeCommandPathKeepsContainerPath(t *testing.T) {
	got, err := absolutizeCommandPath("/usr/bin/chromium")
	if err != nil {
		t.Fatalf("absolutizeCommandPath: %v", err)
	}
	if got != "/usr/bin/chromium" {
		t.Errorf("got %q, want /usr/bin/chromium unchanged", got)
	}
}
