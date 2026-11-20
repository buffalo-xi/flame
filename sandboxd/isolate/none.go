package isolate

import (
	"context"
	"os/exec"

	"flame/tools/sandbox"
)

// noneSandbox is the passthrough implementation: it does no isolation at all and amounts to a plain exec.Command.
//
// Because there is no isolation, the "in-sandbox paths" in argv/dir are translated back into host paths according to
// Mounts before execution, so callers only ever write one set of paths.
//
// Resource caps: only Timeout in Limits is real (ctx can enforce it); MemoryMB / CPUCount / MaxPIDs / MaxFileMB cannot
// be applied at all in this tier — there is no cgroup / namespace / Job Object available. That is the definition of
// "host passthrough", not a missing implementation; sandboxd logs a note about it at startup.
type noneSandbox struct{}

// NewNone creates the passthrough isolation implementation (for development / debugging only).
func NewNone() sandbox.Sandbox { return &noneSandbox{} }

func (s *noneSandbox) Name() string { return NoneName }

func (s *noneSandbox) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	if len(spec.Argv) == 0 {
		return nil, errEmptyArgv
	}

	// Timeout: wrapped only when specified; startWithPipes attaches cancel to the process and releases it once it
	// ends.
	var cancel context.CancelFunc
	if spec.Limits.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, spec.Limits.Timeout)
	}

	// Translate the "in-sandbox paths" back into host paths
	argv := TranslateArgv(spec.Mounts, spec.Argv)
	dir := TranslateToHost(spec.Mounts, spec.Dir)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}

	// kill nil means the default approach (the process group). none has no cgroup / namespace, so that is its only
	// means — therefore its guarantee is only "clean inside the group", not "every descendant killed".
	return startWithPipes(cmd, spec, cancel, nil)
}
