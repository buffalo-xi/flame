package isolate

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flame/tools/sandbox"
)

// unitLabel marks every reusable unit (container).
//
// Used in two places:
//   - CleanOrphans finds containers left behind by an abnormal exit of the previous run by it;
//   - during troubleshooting `docker ps --filter label=sandboxd=1` shows at a glance which ones belong to this
//     service.
const unitLabel = "sandboxd=1"

// execLabel marks every one-shot (exec path) container.
//
// Why it is needed: an unlabelled container is invisible to sandboxd — once leaked, not even CleanOrphans can find it,
// and it can only wait for the container to exit by itself (`--rm` only deletes on container exit, it does not care
// whether the service is alive). With it, exec containers left behind by a SIGKILLed sandboxd are collected on the next
// start.
//
// ⚠️ The same premise as unitLabel: this service assumes it owns the docker daemon exclusively. With several
// instances sharing one daemon, the startup sweep would delete a sibling's in-flight containers.
const execLabel = "sandboxd-exec=1"

// orphanLabels are all container labels the startup sweep collects.
var orphanLabels = []string{unitLabel, execLabel}

// containerKillTimeout caps the "make the container disappear" path itself: killing a process must not become a new
// blocking point (it has to return even when the docker daemon hangs).
const containerKillTimeout = 10 * time.Second

// Acquire implements sandbox.Reusable: start a long-lived container as a reusable unit.
//
// The differences from Start (see nexus/docs/sandbox-design.md §9.3):
//   - it stays resident in the background (-d) and the container command is a fixed sleep infinity — it is only the
//     isolation boundary, and the interpreter is started fresh by every subsequent Exec;
//   - --init: the container's PID 1 becomes tini. Otherwise orphans/zombies forked by a script pile up under sleep
//     infinity with nobody reaping them;
//   - --label: leaves an identifying mark for the startup orphan collection (CleanOrphans);
//   - **no -t**: TTY line handling would break the binary frame protocol with its 4-byte length prefix.
//
// Note: docker exec does not support -v, so writable directories must be mounted when the container is created, and a
// per-request subdirectory has to be created under an already mounted mount point (see spec.Dir in Exec).
func (s *dockerSandbox) Acquire(ctx context.Context, spec *sandbox.Spec) (sandbox.Unit, error) {
	if s.opts.Image == "" {
		return nil, fmt.Errorf("isolate/docker: image is required")
	}
	if spec == nil {
		spec = &sandbox.Spec{}
	}

	lim := sandbox.MergeLimits(s.opts.Limits, spec.Limits)

	args := []string{"run", "-d", "--rm", "--init", "--label", unitLabel}
	args = append(args, s.containerArgs(spec, lim)...)
	args = append(args, s.opts.ExtraArgs...)
	args = append(args, s.opts.Image, "sleep", "infinity")

	out, err := exec.CommandContext(ctx, s.opts.Binary, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("isolate/docker: acquire unit: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return nil, fmt.Errorf("isolate/docker: acquire unit: empty container id")
	}
	return &dockerUnit{opts: s.opts, id: id}, nil
}

// containerArgs assembles the "container-level" arguments: network / read-only / user / limits / mounts / cwd / env.
//
// Start and Acquire share this one builder to keep the two from drifting — a one-shot run and a long-lived unit must be
// identical in isolation parameters, otherwise "same image, two strengths" becomes a very hard bug to find.
func (s *dockerSandbox) containerArgs(spec *sandbox.Spec, lim sandbox.Limits) []string {
	var args []string

	// Network: spec.Egress is the **already adjudicated** intent (the policy is decided in handleExec), nil = follow
	// the config. isolate does not make policy decisions, it only translates "should this be offline" into arguments.
	//
	// Note that with egress=true the configured network is used (NewDocker has already filled an empty value with
	// none): if the config really is none, the adjudication layer missed something, and none is kept here — better
	// offline than unexpectedly online.
	network := s.opts.Network
	if spec.Egress != nil && !*spec.Egress {
		network = "none"
	}
	args = append(args, "--network", network)
	if s.opts.ReadOnly {
		args = append(args, "--read-only")
	}
	if s.opts.User != "" {
		args = append(args, "--user", s.opts.User)
	}

	if lim.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(lim.MemoryMB)+"m")
	}
	if lim.CPUCount > 0 {
		args = append(args, "--cpus", strconv.Itoa(lim.CPUCount))
	}
	if lim.MaxPIDs > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(lim.MaxPIDs))
	}

	// Mounts (host paths are converted to slashes for Windows compatibility)
	hasTmpMount := false
	for _, m := range spec.Mounts {
		if m.Host == "" || m.Guest == "" {
			continue
		}
		if m.Guest == "/tmp" {
			hasTmpMount = true
		}
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		args = append(args, "-v", filepath.ToSlash(m.Host)+":"+m.Guest+":"+mode)
	}

	// Under a read-only rootfs /tmp is read-only as well — and it is the only generic location that "every image has
	// and that should naturally be writable": temporary files in shell sessions rely on it, and a program that writes
	// to /tmp (pip, a compiler's scratch space) would otherwise fail for a reason that looks unrelated to the sandbox.
	// Callers that already mounted /tmp explicitly (the script worker mounts its exclusive directory at /tmp) are not
	// overridden. size is a hard cap: tmpfs counts against container memory, and without a cap one download / cache
	// would blow up the cgroup.
	if s.opts.ReadOnly && !hasTmpMount {
		args = append(args, "--tmpfs", "/tmp:rw,size=256m,mode=1777")
	}

	if spec.Dir != "" {
		args = append(args, "-w", spec.Dir)
	}
	args = append(args, envArgs(spec.Env)...)
	return args
}

