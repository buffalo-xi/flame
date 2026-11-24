package isolate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"flame/tools/sandbox"
)

// nsjailPath is PATH inside the jail: nsjail executes commands through execve and does not resolve PATH, so this PATH
// must be passed to the jailed process via --env (codehost forks interpreters and depends on it) and also be used to
// resolve a bare command name into an absolute path inside the rootfs (see resolveRootfsCmd).
const nsjailPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// nsjailSandbox executes commands inside Linux namespaces/cgroups through nsjail (Linux only).
//
// Advantages: extremely low startup overhead (~10ms), which fits pooling (long-lived workers) naturally.
// Disadvantages: the rootfs (including interpreters and dependencies) must be prepared by hand, and it cannot run on
// Windows directly.
type nsjailSandbox struct {
	opts Options
}

// NewNsjail creates the nsjail isolation implementation. opts.Rootfs is required.
func NewNsjail(opts Options) sandbox.Sandbox {
	if opts.Binary == "" {
		opts.Binary = NsjailName
	}
	return &nsjailSandbox{opts: opts}
}

func (s *nsjailSandbox) Name() string { return NsjailName }

func (s *nsjailSandbox) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	if len(spec.Argv) == 0 {
		return nil, errEmptyArgv
	}
	if s.opts.Rootfs == "" {
		return nil, fmt.Errorf("isolate/nsjail: rootfs is required")
	}

	lim := sandbox.MergeLimits(s.opts.Limits, spec.Limits)

	var cancel context.CancelFunc
	if lim.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, lim.Timeout)
		// cancel is handed to startWithPipes to release once the process ends, so the jail is not terminated early
	}

	args := []string{"-Mo", "--chroot", s.opts.Rootfs}

	// Mounts: Host → Guest
	for _, m := range spec.Mounts {
		if m.Host == "" || m.Guest == "" {
			continue
		}
		flag := "--bindmount"
		if m.ReadOnly {
			flag = "--bindmount_ro"
		}
		args = append(args, flag, filepath.ToSlash(m.Host)+":"+m.Guest)
	}

	if spec.Dir != "" {
		args = append(args, "--cwd", spec.Dir)
	}
	if s.opts.UID > 0 {
		args = append(args, "--user", strconv.Itoa(s.opts.UID))
	}
	if s.opts.GID > 0 {
		args = append(args, "--group", strconv.Itoa(s.opts.GID))
	}
	// PATH inside the jail: nsjail does not inherit the parent's environment by default (without an explicit --env the
	// jail's PATH is empty), while codehost forks interpreters by command name ("lua"/"python3"/"node", see
	// langs.*.command and ResolveCommand), so PATH is how they get resolved inside the rootfs. A minimal PATH covering
	// the standard rootfs locations (/usr/local/bin symlinks, /usr/bin binaries) is provided.
	args = append(args, "--env", "PATH="+nsjailPath)
	// Read-only rootfs: an nsjail chroot is R/O by default (only --rw makes it writable), so ReadOnlyRootfs=true needs
	// no extra argument and --rw is added only when writability is explicitly requested.
	//
	// This used to be mis-mapped to `--disable_proc`, whose meaning is "Disable mounting procfs in the jail" and has
	// nothing to do with a read-only rootfs (and mount_proc defaults to false in config.proto, so the flag was
	// essentially a no-op). The consequence was that read_only_rootfs=false silently did nothing — anyone trying to
	// turn the read-only mode off would hit it — hence the direct translation.
	if !s.opts.ReadOnlyRootfs {
		args = append(args, "--rw")
	}
	// Network cutoff: spec.Egress is the **already adjudicated** intent (the policy is decided in handleExec), nil =
	// follow the config.
	//
	// ⚠️ egress=true here only means "do not actively cut it off": nsjail clones a new netns by default and the jail has
	// no external interface anyway, so real egress needs --disable_clone_newnet in extra_args (the config's job, see
	// the warning in validateNetworkOnRequest). isolate does not judge this, and should not.
	disableNet := s.opts.DisableNetwork
	if spec.Egress != nil {
		disableNet = !*spec.Egress
	}
	if disableNet {
		args = append(args, "--iface_no_lo")
	}

	// Resource caps
	// Memory: use the cgroup physical memory cap (--cgroup_mem_max, in bytes) rather than --rlimit_as — the latter
	// limits virtual address space (VSZ), and runtimes such as Go/V8 reserve tens of GB of virtual memory, so they
	// would be killed by mistake. --cgroup_mem_max works under both v1 and v2; the version comes from the
	// cgroup_version config.
	if lim.MemoryMB > 0 {
		switch s.opts.CgroupVersion {
		case CgroupV2:
			args = append(args, "--use_cgroupv2")
		case CgroupAuto:
			args = append(args, "--detect_cgroupv2")
			// CgroupV1 or empty: no version flag, keep nsjail's default (v1)
		}
		args = append(args, "--cgroup_mem_max", strconv.Itoa(lim.MemoryMB*1024*1024))
	}
	if lim.CPUCount > 0 {
		args = append(args, "--max_cpus", strconv.Itoa(lim.CPUCount))
	}
	if lim.MaxPIDs > 0 {
		args = append(args, "--rlimit_nproc", strconv.Itoa(lim.MaxPIDs))
	}
	if lim.Timeout > 0 {
		args = append(args, "--time_limit", strconv.Itoa(int(lim.Timeout.Seconds())))
	}

	args = append(args, s.opts.ExtraArgs...)
	args = append(args, "--")
	// nsjail's execve does not resolve PATH, so a bare command name (such as "cat", "python3") reports No such file or
	// directory. argv[0] is resolved into an absolute path inside the rootfs here; anything already containing a slash
	// is kept as-is.
	argv := append([]string(nil), spec.Argv...)
	argv[0] = resolveRootfsCmd(s.opts.Rootfs, argv[0])
	args = append(args, argv...)

	// kill nil: the default approach (the process group). The nsjail process itself and everything it derives live in
	// the host process tree, which the process group covers; if it turns out that nsjail does setsid itself away, this
	// must be switched to its cgroup parameters (see the killTree note in sysproc_unix.go).
	cmd := exec.CommandContext(ctx, s.opts.Binary, args...)
	return startWithPipes(cmd, spec, cancel, nil)
}

