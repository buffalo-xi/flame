package sandbox

import "context"

// Reusable is implemented by backends that can reuse an isolation unit (currently docker only).
//
// Why: establishing an isolation boundary costs far more than starting an interpreter
// (a docker container 300ms–1s; a python/node interpreter 20–60ms). A python/js call that takes "tens
// of milliseconds to a second" only needs "no residue across requests" — it does not need the whole
// boundary rebuilt every time.
//
// So the "isolation unit" is split from the "execution process": the unit is reused, the process is
// created per request. Backends that do not implement this interface (none / nsjail) degrade to L3
// automatically (unit and process live and die together), see nexus/docs/sandbox-design.md §9.
type Reusable interface {
	// Acquire creates an idle unit: the isolation boundary is ready and nothing is running inside it.
	// spec describes how to build the unit (for docker: image, mounts, limits, network).
	Acquire(ctx context.Context, spec *Spec) (Unit, error)
}

// Unit is one reusable unit: the isolation boundary stays up, and Exec starts new processes inside it.
//
// Lifecycle: Acquire creates it → Exec any number of times (one short-lived process each) → Close
// destroys it. The Process returned by Exec has exactly the semantics of Sandbox.Start (stdin/stdout as
// the frame protocol needs them, and an exit code normalised to *ExitError).
//
// Two reuse-related rules (see the design document §9.3):
//   - killing a process inside the unit is not destroying the unit: close stdin and, if needed, kill it
//     from inside; only Close destroys the unit itself;
//   - once a unit is untrustworthy (still dirty after the kill, or Exec failed) Close and discard it
//     rather than reuse it — "not clean" is the worst premise for reuse.
type Unit interface {
	// Exec starts a process inside the unit.
	// spec.Dir is a path inside the unit (it must already exist); writable directories are prepared by
	// the caller under the mount points.
	Exec(ctx context.Context, spec *Spec) (Process, error)
	// Close destroys the unit, including any processes left inside it.
	Close() error
	// Name is the unit identifier (a container ID, say), used in logs and troubleshooting.
	Name() string
}

// OrphanCleaner is implemented by backends that can leave recognisable residue on the host (docker
// containers).
//
// When a process is force-killed or the host loses power, a unit may be left behind without Close ever
// running. Backends reclaim their own leftovers at startup by their own marker (a container label, say)
// so containers cannot pile up forever.
type OrphanCleaner interface {
	// CleanOrphans reclaims units left behind by this service and returns how many were removed.
	CleanOrphans(ctx context.Context) (int, error)
}
