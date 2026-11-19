package plugin

import (
	"context"
	"testing"

	"flame/nexus/shared/models"
	"flame/nexus/shared/pluginpolicy"
)

// fakePolicy is a platform policy double: no database, just a fixed policy.
type fakePolicy struct {
	p   pluginpolicy.Policy
	err error
}

func (f fakePolicy) Resolve(string, string) (pluginpolicy.Policy, error) {
	return f.p, f.err
}

// allowAll is "usable" — the same as the production default (open by default since 2026-11-02); it is
// constructed explicitly so the cases do not depend on the concrete value of Default().
func allowAll() fakePolicy { return fakePolicy{p: pluginpolicy.Policy{Allowed: true}} }

// The platform disabled the plugin for this tenant (explicit is_allowed=false): no process may start.
func TestRunnerRejectsPluginStoppedForTenant(t *testing.T) {
	inv := &fakeInvoker{}
	r := newRunner(inv, runnerManifest("tavily"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
	}).SetPolicySource(fakePolicy{p: pluginpolicy.Policy{Allowed: false}})

	_, err := r.Invoke(context.Background(), "t0", "web_search", nil)
	if Code(err) != CodeConfig {
		t.Fatalf("a disabled plugin should report config, got %v", err)
	}
	if len(inv.calls) != 0 {
		t.Fatalf("a disabled plugin must not reach the host, but called %v", inv.calls)
	}
}

// An unreadable policy must fail closed: silently allowing it would defeat the whole grant mechanism.
func TestRunnerPolicyErrorFailsClosed(t *testing.T) {
	inv := &fakeInvoker{}
	r := newRunner(inv, runnerManifest("tavily"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
	}).SetPolicySource(fakePolicy{err: context.DeadlineExceeded})

	_, err := r.Invoke(context.Background(), "t0", "web_search", nil)
	if Code(err) != CodeOther {
		t.Fatalf("a failed policy read should report other (no access), got %v", err)
	}
	if len(inv.calls) != 0 {
		t.Fatalf("a failed policy read must not reach the host, but called %v", inv.calls)
	}
}