// resolveRootfsCmd resolves a bare command name into an absolute path inside the rootfs.
//
// nsjail executes argv[0] through execve without resolving PATH, so a bare command name gives ENOENT. This walks
// nsjailPath inside the rootfs and returns the in-jail absolute path (for example /usr/bin/cat); when it is not found,
// or already contains a slash, it is returned unchanged so nsjail reports its own error.
func resolveRootfsCmd(rootfs, cmd string) string {
	if cmd == "" || rootfs == "" || strings.ContainsRune(cmd, '/') {
		return cmd
	}
	for _, dir := range strings.Split(nsjailPath, ":") {
		guest := filepath.Join(dir, cmd) // in-jail path
		if rootfsHasFile(rootfs, guest, 0) {
			return guest
		}
	}
	return cmd
}

// rootfsHasFile reports whether guest is a regular file inside rootfs, resolving symlinks with jail semantics.
//
// The difference from os.Stat(filepath.Join(rootfs, guest)) matters: a rootfs prepared from a container image carries
// absolute symlinks such as /usr/local/bin/lua -> /usr/bin/lua5.4. On the host that target usually does not exist, so a
// plain Stat sees a dangling link and reports "missing", and the command name is passed to nsjail unresolved — inside
// the jail execve() then fails with ENOENT and the language is reported as unavailable. An absolute target therefore
// has to be re-anchored at rootfs, which is where it points once the jail is up.
//
// The returned path is still guest, not the target: inside the jail the kernel resolves the link the same way.
func rootfsHasFile(rootfs, guest string, depth int) bool {
	const maxSymlinkDepth = 8
	if depth > maxSymlinkDepth {
		// A link cycle: treat it as missing rather than looping.
		return false
	}
	host := filepath.Join(rootfs, guest) // host-side path (rootfs prefix)
	fi, err := os.Lstat(host)
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return !fi.IsDir()
	}
	target, err := os.Readlink(host)
	if err != nil {
		return false
	}
	if !path.IsAbs(target) {
		target = path.Join(path.Dir(guest), target)
	}
	return rootfsHasFile(rootfs, target, depth+1)
}
