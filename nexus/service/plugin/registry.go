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

// KindTools 工具插件。当前唯一支持的 kind，也是宿主扫描与清单校验的基准值。
// 与 repository/plugins.KindTools、pluginpolicy.KindTools 同值（三层各自声明，不互相 import）。
const KindTools = "tools"

// SupportedKinds 当前支持的插件种类。
//
// 未知 kind 只记录、不加载：将来加一类插件（sources / channels ...）时，
// 先在宿主里加对应加载器，再把 kind 放进这里 —— 已有插件与授权数据不受影响。
var SupportedKinds = map[string]bool{KindTools: true}

// Snapshot 某一时刻的插件清单。
type Snapshot struct {
	Tools  []Manifest
	Errors []ScanError
	At     time.Time
	// Err 最近一次刷新整体失败的原因。非 nil 时 Tools 仍是上一次成功的结果
	//（「保留旧结果」而不是清空：磁盘暂时读不到，不代表插件不该可用）。
	Err error
}

// Registry 插件清单的内存视图：启动拉一次，之后定期刷新（目录 mtime 变化会提前触发）。
//
// 为什么清单不落库：磁盘是唯一真源。落库会多出一份需要同步的副本，
// 插件升级改了 schema 还要等一轮同步；这里直接每轮重扫，最坏晚一个刷新周期。
type Registry struct {
	host    *Host
	builtin map[string]bool // 内置工具名（小写）：插件工具名与它重名即判废

	mu   sync.RWMutex
	snap Snapshot
	sig  string // 目录签名；未变化时跳过扫描（省掉一次 python 启动）
}

// NewRegistry 构造注册表。builtinNames 传内置工具名（tools.Names()）。
func NewRegistry(host *Host, builtinNames []string) *Registry {
	names := make(map[string]bool, len(builtinNames))
	for _, n := range builtinNames {
		names[strings.ToLower(n)] = true
	}
	return &Registry{host: host, builtin: names}
}

// EnsureDir 确保插件根目录存在（不存在就建空目录）。
//
// 刻意不因为「目录还没建」而拒绝启动：插件是可选能力，
// 部署时忘建目录不该让整个 nexus 起不来。
func EnsureDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("插件目录为空")
	}
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("插件目录不是目录：%s", dir)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建插件目录失败 %s: %w", dir, err)
	}
	xlog.Info("插件目录不存在，已创建空目录：%s", dir)
	return nil
}

// Refresh 重新扫描目录。
//
// force=false 且目录签名未变时直接返回（不启动 python 子进程）。
// 扫描失败不覆盖上一次的成功结果，只记录 Err。
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
		xlog.Error("插件清单扫描失败 dir=%s: %v", r.host.Dir, err)
		return err
	}

	tools, errs := r.validate(res)
	for _, e := range errs {
		xlog.Error("插件清单问题 %s: %s", e.Path, e.Error)
	}

	r.mu.Lock()
	r.snap = Snapshot{Tools: tools, Errors: errs, At: time.Now()}
	r.sig = sig
	r.mu.Unlock()

	xlog.Info("插件清单已刷新 dir=%s tools=%d errors=%d", r.host.Dir, len(tools), len(errs))
	return nil
}

// validate 过滤不可用/冲突的插件，并把原因记进 errors。
//
// 三条硬规则（都只在加载期判定，不在调用期反复猜）：
//  1. kind 必须在 SupportedKinds 里；
//  2. 工具名不能与内置工具重名（否则 ExecuteTool 的分派会含糊）；
//  3. 工具名全局唯一（跨 kind 也是）—— 授权 ref 与工具名都只用这一段名字。
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
				Error: fmt.Sprintf("不支持的插件类型 %q（当前只支持 tools），已跳过", m.Kind),
			})
			continue
		case name == "":
			errs = append(errs, ScanError{Path: path, Error: "工具名为空，已跳过"})
			continue
		case r.builtin[strings.ToLower(name)]:
			errs = append(errs, ScanError{
				Path:  path,
				Error: fmt.Sprintf("工具名 %q 与内置工具重名，已跳过（请给插件改名）", name),
			})
			continue
		}
		key := strings.ToLower(name)
		if prev, dup := seen[key]; dup {
			errs = append(errs, ScanError{
				Path:  path,
				Error: fmt.Sprintf("工具名 %q 与 %s 重复，已跳过（工具名必须全局唯一）", name, prev),
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

// Snapshot 返回当前快照（切片是只读视图，调用方不要改）。
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snap
}

// Get 按工具名取 manifest（大小写不敏感）。
func (r *Registry) Get(name string) (Manifest, bool) {
	snap := r.Snapshot()
	for _, m := range snap.Tools {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return Manifest{}, false
}

// Has 判断某名字是不是已发现的插件工具。
func (r *Registry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// Start 起后台刷新协程。interval <= 0 时用 1 分钟。
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

// ===== 包级单例（与 tools.SetSandbox / sandboxinfo 同一套注册风格） =====

// Source 插件清单的只读视图。Registry 是默认实现；
// 单测可以注入假实现，不必为了「目录里有没有插件」去起 python 子进程。
type Source interface {
	Snapshot() Snapshot
	Get(name string) (Manifest, bool)
}

// sourceBox 包一层：atomic.Value 要求存入的具体类型始终一致。
type sourceBox struct{ s Source }

var defaultSource atomic.Value

// currentRegistry 具体注册表（管理端刷新用）。
var currentRegistry atomic.Pointer[Registry]

// SetSource 注入全局插件清单来源（生产传 *Registry，测试可传假实现；传 nil 表示清空）。
func SetSource(s Source) { defaultSource.Store(sourceBox{s: s}) }

// DefaultSource 取全局清单来源；未注入时为 nil（工具目录里就看不到插件）。
func DefaultSource() Source {
	if v, ok := defaultSource.Load().(sourceBox); ok {
		return v.s
	}
	return nil
}

// ===== 具体注册表的单例（管理端「刷新」需要它，接口视图不够） =====

// SetDefault 注入全局注册表（main 启动时调用；同时承担 SetSource 的职责）。
//
// 顺带把清单暴露给 shared/plugininfo：平台管理台要列「可以给租户开通哪些插件」，
// 但 nexus/platform 的边界规则禁止 import 本包（见 platform/README.md），
// 只能通过 shared 里的最小视图转一手。
func SetDefault(r *Registry) {
	currentRegistry.Store(r)
	SetSource(r)
	plugininfo.SetSource(infoAdapter{r: r})
}

// infoAdapter 把 Registry 的清单映射成 shared/plugininfo 的最小结构。
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

// Current 取全局注册表（*Registry）；测试注入假 Source 时为 nil。
func Current() *Registry { return currentRegistry.Load() }

// dirSignature 汇总目录树的 mtime，用来判断「有没有东西变了」。
//
// 只看 kind/tool/impl 三层：更深的内容（__pycache__、插件自带的数据文件）
// 不影响 manifest，没必要为此重启扫描。
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
			// __pycache__ 之类整棵跳过（它只会带来噪声，不影响 manifest）
			if strings.HasPrefix(d.Name(), "__") {
				return filepath.SkipDir
			}
			depth := 0
			if rel != "." {
				depth = strings.Count(rel, string(filepath.Separator)) + 1
			}
			if depth >= 4 { // 第 4 层（impl/子目录）以下不再关心
				return filepath.SkipDir
			}
			// 目录只记名字、不记 mtime：Python import 会在旁边生成 __pycache__，
			// 那会改掉上层目录的 mtime，但 manifest 没变 —— 记 mtime 会导致每轮都重扫。
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
