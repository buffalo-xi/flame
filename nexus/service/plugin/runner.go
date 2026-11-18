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

// Invoker 执行一次宿主调用。*Host 是生产实现；单测注入假实现就不必起 python。
type Invoker interface {
	Invoke(ctx context.Context, tool, impl string, payload *InvokePayload) (any, error)
}

// MaxResultBytes 回给模型的结果字节上限。
//
// 宁可截断也不让一次检索撑爆上下文：超限时从尾部丢 results，置 truncated=true。
const MaxResultBytes = 12 * 1024

// Runner 把「工具名 + 入参」变成一次带租户配置的插件调用。
//
// 职责边界：
//   - 选实现与兜底链（主实现 → fallbacks；config 类错误不降级）
//   - 结果裁剪与元信息补全
//   - 不负责发现（Registry 干）、不负责授权（ExecuteTool 干）
//
// 结构上满足 tools.PluginRunner（Has / Invoke），但刻意不 import tools 包 ——
// 接口是结构化的，少一条依赖就少一个环。
type Runner struct {
	host Invoker
	repo pluginsrepo.Repository
	src  Source
	pol  PolicySource // 可为 nil（视为缺省 = 开放；停用要显式写行）
}

// PolicySource 读取「租户 × 插件」的平台策略（能否使用）。
//
// 做成接口是为了单测：生产实现直连 platform.tenant_plugins，
// 用例注入假实现就不必为「平台给没给授权」去连库。
type PolicySource interface {
	Resolve(tenantID, pluginName string) (pluginpolicy.Policy, error)
}

// policySourceFunc 让普通函数满足 PolicySource（默认实现就是 pluginpolicy.Resolve）。
type policySourceFunc func(tenantID, pluginName string) (pluginpolicy.Policy, error)

func (f policySourceFunc) Resolve(tenantID, pluginName string) (pluginpolicy.Policy, error) {
	return f(tenantID, pluginName)
}

// NewRunner 构造执行器。host 可为 nil（此时所有调用报 config 错，便于「插件没配好」时降级）。
func NewRunner(host Invoker, repo pluginsrepo.Repository, src Source) *Runner {
	return &Runner{host: host, repo: repo, src: src, pol: policySourceFunc(pluginpolicy.Resolve)}
}

// SetPolicySource 替换平台策略来源（单测用；返回自身便于链式调用）。
func (r *Runner) SetPolicySource(p PolicySource) *Runner {
	r.pol = p
	return r
}

// NewDefaultRunner 用默认仓储构造执行器（main 用它，省得再 import 仓储包）。
func NewDefaultRunner(host Invoker, src Source) *Runner {
	return NewRunner(host, pluginsrepo.NewRepository(), src)
}

// Has 该名字是不是已发现的插件工具（只看 manifest）。
func (r *Runner) Has(name string) bool {
	if r == nil || r.src == nil {
		return false
	}
	_, ok := r.src.Get(name)
	return ok
}

