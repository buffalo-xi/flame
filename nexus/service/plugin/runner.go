package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/pluginpolicy"
	pluginsrepo "flame/nexus/shared/repository/plugins"
	"flame/tools/xlog"
)

// Invoker runs one host call. *Host is the production implementation; unit tests inject a fake so no
// python is started.
type Invoker interface {
	Invoke(ctx context.Context, tool, impl string, payload *InvokePayload) (any, error)
}

// MaxResultBytes is the byte ceiling on the result handed back to the model.
//
// Truncating beats letting one lookup blow up the context: over the ceiling, results are dropped from
// the tail and truncated=true is set.
const MaxResultBytes = 12 * 1024

// Runner turns "tool name + arguments" into one plugin call carrying the tenant configuration.
//
// Boundaries of responsibility:
//   - choosing the implementation and the fallback chain (primary -> fallbacks; config errors are not
//     downgraded)
//   - trimming the result and completing its metadata
//   - it does not discover (Registry does) and does not authorise (ExecuteTool does)
//
// Structurally it satisfies tools.PluginRunner (Has / Invoke) but deliberately does not import the
// tools package — the interface is structural, and one dependency less is one cycle less.
type Runner struct {
	host Invoker
	repo pluginsrepo.Repository
	src  Source
	pol  PolicySource // may be nil (treated as the default = open; disabling needs an explicit row)
}

// PolicySource reads the platform policy for "tenant × plugin" (whether it may be used).
//
// It is an interface for the sake of unit tests: the production implementation queries
// platform.tenant_plugins directly, while a test injects a fake and never touches the database for
// "did the platform grant this".
type PolicySource interface {
	Resolve(tenantID, pluginName string) (pluginpolicy.Policy, error)
}

// policySourceFunc lets a plain function satisfy PolicySource (the default implementation is
// pluginpolicy.Resolve).
type policySourceFunc func(tenantID, pluginName string) (pluginpolicy.Policy, error)

func (f policySourceFunc) Resolve(tenantID, pluginName string) (pluginpolicy.Policy, error) {
	return f(tenantID, pluginName)
}

// NewRunner builds a runner. host may be nil (every call then reports a config error, which is the
// graceful degradation when "the plugin is not set up").
func NewRunner(host Invoker, repo pluginsrepo.Repository, src Source) *Runner {
	return &Runner{host: host, repo: repo, src: src, pol: policySourceFunc(pluginpolicy.Resolve)}
}

// SetPolicySource replaces the platform policy source (for tests; it returns the receiver so calls can
// be chained).
func (r *Runner) SetPolicySource(p PolicySource) *Runner {
	r.pol = p
	return r
}

// NewDefaultRunner builds a runner on the default repository (main uses it, saving an import of the
// repository package).
func NewDefaultRunner(host Invoker, src Source) *Runner {
	return NewRunner(host, pluginsrepo.NewRepository(), src)
}

// Has reports whether a name is an already discovered plugin tool (manifest only).
func (r *Runner) Has(name string) bool {
	if r == nil || r.src == nil {
		return false
	}
	_, ok := r.src.Get(name)
	return ok
}

