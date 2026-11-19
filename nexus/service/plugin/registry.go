package plugin

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"flame/nexus/shared/plugininfo"
	"flame/tools/xlog"
)

// KindTools is the tools plugin kind. It is the only kind supported today and the reference value for
// both the host scan and the manifest validation. It holds the same value as
// repository/plugins.KindTools and pluginpolicy.KindTools (each of the three layers declares it and
// none imports another).
const KindTools = "tools"

// SupportedKinds is the set of plugin kinds supported today.
//
// An unknown kind is only recorded, never loaded: adding a kind later (sources / channels ...) means
// adding the loader to the host first and then putting the kind in here — existing plugins and grant
// data are unaffected.
var SupportedKinds = map[string]bool{KindTools: true}

// Snapshot is the plugin manifest as of one moment.
type Snapshot struct {
	Tools  []Manifest
	Errors []ScanError
	At     time.Time
	// Err is why the latest refresh failed as a whole. While it is non-nil Tools still holds the
	// result of the last success (keeping the old result rather than clearing it: disk being
	// temporarily unreadable does not mean the plugins should stop working).
	Err error
}

// Registry is the in-memory view of the plugin manifest: fetched once at startup, refreshed
// periodically afterwards (a directory mtime change triggers a refresh earlier).
//
// Why the manifest is not persisted: disk is the only source of truth. Persisting would add a copy to
// keep in sync, and a plugin upgrade that changed the schema would wait a whole sync round; rescanning
// every round is at worst one refresh interval behind.
type Registry struct {
	host    *Host
	builtin map[string]bool // built-in tool names (lower case): a plugin tool with the same name is rejected

	mu   sync.RWMutex
	snap Snapshot
	sig  string // directory signature; unchanged means the scan is skipped (saving one python start)
}

// NewRegistry builds a registry. builtinNames is the built-in tool names (tools.Names()).
func NewRegistry(host *Host, builtinNames []string) *Registry {
	names := make(map[string]bool, len(builtinNames))
	for _, n := range builtinNames {
		names[strings.ToLower(n)] = true
	}
	return &Registry{host: host, builtin: names}
}

// EnsureDir makes sure the plugin root exists (creating an empty directory when it does not).
//
// It deliberately does not refuse to start because "the directory has not been created yet": plugins
// are an optional capability, and forgetting to create the directory during deployment must not keep
// the whole nexus from starting.
func EnsureDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("the plugin directory is empty")
	}
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("the plugin path is not a directory: %s", dir)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create the plugin directory %s: %w", dir, err)
	}
	xlog.Info("the plugin directory did not exist; created an empty one: %s", dir)
	return nil
}

// Refresh rescans the directory.
//
// With force=false and an unchanged directory signature it returns immediately (no python subprocess).
// A failed scan does not overwrite the last success; it only records Err.
func (r *Registry) Refresh(ctx context.Context, force bool) error {
	sig := dirSignature(r.host.Dir)

	r.mu.RLock()
	unchanged := sig != "" && sig == r.sig
	r.mu.RUnlock()
	if !force && unchanged {
		return nil
	}

	res, err := r.host.List(ctx)
	if err != nil {
		r.mu.Lock()
		r.snap.Err = err
		r.mu.Unlock()
		xlog.Error("plugin manifest scan failed dir=%s: %v", r.host.Dir, err)
		return err
	}

	tools, errs := r.validate(res)
	for _, e := range errs {
		xlog.Error("plugin manifest issue %s: %s", e.Path, e.Error)
	}

	r.mu.Lock()
	r.snap = Snapshot{Tools: tools, Errors: errs, At: time.Now()}
	r.sig = sig
	r.mu.Unlock()

	xlog.Info("plugin manifests refreshed dir=%s tools=%d errors=%d", r.host.Dir, len(tools), len(errs))
	return nil
}

// validate filters out unusable or conflicting plugins and records the reasons in errors.
//
// Three hard rules, all decided at load time rather than guessed again per call:
//  1. kind must be in SupportedKinds;
//  2. a tool name must not clash with a built-in tool (otherwise the dispatch in ExecuteTool is
//     ambiguous);
//  3. a tool name must be globally unique (across kinds too) — both the grant ref and the tool name
//     use just this one name.
func (r *Registry) validate(res *ListResult) ([]Manifest, []ScanError) {
	errs := make([]ScanError, 0, len(res.Errors)+2)
	errs = append(errs, res.Errors...)

	seen := make(map[string]string, len(res.Tools))
	tools := make([]Manifest, 0, len(res.Tools))

	for _, m := range res.Tools {
		path := m.Kind + "/" + m.Name
		name := strings.TrimSpace(m.Name)
		switch {
		case !SupportedKinds[m.Kind]:
			errs = append(errs, ScanError{
				Path:  path,
				Error: fmt.Sprintf("unsupported plugin kind %q (only tools is supported), skipped", m.Kind),
			})
			continue
		case name == "":
			errs = append(errs, ScanError{Path: path, Error: "empty tool name, skipped"})
			continue
		case r.builtin[strings.ToLower(name)]:
			errs = append(errs, ScanError{
				Path:  path,
				Error: fmt.Sprintf("tool name %q clashes with a builtin tool, skipped (rename the plugin)", name),
			})
			continue
		}
		key := strings.ToLower(name)
		if prev, dup := seen[key]; dup {
			errs = append(errs, ScanError{
				Path:  path,
				Error: fmt.Sprintf("tool name %q duplicates %s, skipped (tool names must be globally unique)", name, prev),
			})
			continue
		}
		seen[key] = path

		if m.DisplayName == "" {
			m.DisplayName = name
		}
		tools = append(tools, m)
	}

	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, errs
}

