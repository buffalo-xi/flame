package gw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Backend 一个 sandboxd 实例 + 它的负载快照。
//
// 负载是「估计值」：快照之间可能有瞬时漂移，但足以支撑负载均衡。
// refresh 失败时保留旧值（lastSeen 不变），宁可给出过时估计、不主动摘除
// （摘除与健康检查是后续阶段的事）。
type Backend struct {
	URL   string // HTTP 基地址（配置值）
	Token string

	mu       sync.RWMutex
	exec     int64     // exec_inflight
	code   int64     // Σ code_busy
	active   int64     // interactive_active（正服务调用的进程，用于计重）
	sessions int64     // interactive_sessions（观测用）
	procs    int64     // interactive_processes（观测用，含空闲）
	lastSeen time.Time // 最近一次成功拉取
	err      error     // 最近一次拉取错误（观测用）

	// healthy 健康状态（atomic，健康检查 loop 更新）。默认 true，连续失败达到
	// 阈值后置 false（摘除），恢复后置回 true。
	healthy   atomic.Bool
	failCount atomic.Int32 // 连续失败次数

	// 长交互超时（/v1/capabilities 的 interactive 段）。只用于校验本网关的
	// sticky_ttl 是否覆盖得住对方的空闲回收上界，属于「启动后拉一次」的静态信息。
	capsMu sync.Mutex
	caps   interactiveCaps
}

// capsRefresh capabilities 的重新拉取间隔。
//
// 为什么不能只拉一次：sandboxd 改配置重启是常态，而 idle/janitor 就写在它的配置里。
// 缓存永不过期会导致「对端已改、这边仍按旧值判断」—— 更糟的是判断结果为"合格"，
// 于是连一条日志都没有，人以为校验生效了。
//
// 变量而非常量：测试要能把间隔压到毫秒级。
var capsRefresh = time.Minute

// interactiveCaps sandboxd 声明的长交互生命周期参数。
type interactiveCaps struct {
	IdleSec    int // interactive_idle_sec：持久 session 进程的空闲回收
	EphSec     int // interactive_ephemeral_idle_sec：临时进程的短兜底
	JanitorSec int // interactive_janitor_sec：巡检间隔（回收延迟的上界）

	known     bool      // 是否已成功拉到（失败时下次刷新再试）
	at        time.Time // 拉取时刻（决定何时该重新拉）
	ttlWarned bool      // 是否已就当前这组值报过 TTL 不足
}

// capsPayload 只取校验 TTL 需要的字段，其余（mode/network/langs/environment）网关不关心。
type capsPayload struct {
	Interactive struct {
		IdleSec          int `json:"idle_sec"`
		EphemeralIdleSec int `json:"ephemeral_idle_sec"`
		JanitorSec       int `json:"janitor_sec"`
	} `json:"interactive"`
}

// fetchInteractiveCaps 拉一次 /v1/capabilities，取 sandboxd 的 interactive 段。
//
// 老版本 sandboxd 没有这一段：解出来全是 0，调用方按「未知」处理（不校验、不告警）。
func (b *Backend) fetchInteractiveCaps(ctx context.Context) (interactiveCaps, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.capabilitiesURL(), nil)
	if err != nil {
		return interactiveCaps{}, err
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return interactiveCaps{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return interactiveCaps{}, fmt.Errorf("capabilities: http %d", resp.StatusCode)
	}
	var p capsPayload
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return interactiveCaps{}, err
	}
	return interactiveCaps{
		IdleSec:    p.Interactive.IdleSec,
		EphSec:     p.Interactive.EphemeralIdleSec,
		JanitorSec: p.Interactive.JanitorSec,
		known:      true,
	}, nil
}

// setCaps 记下对方的能力。
//
// 值没变就沿用旧的告警标记（避免同一条反复刷）；值变了就清掉标记 —— 说明对端改过
// 配置重启了，需要重新判一次，哪怕上次已经报过。
func (b *Backend) setCaps(c interactiveCaps) {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	c.at = time.Now()
	if c.IdleSec == b.caps.IdleSec && c.JanitorSec == b.caps.JanitorSec {
		c.ttlWarned = b.caps.ttlWarned
	}
	b.caps = c
}

// capsSnapshot 当前已知的长交互参数；known=false 表示还没拉到或对方没报。
func (b *Backend) capsSnapshot() interactiveCaps {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	return b.caps
}

