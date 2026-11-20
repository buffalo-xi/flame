package daemon

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"flame/tools/sandbox"
)

// guardMounts validates that every Mounts Host falls under one of the allowed roots.
//
// Background: Mounts[].Host is a host absolute path supplied by the caller; unchecked, a forged or mistyped
// request could mount /etc or / into the sandbox. This is a policy-level backstop — sandboxd does not interpret
// the caller's business paths (that is the caller's own responsibility), it only validates the range.
//
// An empty roots means no allowlist is configured — nothing is validated then (a warning is logged at startup).
func guardMounts(mounts []sandbox.Mount, roots []string) error {
	if len(roots) == 0 {
		return nil
	}

	absRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return fmt.Errorf("invalid allowed_mount_roots entry %q: %w", r, err)
		}
		absRoots = append(absRoots, abs)
	}
	if len(absRoots) == 0 {
		return nil
	}

	for _, m := range mounts {
		if m.Host == "" {
			continue
		}
		abs, err := filepath.Abs(m.Host)
		if err != nil {
			return fmt.Errorf("invalid mount host %q: %w", m.Host, err)
		}

		matched := false
		for _, root := range absRoots {
			rel, rerr := filepath.Rel(root, abs)
			if rerr != nil {
				continue // incomparable paths (different drive letters etc.): this root does not match, try the next
			}
			if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("mount %q is outside allowed_mount_roots %v", m.Host, roots)
		}
	}
	return nil
}

// guardDir validates that dir falls under some mount point's Guest (an empty string means "leave it to the
// implementation's default").
//
// Why it is needed: when the none implementation's TranslateToHost matches no mount point it **returns the input
// unchanged**, so a dir meant to be "an in-sandbox path" becomes a host path and is used directly as the working
// directory — breaking the contract that "paths in a Spec always carry in-sandbox semantics".
//
// Note that Guest is in-sandbox semantics, so path (forward slashes) is used for normalization here rather than
// filepath.
func guardDir(dir string, mounts []sandbox.Mount) error {
	if dir == "" {
		return nil
	}
	d := path.Clean(filepath.ToSlash(dir))
	for _, m := range mounts {
		if m.Guest == "" {
			continue
		}
		g := path.Clean(filepath.ToSlash(m.Guest))
		if d == g || strings.HasPrefix(d, g+"/") {
			return nil
		}
	}
	return fmt.Errorf("dir %q is not under any mount guest", dir)
}
