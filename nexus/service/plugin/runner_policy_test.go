package plugin

import (
	"context"
	"testing"

	"flame/nexus/shared/models"
	"flame/nexus/shared/pluginpolicy"
)

// fakePolicy 平台策略替身：不连库，直接给一个固定策略。
type fakePolicy struct {
	p   pluginpolicy.Policy
	err error
}

func (f fakePolicy) Resolve(string, string) (pluginpolicy.Policy, error) {
	return f.p, f.err
}

// allowAll 「可用」—— 与生产缺省一致（2026-11-02 起缺省开放），
// 显式构造是为了让用例不依赖 Default() 的具体值。
func allowAll() fakePolicy { return fakePolicy{p: pluginpolicy.Policy{Allowed: true}} }

// 平台对本租户停用（显式 is_allowed=false）：不该起进程。
func TestRunnerRejectsPluginStoppedForTenant(t *testing.T) {
	inv := &fakeInvoker{}
	r := newRunner(inv, runnerManifest("tavily"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
	}).SetPolicySource(fakePolicy{p: pluginpolicy.Policy{Allowed: false}})

	_, err := r.Invoke(context.Background(), "t0", "web_search", nil)
	if Code(err) != CodeConfig {
		t.Fatalf("被停用应报 config，实际 %v", err)
	}
	if len(inv.calls) != 0 {
		t.Fatalf("被停用不该触达宿主，实际调了 %v", inv.calls)
	}
}

// 策略读不出来必须 fail-closed：静默放行等于授权机制失效。
func TestRunnerPolicyErrorFailsClosed(t *testing.T) {
	inv := &fakeInvoker{}
	r := newRunner(inv, runnerManifest("tavily"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
	}).SetPolicySource(fakePolicy{err: context.DeadlineExceeded})

	_, err := r.Invoke(context.Background(), "t0", "web_search", nil)
	if Code(err) != CodeOther {
		t.Fatalf("策略读取失败应报 other（不放行），实际 %v", err)
	}
	if len(inv.calls) != 0 {
		t.Fatalf("策略读失败不该触达宿主，实际调了 %v", inv.calls)
	}
}