// envArgs filters and expands environment variables.
// Keys that do not start with an identifier character are skipped (Windows has entries like =C:, which docker rejects).
func envArgs(env []string) []string {
	var args []string
	for _, e := range env {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			continue
		}
		if !isEnvKey(e[:eq]) {
			continue
		}
		args = append(args, "-e", e)
	}
	return args
}

// dockerUnit is a sandbox unit made of one resident container.
type dockerUnit struct {
	opts Options
	id   string
}

func (u *dockerUnit) Name() string { return u.id }

// Exec starts a process inside the existing container (docker exec).
//
// The difference from run: exec only accepts "process-level" arguments such as -i / -w / -e / -u; network, read-only
// and resource caps are fixed when the container is created (container level), and processes started by exec inherit
// them.
//
// The returned Process has the same semantics as Start's, so the frame protocol layer does not have to distinguish L2
// from L3 at all.
func (u *dockerUnit) Exec(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	if spec == nil || len(spec.Argv) == 0 {
		return nil, errEmptyArgv
	}

	var cancel context.CancelFunc
	if spec.Limits.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, spec.Limits.Timeout)
	}

	args := []string{"exec", "-i"}
	if spec.Dir != "" {
		args = append(args, "-w", spec.Dir)
	}
	args = append(args, envArgs(spec.Env)...)
	args = append(args, u.id)
	args = append(args, spec.Argv...)

	// kill nil: this kills the docker exec CLI process (the process group), and that is merely the channel "into the
	// unit from outside" — the interpreter inside the unit does not necessarily die with it. The follow-up strike
	// inside the unit is the caller's job (see killResidual in daemon); nothing is done here.
	cmd := exec.CommandContext(ctx, u.opts.Binary, args...)
	return startWithPipes(cmd, spec, cancel, nil)
}

// Close destroys the container. It uses -f: the unit may still have processes that have not exited cleanly, so kill
// first and delete after.
//
// ⚠️ Call it only when "destroying the unit". Killing "the interpreter inside the unit" must not use it — that would
// throw away the isolation boundary which was hard to build, which amounts to falling back to L3.
func (u *dockerUnit) Close() error {
	if u.id == "" {
		return nil
	}
	out, err := exec.Command(u.opts.Binary, "rm", "-f", u.id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("isolate/docker: close unit %s: %w (%s)", u.id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CleanOrphans implements sandbox.OrphanCleaner: collect containers left behind by the previous abnormal exit.
//
// Why it is necessary: --rm only guarantees "deleted when the container exits", not "the container exits when the
// service does". When sandboxd is SIGKILLed (or the host loses power), the container has no chance to Close /
// removeContainer and keeps holding memory. At startup each label is swept once, at the cost of two docker ps calls.
//
// Two kinds are covered: reusable units (unitLabel) and one-shot exec containers (execLabel) — the latter only became
// addressable after the "kill everything" rework (before that it had no label at all, and a leak was invisible).
func (s *dockerSandbox) CleanOrphans(ctx context.Context) (int, error) {
	total := 0
	for _, label := range orphanLabels {
		n, err := s.removeByLabel(ctx, label)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// removeByLabel collects containers by label and returns how many were removed.
func (s *dockerSandbox) removeByLabel(ctx context.Context, label string) (int, error) {
	out, err := exec.CommandContext(ctx, s.opts.Binary, "ps", "-aq", "--filter", "label="+label).Output()
	if err != nil {
		return 0, fmt.Errorf("isolate/docker: list orphans (%s): %w", label, err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return 0, nil
	}

	args := append([]string{"rm", "-f"}, ids...)
	if rmOut, err := exec.CommandContext(ctx, s.opts.Binary, args...).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("isolate/docker: remove orphans (%s): %w (%s)", label, err, strings.TrimSpace(string(rmOut)))
	}
	return len(ids), nil
}