// Invoke runs one call: read the tenant config -> try the primary and then the fallback chain ->
// trim the result.
func (r *Runner) Invoke(ctx context.Context, schema, toolName string, args map[string]any) (any, error) {
	if r == nil || r.host == nil {
		return nil, &CallError{Code: CodeConfig, Message: "the plugin runner is not initialised"}
	}
	if r.src == nil {
		return nil, &CallError{Code: CodeConfig, Message: "the plugin manifest list is empty (no plugin was found)"}
	}
	if strings.TrimSpace(schema) == "" {
		return nil, &CallError{Code: CodeConfig, Message: "the tenant context is missing, so the plugin config cannot be located"}
	}

	manifest, ok := r.src.Get(toolName)
	if !ok {
		return nil, &CallError{Code: CodeConfig, Message: "plugin tool not found: " + toolName}
	}

	// The platform policy is decided before the tenant config: when the platform disabled it, the
	// message should say "ask a platform administrator", not "enable it on the plugin page" — enabling
	// it there changes nothing.
	pol := pluginpolicy.Default()
	if r.pol != nil {
		got, perr := r.pol.Resolve(schema, manifest.Name)
		if perr != nil {
			// When it cannot be read, access is **not** granted: silently falling back to the default
			// here would defeat the whole grant mechanism. A missing table usually means the rollout
			// order was wrong (the grant / gate tables were not run first), so give an actionable
			// hint instead of leaving nothing but "the plugin cannot be called".
			hint := ""
			if strings.Contains(perr.Error(), "does not exist") {
				hint = " - run nexus/platform/sql/tenant_plugins.sql and plugin_gates.sql first"
			}
			return nil, &CallError{
				Code:    CodeOther,
				Message: fmt.Sprintf("failed to read the tenant grant of plugin %s: %v%s", manifest.Name, perr, hint),
			}
		}
		pol = got
	}
	if !pol.Allowed {
		// When a gate blocks it, say whether it is a platform-wide disable or a per-tenant one: the
		// two messages have a different blast radius and call for different actions (wait for the gate
		// to lift vs ask the platform to restore this tenant).
		if pol.Blocked {
			return nil, &CallError{
				Code:    CodeConfig,
				Message: fmt.Sprintf("plugin %s is disabled platform-wide", manifest.Name),
			}
		}
		return nil, &CallError{
			Code:    CodeConfig,
			Message: fmt.Sprintf("plugin %s is disabled by the platform for this tenant (ask a platform administrator to restore it)", manifest.Name),
		}
	}

	p, err := r.repo.Get(&auth.AuthInfo{Schema: schema}, manifest.Kind, manifest.Name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, &CallError{
			Code:    CodeConfig,
			Message: fmt.Sprintf("plugin %s is not enabled (enable it and configure an implementation on the admin Plugins page)", manifest.Name),
		}
	case err != nil:
		return nil, err
	}
	if !p.IsActive {
		return nil, &CallError{Code: CodeConfig, Message: fmt.Sprintf("plugin %s is disabled", manifest.Name)}
	}

	chain, notes := buildChain(manifest, p)
	if len(chain) == 0 {
		return nil, &CallError{
			Code:    CodeConfig,
			Message: fmt.Sprintf("plugin %s has no usable implementation (the primary implementation %q is not on disk)", manifest.Name, p.Impl),
		}
	}

	var failures []string
	for _, impl := range chain {
		started := time.Now()
		res, err := r.host.Invoke(ctx, manifest.Name, impl, &InvokePayload{
			Meta:   map[string]any{"tenant": schema},
			Config: p.ImplConfig(impl),
			Args:   args,
		})
		if err == nil {
			xlog.Info("plugin call tool=%s impl=%s schema=%s took=%s", manifest.Name, impl, schema, time.Since(started))
			return truncateResult(res, impl, args, notes), nil
		}

		// A config error (missing key / implementation absent) is not fixed by trying another one, so
		// it is reported as is; everything else (auth / quota / network / other) falls back to the next
		// implementation on the chain.
		if Code(err) == CodeConfig {
			return nil, err
		}
		xlog.Error("plugin call failed tool=%s impl=%s schema=%s: %v", manifest.Name, impl, schema, err)
		failures = append(failures, impl+": "+err.Error())
		if ctx.Err() != nil {
			break
		}
	}

	return nil, &CallError{
		Code:    CodeOther,
		Message: fmt.Sprintf("every implementation of plugin %s failed - %s", manifest.Name, strings.Join(failures, "; ")),
	}
}

// buildChain assembles "primary implementation + fallback chain", recording the skipped entries on the
// way (they go into the result's notes, visible to both the model and people).
func buildChain(manifest Manifest, p *models.Plugin) (impls []string, notes []string) {
	seen := make(map[string]bool, 2)

	add := func(name, role string) {
		name = strings.TrimSpace(name)
		if name == "" {
			notes = append(notes, role+" is empty, skipped")
			return
		}
		if seen[name] {
			notes = append(notes, "duplicate "+role+" ignored: "+name)
			return
		}
		if _, ok := manifest.ImplementationOf(name); !ok {
			notes = append(notes, fmt.Sprintf("%s %q is not in the plugin manifest, skipped", role, name))
			return
		}
		seen[name] = true
		impls = append(impls, name)
	}

	add(p.Impl, "primary implementation")
	for _, fb := range p.FallbackImpls() {
		add(fb, "fallback implementation")
	}
	return impls, notes
}

// truncateResult completes the metadata and trims the result to the byte ceiling.
//
// A plugin result is by convention {results:[], answer?, notes?}; this only guarantees three things:
// provider is set, query is filled in, and the total stays within MaxResultBytes.
func truncateResult(raw any, impl string, args map[string]any, notes []string) any {
	res, ok := raw.(map[string]any)
	if !ok {
		res = map[string]any{"value": raw}
	}
	if _, ok := res["provider"]; !ok {
		res["provider"] = impl
	}
	if _, ok := res["query"]; !ok {
		res["query"] = argString(args, "query")
	}
	if len(notes) > 0 {
		joined := make([]any, 0, len(notes))
		if existing, ok := res["notes"].([]any); ok {
			joined = append(joined, existing...)
		} else if existing, ok := res["notes"].([]string); ok {
			for _, n := range existing {
				joined = append(joined, n)
			}
		}
		for _, n := range notes {
			joined = append(joined, n)
		}
		res["notes"] = joined
	}

	if b, err := json.Marshal(res); err == nil && len(b) <= MaxResultBytes {
		return res
	}

	if items, ok := res["results"].([]any); ok {
		for len(items) > 0 {
			items = items[:len(items)-1]
			res["results"] = items
			res["truncated"] = true
			if b, err := json.Marshal(res); err == nil && len(b) <= MaxResultBytes {
				return res
			}
		}
	}

	res["truncated"] = true
	if b, err := json.Marshal(res); err == nil && len(b) > MaxResultBytes {
		// The extreme case: a non-results field (answer / notes) is over the ceiling on its own, so it
		// is cut down to a summary
		return map[string]any{
			"provider":  res["provider"],
			"query":     res["query"],
			"results":   []any{},
			"truncated": true,
			"notes":     []any{fmt.Sprintf("the result exceeded %d bytes, the body was dropped", MaxResultBytes)},
		}
	}
	return res
}

// argString reads a string out of args (empty when absent); only used to fill in query.
func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return s
}
