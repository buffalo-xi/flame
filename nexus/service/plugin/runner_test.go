package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// ===== fakes (no python, no DB) =====

type fakeInvoker struct {
	calls []string
	fn    func(impl string) (any, error)
}

func (f *fakeInvoker) Invoke(_ context.Context, _ string, impl string, _ *InvokePayload) (any, error) {
	f.calls = append(f.calls, impl)
	if f.fn == nil {
		return map[string]any{"results": []any{}}, nil
	}
	return f.fn(impl)
}

type fakeSource struct{ tools []Manifest }

func (f fakeSource) Snapshot() Snapshot { return Snapshot{Tools: f.tools} }

func (f fakeSource) Get(name string) (Manifest, bool) {
	for _, m := range f.tools {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return Manifest{}, false
}

// fakeRepo stores rows by name only: every case in this package goes through the kind=tools path, and
// the (kind, name) isolation is covered by the toolgrant double and the real repository.
type fakeRepo struct{ rows map[string]*models.Plugin }

func (f fakeRepo) Get(_ *auth.AuthInfo, _, name string) (*models.Plugin, error) {
	if p, ok := f.rows[name]; ok {
		return p, nil
	}
	return nil, sql.ErrNoRows
}

func (f fakeRepo) List(_ *auth.AuthInfo) ([]models.Plugin, error) { return nil, nil }
func (f fakeRepo) Upsert(_ *auth.AuthInfo, p *models.Plugin) error {
	f.rows[p.Name] = p
	return nil
}
func (f fakeRepo) SetActive(_ *auth.AuthInfo, _, name string, active bool) error {
	p, ok := f.rows[name]
	if !ok {
		return sql.ErrNoRows
	}
	p.IsActive = active
	return nil
}
func (f fakeRepo) Delete(_ *auth.AuthInfo, _, name string) error {
	delete(f.rows, name)
	return nil
}
func (f fakeRepo) TouchUsed(_ *auth.AuthInfo, _, _ string) error   { return nil }
func (f fakeRepo) SetError(_ *auth.AuthInfo, _, _, _ string) error { return nil }

func runnerManifest(impls ...string) Manifest {
	out := make([]Implementation, 0, len(impls))
	for _, im := range impls {
		out = append(out, Implementation{Name: im, DisplayName: im})
	}
	return Manifest{
		Kind:            "tools",
		Name:            "web_search",
		DisplayName:     "Web search",
		Description:     "Searches the web",
		Parameters:      map[string]any{"type": "object"},
		Implementations: out,
	}
}

// newRunner builds the runner used by the cases: the platform policy defaults to "granted and
// unrestricted" (the opposite of the production default), so that not every case has to spell out a
// grant; a case that asserts quotas or grants overrides it with SetPolicySource.
func newRunner(inv Invoker, m Manifest, p *models.Plugin) *Runner {
	rows := map[string]*models.Plugin{}
	if p != nil {
		rows[p.Name] = p
	}
	return NewRunner(inv, fakeRepo{rows: rows}, fakeSource{tools: []Manifest{m}}).
		SetPolicySource(allowAll())
}

// ===== cases =====

func TestRunnerInvokePrimary(t *testing.T) {
	inv := &fakeInvoker{fn: func(string) (any, error) {
		return map[string]any{"results": []any{map[string]any{"title": "a"}}}, nil
	}}
	r := newRunner(inv, runnerManifest("searxng", "tavily"), &models.Plugin{
		Name: "web_search", Impl: "searxng", IsActive: true,
	})

	raw, err := r.Invoke(context.Background(), "t0", "web_search", map[string]any{"query": "nexus"})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if len(inv.calls) != 1 || inv.calls[0] != "searxng" {
		t.Fatalf("implementations called = %v, want searxng only", inv.calls)
	}
	res, _ := raw.(map[string]any)
	if res["provider"] != "searxng" {
		t.Errorf("provider = %v", res["provider"])
	}
	if res["query"] != "nexus" {
		t.Errorf("query was not filled in: %v", res["query"])
	}
}

func TestRunnerFallsBackOnNetworkError(t *testing.T) {
	inv := &fakeInvoker{fn: func(impl string) (any, error) {
		if impl == "tavily" {
			return nil, &CallError{Code: CodeNetwork, Message: "cannot connect"}
		}
		return map[string]any{"results": []any{}}, nil
	}}
	r := newRunner(inv, runnerManifest("tavily", "searxng"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
		Fallbacks: mustJSONB(`["searxng"]`),
	})

	raw, err := r.Invoke(context.Background(), "t0", "web_search", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("the fallback should have succeeded, got %v", err)
	}
	if strings.Join(inv.calls, ",") != "tavily,searxng" {
		t.Fatalf("call order = %v", inv.calls)
	}
	res, _ := raw.(map[string]any)
	if res["provider"] != "searxng" {
		t.Errorf("provider = %v, want the fallback searxng", res["provider"])
	}
}

func TestRunnerDoesNotFallbackOnConfigError(t *testing.T) {
	inv := &fakeInvoker{fn: func(string) (any, error) {
		return nil, &CallError{Code: CodeConfig, Message: "api_key is missing"}
	}}
	r := newRunner(inv, runnerManifest("tavily", "searxng"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
		Fallbacks: mustJSONB(`["searxng"]`),
	})

	_, err := r.Invoke(context.Background(), "t0", "web_search", nil)
	if err == nil {
		t.Fatal("a missing key should fail right away")
	}
	if Code(err) != CodeConfig {
		t.Fatalf("error code = %q, want config", Code(err))
	}
	if len(inv.calls) != 1 {
		t.Fatalf("a config error must not fall back, but calls were %v", inv.calls)
	}
}

func TestRunnerRejectsDisabledOrMissingConfig(t *testing.T) {
	inv := &fakeInvoker{}

	disabled := newRunner(inv, runnerManifest("searxng"), &models.Plugin{
		Name: "web_search", Impl: "searxng", IsActive: false,
	})
	if _, err := disabled.Invoke(context.Background(), "t0", "web_search", nil); Code(err) != CodeConfig {
		t.Fatalf("a disabled plugin should report config, got %v", err)
	}

	missing := newRunner(inv, runnerManifest("searxng"), nil)
	if _, err := missing.Invoke(context.Background(), "t0", "web_search", nil); Code(err) != CodeConfig {
		t.Fatalf("an unconfigured plugin should report config, got %v", err)
	}

	if len(inv.calls) != 0 {
		t.Fatalf("no implementation should have been called: %v", inv.calls)
	}
}

func TestRunnerUndiscoveredTool(t *testing.T) {
	r := newRunner(&fakeInvoker{}, runnerManifest("searxng"), &models.Plugin{
		Name: "web_search", Impl: "searxng", IsActive: true,
	})
	if _, err := r.Invoke(context.Background(), "t0", "no_such_tool", nil); Code(err) != CodeConfig {
		t.Fatalf("an unknown tool should report config, got %v", err)
	}
	if r.Has("no_such_tool") {
		t.Error("Has must not match an unknown tool")
	}
	if !r.Has("WEB_SEARCH") {
		t.Error("Has should be case-insensitive")
	}
}

// The fallback chain names an implementation that is not on disk: skip it and record it in notes
// rather than failing the whole chain.
func TestRunnerSkipsGhostImplementation(t *testing.T) {
	inv := &fakeInvoker{fn: func(impl string) (any, error) {
		if impl == "tavily" {
			return nil, &CallError{Code: CodeNetwork, Message: "cannot connect"}
		}
		return map[string]any{"results": []any{}}, nil
	}}
	r := newRunner(inv, runnerManifest("tavily", "searxng"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
		Fallbacks: mustJSONB(`["ghost","searxng"]`),
	})

	raw, err := r.Invoke(context.Background(), "t0", "web_search", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if strings.Join(inv.calls, ",") != "tavily,searxng" {
		t.Fatalf("call order = %v (ghost must not be called)", inv.calls)
	}
	res, _ := raw.(map[string]any)
	notes, _ := res["notes"].([]any)
	found := false
	for _, n := range notes {
		if s, _ := n.(string); strings.Contains(s, "not in the plugin manifest") {
			found = true
		}
	}
	if !found {
		t.Errorf("the skipped implementation was not explained: %v", res["notes"])
	}
}

func TestRunnerTruncatesLargeResult(t *testing.T) {
	big := make([]any, 0, 100)
	for i := 0; i < 100; i++ {
		big = append(big, map[string]any{
			"title":   fmt.Sprintf("title %03d", i),
			"url":     fmt.Sprintf("https://example.com/%03d", i),
			"snippet": strings.Repeat("snippet body", 40),
		})
	}
	inv := &fakeInvoker{fn: func(string) (any, error) {
		return map[string]any{"results": big}, nil
	}}
	r := newRunner(inv, runnerManifest("searxng"), &models.Plugin{
		Name: "web_search", Impl: "searxng", IsActive: true,
	})

	raw, err := r.Invoke(context.Background(), "t0", "web_search", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	res, _ := raw.(map[string]any)
	if res["truncated"] != true {
		t.Fatal("an over-limit result should be marked truncated")
	}
	items, _ := res["results"].([]any)
	if len(items) >= 100 {
		t.Fatalf("the result was not trimmed: %d entries", len(items))
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxResultBytes {
		t.Fatalf("after trimming it is still %d bytes, over the %d limit", len(b), MaxResultBytes)
	}
}

func mustJSONB(s string) xsql.JSONB {
	return xsql.JSONB{Data: json.RawMessage(s)}
}