// Snapshot returns the current snapshot (the slice is a read-only view; callers must not modify it).
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snap
}

// Get looks a manifest up by tool name (case-insensitive).
func (r *Registry) Get(name string) (Manifest, bool) {
	snap := r.Snapshot()
	for _, m := range snap.Tools {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return Manifest{}, false
}

// Has reports whether a name is an already discovered plugin tool.
func (r *Registry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// Start launches the background refresh goroutine. An interval <= 0 means one minute.
func (r *Registry) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = r.Refresh(ctx, false)
			}
		}
	}()
}

// ===== package-level singleton (the same registration style as tools.SetSandbox / sandboxinfo) =====

// Source is a read-only view of the plugin manifest. Registry is the default implementation; unit
// tests can inject a fake so they need not start a python subprocess just to answer "is there a
// plugin in the directory".
type Source interface {
	Snapshot() Snapshot
	Get(name string) (Manifest, bool)
}

// sourceBox wraps one level: atomic.Value requires the stored concrete type to stay the same.
type sourceBox struct{ s Source }

var defaultSource atomic.Value

// currentRegistry is the concrete registry (used by the admin refresh).
var currentRegistry atomic.Pointer[Registry]

// SetSource injects the global manifest source (production passes *Registry, tests may pass a fake;
// nil clears it).
func SetSource(s Source) { defaultSource.Store(sourceBox{s: s}) }

// DefaultSource returns the global manifest source; it is nil when nothing was injected (the tool
// catalogue then shows no plugin).
func DefaultSource() Source {
	if v, ok := defaultSource.Load().(sourceBox); ok {
		return v.s
	}
	return nil
}

// ===== singleton of the concrete registry (the admin "refresh" needs it; the interface view is not enough) =====

// SetDefault injects the global registry (called by main at startup; it also takes over SetSource's job).
//
// It additionally exposes the manifest to shared/plugininfo: the platform admin console has to list
// "which plugins can be granted to a tenant", yet the boundary rules of nexus/platform forbid
// importing this package (see platform/README.md), so it goes through the minimal view in shared.
func SetDefault(r *Registry) {
	currentRegistry.Store(r)
	SetSource(r)
	plugininfo.SetSource(infoAdapter{r: r})
}

// infoAdapter maps Registry's manifest onto the minimal shared/plugininfo structure.
type infoAdapter struct{ r *Registry }

func (a infoAdapter) Tools() []plugininfo.Tool {
	if a.r == nil {
		return []plugininfo.Tool{}
	}
	snap := a.r.Snapshot()
	out := make([]plugininfo.Tool, 0, len(snap.Tools))
	for _, m := range snap.Tools {
		impls := make([]plugininfo.Implementation, 0, len(m.Implementations))
		for _, im := range m.Implementations {
			impls = append(impls, plugininfo.Implementation{Name: im.Name, DisplayName: im.DisplayName})
		}
		out = append(out, plugininfo.Tool{
			Name:            m.Name,
			DisplayName:     m.DisplayName,
			Description:     m.Description,
			Version:         m.Version,
			Kind:            m.Kind,
			Implementations: impls,
		})
	}
	return out
}

// Current returns the global registry (*Registry); it is nil when a test injected a fake Source.
func Current() *Registry { return currentRegistry.Load() }

// dirSignature sums up the mtimes of the directory tree, to decide whether anything changed.
//
// Only the three levels kind/tool/impl are considered: deeper content (__pycache__, data files a
// plugin ships) does not affect the manifest, so there is no reason to rescan for it.
func dirSignature(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			// Skip whole trees such as __pycache__ (they only add noise and do not affect the manifest)
			if strings.HasPrefix(d.Name(), "__") {
				return filepath.SkipDir
			}
			depth := 0
			if rel != "." {
				depth = strings.Count(rel, string(filepath.Separator)) + 1
			}
			if depth >= 4 { // nothing below the fourth level (impl/subdirectory) matters
				return filepath.SkipDir
			}
			// Directories record only their name, not their mtime: a Python import creates
			// __pycache__ next to the file, which changes the parent directory's mtime while the
			// manifest is unchanged — recording mtime would rescan every round.
			b.WriteString(rel)
			b.WriteByte('/')
			b.WriteByte(';')
			return nil
		}
		b.WriteString(rel)
		if info, ierr := d.Info(); ierr == nil {
			b.WriteString(info.ModTime().UTC().Format(time.RFC3339Nano))
		}
		b.WriteByte(';')
		return nil
	})
	return b.String()
}