// invalidateCaps 把缓存标记为过期，下次巡检重新拉取。
// 后端从「不健康」恢复时调用：它多半刚重启过，配置可能变了。
func (b *Backend) invalidateCaps() {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	b.caps.known = false
}

// refreshCapsIfStale 缓存过期时重新拉一次；失败保留旧值（下次再试），不刷日志。
func (b *Backend) refreshCapsIfStale(ctx context.Context) {
	b.capsMu.Lock()
	fresh := b.caps.known && time.Since(b.caps.at) < capsRefresh
	b.capsMu.Unlock()
	if fresh {
		return
	}
	got, err := b.fetchInteractiveCaps(ctx)
	if err != nil {
		return
	}
	b.setCaps(got)
}

// markTTLWarned 标记「已就 TTL 不足告警过」，返回是否是第一次。
func (b *Backend) markTTLWarned() bool {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	if b.caps.ttlWarned {
		return false
	}
	b.caps.ttlWarned = true
	return true
}

// metricsPayload 与 sandboxd /v1/metrics 的 JSON 对齐。
type metricsPayload struct {
	ExecInFlight        int64            `json:"exec_inflight"`
	CodeBusy          map[string]int64 `json:"code_busy"`
	InteractiveSessions int64            `json:"interactive_sessions"`
	InteractiveProcess  int64            `json:"interactive_processes"`
	InteractiveActive   int64            `json:"interactive_active"`
}

// score 负载分数：三类占用同源相加。interactive 只计活跃进程（空闲不烧 CPU）。
// 分数越低越闲。
func (b *Backend) score() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.exec + b.code + b.active
}

// refresh 拉一次 /v1/metrics，更新快照。
func (b *Backend) refresh(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.metricsURL(), nil)
	if err != nil {
		b.setErr(err)
		return
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.setErr(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.setErr(fmt.Errorf("http %d", resp.StatusCode))
		return
	}
	var m metricsPayload
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		b.setErr(err)
		return
	}
	var code int64
	for _, v := range m.CodeBusy {
		code += v
	}
	b.mu.Lock()
	b.exec = m.ExecInFlight
	b.code = code
	b.active = m.InteractiveActive
	b.sessions = m.InteractiveSessions
	b.procs = m.InteractiveProcess
	b.lastSeen = time.Now()
	b.err = nil
	b.mu.Unlock()
}

func (b *Backend) setErr(err error) {
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

// backendSnapshot 单个后端的只读负载快照（gateway /metrics 输出用）。
type backendSnapshot struct {
	URL       string    `json:"url"`
	Healthy   bool      `json:"healthy"`
	Score     int64     `json:"score"`
	Exec      int64     `json:"exec_inflight"`
	Code    int64     `json:"code_busy"`
	Active    int64     `json:"interactive_active"`
	Sessions  int64     `json:"interactive_sessions"`
	Processes int64     `json:"interactive_processes"`
	LastSeen  time.Time `json:"last_seen"`
	Error     string    `json:"error,omitempty"`
}

// snapshot 返回后端当前负载快照（只读拷贝）。
func (b *Backend) snapshot() backendSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	s := backendSnapshot{
		URL:       b.URL,
		Healthy:   b.healthy.Load(),
		Score:     b.exec + b.code + b.active,
		Exec:      b.exec,
		Code:    b.code,
		Active:    b.active,
		Sessions:  b.sessions,
		Processes: b.procs,
		LastSeen:  b.lastSeen,
	}
	if b.err != nil {
		s.Error = b.err.Error()
	}
	return s
}

// wsURL 把 HTTP 基地址换成 ws，并拼上路径（如 /v1/exec）。
func (b *Backend) wsURL(path string) string {
	return strings.NewReplacer("http://", "ws://", "https://", "wss://").Replace(b.URL) + path
}

func (b *Backend) metricsURL() string {
	return b.URL + "/v1/metrics"
}

func (b *Backend) capabilitiesURL() string {
	return b.URL + "/v1/capabilities"
}

func (b *Backend) healthURL() string {
	return b.URL + "/healthz"
}

// ping 探一次 /healthz，返回是否健康。
func (b *Backend) ping(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.healthURL(), nil)
	if err != nil {
		return false
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
