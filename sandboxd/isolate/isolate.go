// Package isolate provides the concrete process isolation implementations (none / nsjail / docker).
//
// Only sandboxd uses these implementations: the nexus side does no isolation and simply delegates execution to
// sandboxd through sandbox.NewRemote. Every implementation satisfies the sandbox.Sandbox interface.
package isolate

import (
	"errors"
	"fmt"
	"os/exec"

	"flame/tools/sandbox"
)

// Isolation implementation names (corresponding to mode in the config).
const (
	NoneName   = "none"
	NsjailName = "nsjail"
	DockerName = "docker"
)

// cgroup version selection (used for nsjail's memory limit). --cgroup_mem_max works under both v1 and v2 (writing
// memory.limit_in_bytes / memory.max respectively, both limiting physical memory rather than VSZ); this only decides
// "which version to tell nsjail to use".
const (
	CgroupV1   = "v1"   // no version flag, keep nsjail's default (cgroup v1)
	CgroupV2   = "v2"   // --use_cgroupv2 (the host must be v2)
	CgroupAuto = "auto" // --detect_cgroupv2: use v2 when present, otherwise fall back to v1
)

// errEmptyArgv means no command line was provided.
var errEmptyArgv = errors.New("isolate: empty argv")

// Options holds the construction arguments of an isolation implementation.
type Options struct {
	// Limits are the default resource caps; non-zero fields in Spec.Limits override them.
	Limits sandbox.Limits

	// ---- nsjail ----
	Binary         string // the nsjail executable, "nsjail" by default
	Rootfs         string // the sandbox root filesystem (required)
	UID            int    // user id inside the sandbox
	GID            int    // group id inside the sandbox
	DisableNetwork bool   // cut off the network
	ReadOnlyRootfs bool   // the root filesystem is read-only; an nsjail chroot is R/O by default, false adds --rw
	CgroupVersion  string // cgroup version selection: v1 / v2 / auto

	// ---- docker ----
	Image    string // the container image (required)
	Network  string // none / bridge / host
	ReadOnly bool   // the container rootfs is read-only
	User     string // the user inside the container, e.g. "1000:1000"

	// ExtraArgs are appended after the native arguments of each implementation (an escape hatch).
	ExtraArgs []string
}

// New creates an isolation implementation by mode name.
//
// Construction performs a dependency self-check (LookPath) so that environment problems surface at startup instead of
// failing on the first script/command execution.
func New(mode string, opts Options) (sandbox.Sandbox, error) {
	switch mode {
	case "", NoneName:
		return NewNone(), nil

	case NsjailName:
		if opts.Rootfs == "" {
			return nil, fmt.Errorf("isolate: nsjail requires rootfs")
		}
		if err := lookPath(opts.Binary, NsjailName); err != nil {
			return nil, err
		}
		return NewNsjail(opts), nil

	case DockerName:
		if opts.Image == "" {
			return nil, fmt.Errorf("isolate: docker requires image")
		}
		if err := lookPath(opts.Binary, DockerName); err != nil {
			return nil, err
		}
		return NewDocker(opts), nil

	default:
		return nil, fmt.Errorf("isolate: unknown mode %q", mode)
	}
}

// lookPath verifies the executable is available (falls back to fallback when bin is empty).
func lookPath(bin, fallback string) error {
	if bin == "" {
		bin = fallback
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("isolate: %s binary not found (%q): %w", fallback, bin, err)
	}
	return nil
}
