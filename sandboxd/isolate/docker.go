package isolate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"flame/tools/sandbox"
)

// dockerSandbox executes commands inside a container through docker run (cross-platform).
//
// Note: a container shares the host kernel, so the isolation strength is on par with nsjail; the advantages are the
// image ecosystem and cross-platform support, the cost is roughly 300ms–1s per start. Prefer nsjail where startup
// overhead matters.
type dockerSandbox struct {
	opts Options
}

// NewDocker creates the docker isolation implementation. opts.Image is required.
func NewDocker(opts Options) sandbox.Sandbox {
	if opts.Binary == "" {
		opts.Binary = DockerName
	}
	if opts.Network == "" {
		opts.Network = "none"
	}
	return &dockerSandbox{opts: opts}
}

func (s *dockerSandbox) Name() string { return DockerName }

func (s *dockerSandbox) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	if len(spec.Argv) == 0 {
		return nil, errEmptyArgv
	}
	if s.opts.Image == "" {
		return nil, fmt.Errorf("isolate/docker: image is required")
	}

	lim := sandbox.MergeLimits(s.opts.Limits, spec.Limits)

	var cancel context.CancelFunc
	if lim.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, lim.Timeout)
		// cancel is handed to startWithPipes to release once the process ends, so the container is not killed early
	}

	// We name the container ourselves: it is the only addressable handle of this execution, and "kill everything"
	// depends entirely on it — see the note on kill below.
	name, err := newExecContainerName()
	if err != nil {
		releaseCancel(cancel)
		return nil, err
	}

	args := []string{"run", "--rm", "-i", "--name", name, "--label", execLabel}
	// The container-level arguments (network / read-only / user / limits / mounts / cwd / env) share the same builder
	// as the long-lived unit — a one-shot run and a reused unit must be identical in isolation strength.
	args = append(args, s.containerArgs(spec, lim)...)
	args = append(args, s.opts.ExtraArgs...)
	args = append(args, s.opts.Image)
	args = append(args, spec.Argv...)

	cmd := exec.CommandContext(ctx, s.opts.Binary, args...)

	// The cleanup of this execution: make the container disappear first, then collect the CLI process.
	//
	// Why killing only the CLI is not enough (neither cmd.Process.Kill nor the process group is):
	// the container is owned by dockerd / containerd-shim, and the CLI is merely a pipe into it — killing the CLI
	// breaks the pipe while the container keeps running (`docker run -d` is living proof of "the CLI exited, the
	// container kept going"; --sig-proxy cannot save it either, SIGKILL is uncatchable and cannot be forwarded). The
	// container is the cgroup, and its members are accounted for by the kernel: rm -f collects even a process that
	// escaped with setsid, which a process group cannot do.
	kill := func() error {
		rmErr := s.removeContainer(name)
		cliErr := killTree(cmd)
		if rmErr != nil {
			return rmErr
		}
		if cliErr != nil && !errors.Is(cliErr, os.ErrProcessDone) {
			return cliErr
		}
		return nil
	}
	return startWithPipes(cmd, spec, cancel, kill)
}

// isEnvKey reports whether an environment variable name is valid (starts with a letter/underscore, contains only
// letters, digits and underscores).
func isEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// execContainerNamePrefix is the name prefix of one-shot (exec path) containers.
const execContainerNamePrefix = "sandboxd-exec-"

// newExecContainerName generates a unique container name.
//
// Why a name instead of --cidfile: the name takes effect on the docker run command line itself, so the container can
// be rm -f'ed by name as soon as it is up, with no local file to manage and therefore no file lifetime to worry about.
// A name collision (theoretically only from leftovers of a previous crash) makes docker report an error explicitly
// instead of silently killing the wrong container.
func newExecContainerName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("isolate/docker: generate container name: %w", err)
	}
	return execContainerNamePrefix + hex.EncodeToString(b[:]), nil
}

// removeContainer makes the container disappear — the "kill everything" hook under docker.
//
// It looks the container up by name before removing it: the container may already have exited on its own (and been
// removed by --rm), which is not an error and should not surface as a "No such container" to the caller.
func (s *dockerSandbox) removeContainer(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), containerKillTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, s.opts.Binary, "ps", "-aq", "--filter", "name=^"+name+"$").Output()
	if err != nil {
		return fmt.Errorf("isolate/docker: locate container %s: %w", name, err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}

	args := append([]string{"rm", "-f"}, ids...)
	if rmOut, err := exec.CommandContext(ctx, s.opts.Binary, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("isolate/docker: remove container %s: %w (%s)", name, err, strings.TrimSpace(string(rmOut)))
	}
	return nil
}
