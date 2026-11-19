// Package sandbox provides a unified abstraction for "isolated process execution".
//
// It covers two shapes of use (both share Start; only how the caller drives Process differs):
//  1. pooled long-lived process: start once and keep interacting over stdin/stdout (workflow script pool)
//  2. one-shot command: start → write input → read output → wait for exit (agent exec tool)
//
// This package keeps only the abstraction plus the remote client:
//   - the nexus side delegates execution to sandboxd through NewRemote and does no isolation itself;
//   - the isolation implementations (none / nsjail / docker) live in sandboxd's isolate package and
//     implement the Sandbox interface here as well.
//
// Path semantics: every path in Spec is a "sandbox-internal" path; the isolation implementation
// translates Mounts into mount arguments (the none implementation translates them back to host paths).
package sandbox

import (
	"context"
	"io"
	"time"
)

// Sandbox is one isolation implementation.
type Sandbox interface {
	// Name is the implementation identifier: none / nsjail / docker.
	Name() string
	// Start starts a process inside the sandbox.
	// Long-lived or one-shot is the caller's decision: for long-lived keep stdin open and read/write
	// repeatedly; for one-shot write, Close and Wait (or use Run directly).
	Start(ctx context.Context, spec *Spec) (Process, error)
}

// Process is a handle to a process inside the sandbox (same shape as *exec.Cmd, so both forms reuse it).
type Process interface {
	Stdin() io.WriteCloser // the caller closes it to signal end of input
	Stdout() io.ReadCloser // process standard output
	Wait() error           // wait for exit (a non-zero code is normally *exec.ExitError)
	Kill() error           // force termination
}

// Spec is the intent behind one process start inside the sandbox.
//
// The intent is expressed one of two ways, never both:
//   - language path (Lang non-empty): say only "run the script of this language"; sandboxd resolves the
//     interpreter entry point for the current isolation mode (see Script / Args);
//   - direct path (Lang empty): Argv is entirely the caller's (shell commands, a bundled interpreter).
type Spec struct {
	// Lang is the language name (optional). When non-empty the language path is used: sandboxd resolves
	// the interpreter entry point by mode.
	//
	// Why it is needed: the interpreter entry name changes with the mode (none looks at the host PATH,
	// nsjail at the chroot, docker inside the image), and only sandboxd knows what the image actually
	// calls it. A caller assembling argv itself would hard-code one belief, and the moment it drifts you
	// get "the same script runs over the duplex channel but not the one-shot one".
	Lang string
	// Script is the script's path inside the sandbox; only used when Lang is non-empty.
	Script string
	// Args are arguments passed to the script; only used when Lang is non-empty (appended after Script).
	Args []string
	// Argv is the full command line (executable + arguments), as sandbox-internal paths.
	// Mutually exclusive with Lang: when Lang is non-empty this field must be empty.
	Argv   []string
	Dir    string    // working directory (sandbox-internal path)
	Env    []string  // environment variables (nil leaves the inherited ones untouched)
	Stdin  []byte    // input for the one-shot case (used by Run; long-lived uses Process.Stdin)
	Stderr io.Writer // where stderr goes (nil = io.Discard)
	Mounts []Mount   // mount table (ignored by the none implementation)
	Limits Limits    // resource limits

	// Egress is the network intent: nil = no opinion (deployment default), false = explicitly offline,
	// true = wants outbound network.
	//
	// This is an **intent**, not an outcome: sandboxd decides against the deployment ceiling before it
	// becomes an isolation argument (with network=off, true is rejected before the process starts).
	// isolate only interprets "should the network be turned off", it does not evaluate policy.
	//
	// Only meaningful for Start (one-shot): a long-lived unit created by Acquire is reused across
	// requests and its network is fixed at creation, so later calls cannot change it — callers of the
	// duplex channel should not set this field either.
	Egress *bool
}

// Mount maps a host path to a mount point inside the sandbox.
type Mount struct {
	Host     string
	Guest    string
	ReadOnly bool
}

// Limits are resource caps (a zero value means "no extra limit on this dimension"; the implementation
// decides its default).
type Limits struct {
	MemoryMB int
	CPUCount int
	MaxPIDs  int
	// MaxFileMB is the per-file size cap in MB.
	MaxFileMB int
	Timeout   time.Duration
	// MaxOutputBytes caps what is collected per stream (stdout / stderr), so huge output cannot exhaust
	// memory. Used by Run only; 0 means unlimited.
	MaxOutputBytes int64
}

// Result is the outcome of a one-shot execution.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	// Truncated reports whether output was cut off by MaxOutputBytes.
	Truncated bool
}

// MergeLimits overrides the defaults with the non-zero fields of spec.
func MergeLimits(def, override Limits) Limits {
	out := def
	if override.MemoryMB > 0 {
		out.MemoryMB = override.MemoryMB
	}
	if override.CPUCount > 0 {
		out.CPUCount = override.CPUCount
	}
	if override.MaxPIDs > 0 {
		out.MaxPIDs = override.MaxPIDs
	}
	if override.MaxFileMB > 0 {
		out.MaxFileMB = override.MaxFileMB
	}
	if override.Timeout > 0 {
		out.Timeout = override.Timeout
	}
	if override.MaxOutputBytes > 0 {
		out.MaxOutputBytes = override.MaxOutputBytes
	}
	return out
}