// Invoke 执行一次调用：读租户配置 → 按主实现与兜底链逐个试 → 裁剪结果。
func (r *Runner) Invoke(ctx context.Context, schema, toolName string, args map[string]any) (any, error) {
	if r == nil || r.host == nil {
		return nil, &CallError{Code: CodeConfig, Message: "插件执行器未初始化"}
	}
	if r.src == nil {
		return nil, &CallError{Code: CodeConfig, Message: "插件清单为空（未发现任何插件）"}
	}
	if strings.TrimSpace(schema) == "" {
		return nil, &CallError{Code: CodeConfig, Message: "缺少租户上下文，无法定位插件配置"}
	}

	manifest, ok := r.src.Get(toolName)
	if !ok {
		return nil, &CallError{Code: CodeConfig, Message: "插件工具不存在：" + toolName}
	}

	// 平台策略先于租户配置判定：被平台停用时的提示应当是「找平台管理员」，
	// 而不是「去插件页启用」—— 后者启用了也调不动。
	pol := pluginpolicy.Default()
	if r.pol != nil {
		got, perr := r.pol.Resolve(schema, manifest.Name)
		if perr != nil {
			// 读不出来就**不放行**：这里静默回退缺省等于授权机制失效。
			// 表不存在多半是上线顺序错了（授权表 / 闸表没先执行），给一句可操作的线索，
			// 否则只能看到「插件调不动」。
			hint := ""
			if strings.Contains(perr.Error(), "does not exist") {
				hint = " —— 请先执行 nexus/platform/sql/tenant_plugins.sql 与 plugin_gates.sql"
			}
			return nil, &CallError{
				Code:    CodeOther,
				Message: fmt.Sprintf("读取插件 %s 的租户授权失败: %v%s", manifest.Name, perr, hint),
			}
		}
		pol = got
	}
	if !pol.Allowed {
		// 被闸拦下时说清是「全局停用」还是「只停了这一家」：
		// 两句提示的影响面不同，能做的事也不同（等解闸 vs 找平台恢复本租户）。
		if pol.Blocked {
			return nil, &CallError{
				Code:    CodeConfig,
				Message: fmt.Sprintf("插件 %s 已被平台全局停用", manifest.Name),
			}
		}
		return nil, &CallError{
			Code:    CodeConfig,
			Message: fmt.Sprintf("插件 %s 已被平台对本租户停用（请联系平台管理员恢复）", manifest.Name),
		}
	}

	p, err := r.repo.Get(&auth.AuthInfo{Schema: schema}, manifest.Kind, manifest.Name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, &CallError{
			Code:    CodeConfig,
			Message: fmt.Sprintf("插件 %s 未启用（请在管理端「插件」页启用并配置实现）", manifest.Name),
		}
	case err != nil:
		return nil, err
	}
	if !p.IsActive {
		return nil, &CallError{Code: CodeConfig, Message: fmt.Sprintf("插件 %s 已停用", manifest.Name)}
	}

	chain, notes := buildChain(manifest, p)
	if len(chain) == 0 {
		return nil, &CallError{
			Code:    CodeConfig,
			Message: fmt.Sprintf("插件 %s 没有可用的实现（主实现 %q 不在磁盘上）", manifest.Name, p.Impl),
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
			xlog.Info("插件调用 tool=%s impl=%s schema=%s 耗时=%s", manifest.Name, impl, schema, time.Since(started))
			return truncateResult(res, impl, args, notes), nil
		}

		// config 类错误（缺 key / 实现不存在）换一家也没用，直接上报；
		// 其余（auth / quota / network / other）降级到兜底链上的下一家。
		if Code(err) == CodeConfig {
			return nil, err
		}
		xlog.Error("插件调用失败 tool=%s impl=%s schema=%s: %v", manifest.Name, impl, schema, err)
		failures = append(failures, impl+": "+err.Error())
		if ctx.Err() != nil {
			break
		}
	}

	return nil, &CallError{
		Code:    CodeOther,
		Message: fmt.Sprintf("插件 %s 的所有实现均失败 —— %s", manifest.Name, strings.Join(failures, "；")),
	}
}

// buildChain 组装「主实现 + 兜底链」，顺手记下被跳过的项（写进结果的 notes，模型与人都能看见）。
func buildChain(manifest Manifest, p *models.Plugin) (impls []string, notes []string) {
	seen := make(map[string]bool, 2)

	add := func(name, role string) {
		name = strings.TrimSpace(name)
		if name == "" {
			notes = append(notes, role+"为空，已跳过")
			return
		}
		if seen[name] {
			notes = append(notes, "重复的"+role+"已忽略："+name)
			return
		}
		if _, ok := manifest.ImplementationOf(name); !ok {
			notes = append(notes, fmt.Sprintf("%s %q 不在插件清单里，已跳过", role, name))
			return
		}
		seen[name] = true
		impls = append(impls, name)
	}

	add(p.Impl, "主实现")
	for _, fb := range p.FallbackImpls() {
		add(fb, "兜底实现")
	}
	return impls, notes
}

// truncateResult 补全元信息并做体积裁剪。
//
// 插件结果约定是 {results:[], answer?, notes?}；这里只保证三件事：
// provider 有值、query 回填、总字节不超过 MaxResultBytes。
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
		// 极端情况：非 results 字段（answer / notes）本身就超限，直接砍成摘要
		return map[string]any{
			"provider":  res["provider"],
			"query":     res["query"],
			"results":   []any{},
			"truncated": true,
			"notes":     []any{fmt.Sprintf("结果超过 %d 字节，已丢弃正文", MaxResultBytes)},
		}
	}
	return res
}

// argString 从 args 取字符串（缺省空串）；只在回填 query 用。
func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return s
}
