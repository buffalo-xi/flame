package isolate

import (
	"os"
	"path/filepath"
	"testing"
)

// resolveRootfsCmd decides whether a bare command name exists inside the nsjail rootfs, because nsjail's execve does
// not resolve PATH. The case that matters is a rootfs prepared from a container image: its /usr/local/bin entries are
// absolute symlinks (lua -> /usr/bin/lua5.4), and the target only exists inside the rootfs — a host-side Stat sees a
// dangling link and the name is passed through unresolved, which nsjail then fails to exec.
func TestResolveRootfsCmd(t *testing.T) {
	rootfs := t.TempDir()
	write := func(rel string, mode os.FileMode) string {
		p := filepath.Join(rootfs, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		return p
	}
	link := func(rel, target string) {
		p := filepath.Join(rootfs, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.Symlink(target, p); err != nil {
			// Creating symlinks on Windows needs a privilege this test cannot assume; the resolution logic itself
			// is platform independent, so skipping is honest.
			t.Skipf("cannot create symlinks here: %v", err)
		}
	}

	write("usr/bin/python3", 0o755)
	write("usr/bin/lua5.4", 0o755)
	write("usr/bin/real", 0o755)
	link("usr/local/bin/lua", "/usr/bin/lua5.4")    // absolute target, exists inside the rootfs
	link("usr/local/bin/relative", "../bin/real")   // relative target
	link("usr/local/bin/dangling", "/usr/bin/nope") // target missing inside the rootfs
	link("usr/local/bin/loop", "/usr/local/bin/loop")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain binary in /usr/bin", "python3", "/usr/bin/python3"},
		{"absolute symlink resolved through the rootfs", "lua", "/usr/local/bin/lua"},
		{"relative symlink resolved through the rootfs", "relative", "/usr/local/bin/relative"},
		{"dangling symlink stays unresolved", "dangling", "dangling"},
		{"symlink loop stays unresolved", "loop", "loop"},
		{"missing name stays unresolved", "nosuchbinary", "nosuchbinary"},
		{"a path with a slash passes through", "/opt/flame/bin/codehost", "/opt/flame/bin/codehost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRootfsCmd(rootfs, tc.in); got != tc.want {
				t.Errorf("resolveRootfsCmd(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// An empty rootfs argument must not panic: it means "not configured", and Start reports that separately.
	if got := resolveRootfsCmd("", "flame-test-nosuch-binary"); got != "flame-test-nosuch-binary" {
		t.Errorf("empty rootfs: got %q, want the name unchanged", got)
	}
}
