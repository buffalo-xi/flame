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

// ===== 假实现（不起 python、不连 DB） =====

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

// fakeRepo 只按 name 存：本包用例全部走 kind=tools 一条路径，
// (kind, name) 的隔离由 toolgrant 的替身与真实仓储负责。
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
		DisplayName:     "网络搜索",
		Description:     "搜索网络",
		Parameters:      map[string]any{"type": "object"},
		Implementations: out,
	}
}

// newRunner 用例里的执行器：平台策略默认「已开通 + 不限」（与生产缺省相反），
// 避免每个用例都要写一遍授权；需要额度/授权相关的断言时自己 SetPolicySource 覆盖。
func newRunner(inv Invoker, m Manifest, p *models.Plugin) *Runner {
	rows := map[string]*models.Plugin{}
	if p != nil {
		rows[p.Name] = p
	}
	return NewRunner(inv, fakeRepo{rows: rows}, fakeSource{tools: []Manifest{m}}).
		SetPolicySource(allowAll())
}

// ===== 用例 =====

func TestRunnerInvokePrimary(t *testing.T) {
	inv := &fakeInvoker{fn: func(string) (any, error) {
		return map[string]any{"results": []any{map[string]any{"title": "a"}}}, nil
	}}
	r := newRunner(inv, runnerManifest("searxng", "tavily"), &models.Plugin{
		Name: "web_search", Impl: "searxng", IsActive: true,
	})

	raw, err := r.Invoke(context.Background(), "t0", "web_search", map[string]any{"query": "nexus"})
	if err != nil {
		t.Fatalf("invoke 失败: %v", err)
	}
	if len(inv.calls) != 1 || inv.calls[0] != "searxng" {
		t.Fatalf("调用实现 = %v，期望只调 searxng", inv.calls)
	}
	res, _ := raw.(map[string]any)
	if res["provider"] != "searxng" {
		t.Errorf("provider = %v", res["provider"])
	}
	if res["query"] != "nexus" {
		t.Errorf("query 未回填: %v", res["query"])
	}
}

func TestRunnerFallsBackOnNetworkError(t *testing.T) {
	inv := &fakeInvoker{fn: func(impl string) (any, error) {
		if impl == "tavily" {
			return nil, &CallError{Code: CodeNetwork, Message: "连不上"}
		}
		return map[string]any{"results": []any{}}, nil
	}}
	r := newRunner(inv, runnerManifest("tavily", "searxng"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
		Fallbacks: mustJSONB(`["searxng"]`),
	})

	raw, err := r.Invoke(context.Background(), "t0", "web_search", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("应当降级成功，实际 %v", err)
	}
	if strings.Join(inv.calls, ",") != "tavily,searxng" {
		t.Fatalf("调用顺序 = %v", inv.calls)
	}
	res, _ := raw.(map[string]any)
	if res["provider"] != "searxng" {
		t.Errorf("provider = %v，期望走兜底的 searxng", res["provider"])
	}
}

func TestRunnerDoesNotFallbackOnConfigError(t *testing.T) {
	inv := &fakeInvoker{fn: func(string) (any, error) {
		return nil, &CallError{Code: CodeConfig, Message: "缺少 api_key"}
	}}
	r := newRunner(inv, runnerManifest("tavily", "searxng"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
		Fallbacks: mustJSONB(`["searxng"]`),
	})

	_, err := r.Invoke(context.Background(), "t0", "web_search", nil)
	if err == nil {
		t.Fatal("缺 key 应当直接报错")
	}
	if Code(err) != CodeConfig {
		t.Fatalf("错误码 = %q，期望 config", Code(err))
	}
	if len(inv.calls) != 1 {
		t.Fatalf("config 错误不该降级，实际调用 %v", inv.calls)
	}
}

func TestRunnerRejectsDisabledOrMissingConfig(t *testing.T) {
	inv := &fakeInvoker{}

	disabled := newRunner(inv, runnerManifest("searxng"), &models.Plugin{
		Name: "web_search", Impl: "searxng", IsActive: false,
	})
	if _, err := disabled.Invoke(context.Background(), "t0", "web_search", nil); Code(err) != CodeConfig {
		t.Fatalf("停用插件应当报 config，实际 %v", err)
	}

	missing := newRunner(inv, runnerManifest("searxng"), nil)
	if _, err := missing.Invoke(context.Background(), "t0", "web_search", nil); Code(err) != CodeConfig {
		t.Fatalf("未配置插件应当报 config，实际 %v", err)
	}

	if len(inv.calls) != 0 {
		t.Fatalf("不该有任何实现被调用：%v", inv.calls)
	}
}

func TestRunnerUndiscoveredTool(t *testing.T) {
	r := newRunner(&fakeInvoker{}, runnerManifest("searxng"), &models.Plugin{
		Name: "web_search", Impl: "searxng", IsActive: true,
	})
	if _, err := r.Invoke(context.Background(), "t0", "no_such_tool", nil); Code(err) != CodeConfig {
		t.Fatalf("未知工具应当报 config，实际 %v", err)
	}
	if r.Has("no_such_tool") {
		t.Error("Has 不该命中未知工具")
	}
	if !r.Has("WEB_SEARCH") {
		t.Error("Has 应当大小写不敏感")
	}
}

// 兜底链里写了磁盘上没有的实现：跳过它并记进 notes，而不是整条链失败。
func TestRunnerSkipsGhostImplementation(t *testing.T) {
	inv := &fakeInvoker{fn: func(impl string) (any, error) {
		if impl == "tavily" {
			return nil, &CallError{Code: CodeNetwork, Message: "连不上"}
		}
		return map[string]any{"results": []any{}}, nil
	}}
	r := newRunner(inv, runnerManifest("tavily", "searxng"), &models.Plugin{
		Name: "web_search", Impl: "tavily", IsActive: true,
		Fallbacks: mustJSONB(`["ghost","searxng"]`),
	})

	raw, err := r.Invoke(context.Background(), "t0", "web_search", map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("invoke 失败: %v", err)
	}
	if strings.Join(inv.calls, ",") != "tavily,searxng" {
		t.Fatalf("调用顺序 = %v（ghost 不该被调）", inv.calls)
	}
	res, _ := raw.(map[string]any)
	notes, _ := res["notes"].([]any)
	found := false
	for _, n := range notes {
		if s, _ := n.(string); strings.Contains(s, "不在插件清单") {
			found = true
		}
	}
	if !found {
		t.Errorf("没有说明被跳过的实现：%v", res["notes"])
	}
}

func TestRunnerTruncatesLargeResult(t *testing.T) {
	big := make([]any, 0, 100)
	for i := 0; i < 100; i++ {
		big = append(big, map[string]any{
			"title":   fmt.Sprintf("标题 %03d", i),
			"url":     fmt.Sprintf("https://example.com/%03d", i),
			"snippet": strings.Repeat("摘要内容", 40),
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
		t.Fatalf("invoke 失败: %v", err)
	}
	res, _ := raw.(map[string]any)
	if res["truncated"] != true {
		t.Fatal("超限结果应当标记 truncated")
	}
	items, _ := res["results"].([]any)
	if len(items) >= 100 {
		t.Fatalf("结果没被裁掉：%d 条", len(items))
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxResultBytes {
		t.Fatalf("裁剪后仍有 %d 字节，超过上限 %d", len(b), MaxResultBytes)
	}
}

func mustJSONB(s string) xsql.JSONB {
	return xsql.JSONB{Data: json.RawMessage(s)}
}
