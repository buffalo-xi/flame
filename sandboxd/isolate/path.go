package isolate

import (
	"path"
	"path/filepath"
	"strings"

	"flame/tools/sandbox"
)

// Path conventions:
//
//	Paths appearing in Spec.Argv / Spec.Dir uniformly use "in-sandbox" semantics (for example /workspace and /agent
//	per the exec convention, and /workers/default/worker.lua for the former worker executor scripts). The isolation
//	implementations (nsjail / docker) can use them directly once Mounts are mounted; the none implementation does no
//	isolation, so it must translate them back into host paths according to Mounts before executing.
//
// That way callers do not have to distinguish implementations and only write one set of paths.

// TranslateToHost translates an "in-sandbox path" back into a host path according to mounts.
// When no mount point matches (for example the executable name "lua54", or a path that is already a host path), it is
// returned unchanged.
func TranslateToHost(mounts []sandbox.Mount, guest string) string {
	if guest == "" || len(mounts) == 0 {
		return guest
	}

	var best *sandbox.Mount
	for i := range mounts {
		m := &mounts[i]
		if m.Guest == "" {
			continue
		}
		if !hasPathPrefix(guest, m.Guest) {
			continue
		}
		if best == nil || len(m.Guest) > len(best.Guest) {
			best = m
		}
	}
	if best == nil {
		return guest
	}

	rest := strings.TrimPrefix(path.Clean(filepath.ToSlash(guest)),
		path.Clean(filepath.ToSlash(best.Guest)))
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return best.Host
	}
	return filepath.Join(best.Host, filepath.FromSlash(rest))
}

// TranslateArgv translates the in-sandbox paths in argv.
// Only arguments "starting with /" (in-sandbox absolute paths) are translated; command-line strings and switches (such
// as -c, "echo hello") are all skipped so nothing is hit by accident. The first element (the executable) follows the
// same rule: "/usr/bin/lua5.4" is translated while "lua5.4" is left alone.
func TranslateArgv(mounts []sandbox.Mount, argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	out := make([]string, len(argv))
	copy(out, argv)
	for i, a := range out {
		if strings.HasPrefix(a, "/") {
			out[i] = TranslateToHost(mounts, a)
		}
	}
	return out
}

// hasPathPrefix reports whether p is under prefix (compared per path segment, so /work does not match /workshop).
func hasPathPrefix(p, prefix string) bool {
	pp := path.Clean(filepath.ToSlash(p))
	pre := path.Clean(filepath.ToSlash(prefix))
	if pre == "/" {
		return strings.HasPrefix(pp, "/")
	}
	return pp == pre || strings.HasPrefix(pp, pre+"/")
}
