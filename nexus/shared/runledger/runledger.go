package runledger

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/redis"
	"flame/tools/xlog"
)

// ===== 运行台账（跨实例可见）=====
//
// 为什么需要：agent 的 runRegistry 是进程内 map，flow 的 flow_run_records 只在跑完
// 才落库 —— 两边「运行中」都不可见。多副本时管理端既看不到别的实例上跑着什么，
// /stop 也找不对地方。这里把「本进程在跑的 run」写进 Redis，读侧合并成一份租户视图。
//
// 为什么放在 Redis：它已是本服务的硬依赖（缓存）；台账写失败只会让列表变窄，
// **绝不影响运行本身**（所有台账调用都是 best-effort，错误只记日志）。
//
// 键布局：租户一个 hash —— run:active:{schema}，字段 = run_id，值 = Snapshot JSON。
//   - 不用 SAdd + SCAN：xredis 目前只暴露 hash / string 两类命令；
//   - 字段没有独立 TTL（Redis 7.4 之前不支持），存活靠三条兜底：
//     1) 心跳（heartbeat_at，每 runLedgerInterval 刷一次）；
//     2) 读时裁剪（超过 runLedgerTTL 没心跳 = 实例崩了 / 掉线了，读的时候顺手删掉）；
//     3) 键级 TTL（ledgerTTL，每次写入 / 心跳续期）—— 没人读的时候也能自愈。
const (
	// KindAgent 智能体运行。
	KindAgent = "agent"
	// KindFlow 工作流运行。
	KindFlow = "flow"
)

const (
	// runLedgerTTL 多久没心跳就判定这条 run 已失效。
	// 取心跳周期的 4 倍多：偶发一次 Redis 抖动不至于让列表抖动。
	runLedgerTTL = 90 * time.Second
	// runLedgerInterval 心跳周期。
	runLedgerInterval = 20 * time.Second
	// ledgerTTL 台账键的整体过期时间，每次写入 / 心跳都续期。
	//
	// 为什么需要它：字段级 TTL Redis 7.4 之前不支持，而「读时裁剪」有个缺口 ——
	// 某租户的实例崩了、之后一直没人打开运行页，脏条目就会永久留在 hash 里。
	// 加上键级 TTL 后，只要这个租户彻底没有活着的实例在跑，键会自己消失。
	// 取 runLedgerTTL 的 2 倍：还有活实例在刷心跳时，绝对不会掉。
	ledgerTTL = 2 * runLedgerTTL
)

// Entry 一次运行的登记信息（agent 与 flow 共用）。
type Entry struct {
	Kind       string // KindAgent / KindFlow
	RunID      string
	SessionID  string
	Schema     string
	TargetID   string // agent_id / flow_id
	TargetName string // agent 名 / flow 名
	UserID     string
	RunType    string // call / rpc / mcp / flow / agent / debug / preview
	StartedAt  time.Time
}

// Snapshot 台账里的一条（列表返回的形状）。
type Snapshot struct {
	Kind       string `json:"kind"`
	RunID      string `json:"run_id"`
	SessionID  string `json:"session_id"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
	UserID     string `json:"user_id"`
	RunType    string `json:"run_type"`
	Instance   string `json:"instance"`
	StartedAt  int64  `json:"started_at"`   // unix ms
	Heartbeat  int64  `json:"heartbeat_at"` // unix ms，超过 runLedgerTTL 未更新即失效
	// Local 这条就在当前实例上（读侧补的，不落 Redis）：
	// 只有它能被本进程的 /stop 直接命中。
	Local bool `json:"local"`
	// UserName 用户展示名（同样读侧补，不落 Redis）：台账里只存 user_id，
	// 名字留在 users 表 —— 用户改名后这里应该立刻跟着变，而不是台账里留一份旧快照。
	UserName string `json:"user_name,omitempty"`
}

// Store 台账用到的 Redis 能力（抽出来是为了单测能替换成内存实现）。
type Store interface {
	HSetMultiple(key string, values map[string]interface{}) error
	HGetAll(key string) (map[string]string, error)
	HDel(key string, fields ...string) error
	HLen(key string) (int64, error)
	Del(keys ...string) error
	// Expire 给整个台账键续命（见 ledgerTTL）：没人读的时候也能清掉脏条目。
	Expire(key string, expiration time.Duration) error
}

// 本进程在跑的条目：心跳与「Redis 不可用时的兜底视图」都读它。
var local = struct {
	mu      sync.Mutex
	entries map[string]Entry
	once    sync.Once
}{entries: make(map[string]Entry)}

var (
	instanceOnce sync.Once
	instanceName string
)

// LocalInstanceID 本实例标识：host:port（配置未加载时退化为 host）。
// 只用于台账展示与 Local 判断，不参与任何路由。
func LocalInstanceID() string {
	instanceOnce.Do(func() {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "unknown"
		}
		if config.Cfg != nil && config.Cfg.Server.Port > 0 {
			instanceName = fmt.Sprintf("%s:%d", host, config.Cfg.Server.Port)
			return
		}
		instanceName = host
	})
	return instanceName
}

// Register 登记一次运行（写台账 + 纳入心跳），返回幂等的注销函数。
//
// schema 为空（无租户语义的调用）或 Redis 未启用时退化为「只记内存」：
// 不影响运行本身，只是跨实例看不见。
func Register(e Entry) func() {
	if e.RunID == "" || e.Schema == "" {
		return func() {}
	}
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}

	local.mu.Lock()
	local.entries[e.RunID] = e
	local.mu.Unlock()

	if s := currentStore(); s != nil {
		if err := record(s, e, time.Now()); err != nil {
			logErr("写入运行台账失败 schema=%s kind=%s run=%s: %v", e.Schema, e.Kind, e.RunID, err)
		}
		startHeartbeat()
		// 停止信号的订阅依赖 Redis：启动时它可能还没就绪，所以在每次登记时补一次
		// 「确保已订阅」（已订阅时是空操作）。
		ensureStopSubscription()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			local.mu.Lock()
			delete(local.entries, e.RunID)
			local.mu.Unlock()
			if s := currentStore(); s != nil {
				if err := drop(s, e.Schema, e.RunID); err != nil {
					logErr("清理运行台账失败 schema=%s run=%s: %v", e.Schema, e.RunID, err)
				}
			}
		})
	}
}

// List 列出某租户当前在跑的运行：Redis 台账（跨实例）+ 本进程兜底。
//
// kind 为空 = 全部。err 非 nil 表示台账没读全（此时列表只剩本进程的那些）。
func List(schema, kind string) ([]Snapshot, error) {
	var (
		remote []Snapshot
		rerr   error
	)
	if s := currentStore(); s != nil {
		remote, rerr = list(s, schema, time.Now())
	}

	byID := make(map[string]Snapshot, len(remote))
	for _, snap := range remote {
		byID[snap.RunID] = snap
	}
	// 本地兜底：Redis 不可用、或心跳还没来得及写进去的那一瞬间，至少能看到本进程的。
	for _, snap := range localSnapshots(schema, time.Now()) {
		if prev, ok := byID[snap.RunID]; ok {
			if prev.Instance == "" {
				prev.Instance = snap.Instance
			}
			if prev.StartedAt == 0 {
				prev.StartedAt = snap.StartedAt
			}
			prev.Local = true
			byID[snap.RunID] = prev
			continue
		}
		byID[snap.RunID] = snap
	}

	out := make([]Snapshot, 0, len(byID))
	for _, snap := range byID {
		if kind != "" && snap.Kind != kind {
			continue
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out, rerr
}

// ===== 停止信号（跨实例）=====
//
// 为什么需要：运行只存在于跑它的那台机器的内存里（agent 的 runRegistry、flow 的
// ExecContext），而 /stop 是一条可能被负载均衡分到任意实例的 HTTP 请求。台账解决
// 「在哪台」，这里解决「告诉那台」：停止意图发到 Redis 频道，所有实例都收到，
// 只有持有这条运行的实例能命中，其余忽略即可（取消本身是幂等的）。
const stopChannelPattern = "run:stop:*"

// StopSignal 一条停止意图。RunID 为空 = 停整个会话（只有 agent 支持会话级）。
type StopSignal struct {
	Schema    string `json:"schema"`
	Kind      string `json:"kind"`
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
}

type stopPublisher interface {
	Publish(channel string, payload []byte) error
}

type stopSubscriber interface {
	SubscribePattern(handler func(channel string, payload []byte), patterns ...string) (func() error, error)
}

// BroadcastStop 把停止意图广播给所有实例（经 Redis PUBLISH；含本进程 —— 本地已处理过
// 也无妨，取消是幂等的）。返回 false 表示 Redis 不可用：广播没发出去，只能指望本地命中。
func BroadcastStop(sig StopSignal) (bool, error) {
	if sig.Schema == "" || (sig.RunID == "" && sig.SessionID == "") {
		return false, nil
	}
	pub, ok := currentStore().(stopPublisher)
	if !ok {
		return false, nil
	}
	b, err := json.Marshal(sig)
	if err != nil {
		return false, err
	}
	if err := pub.Publish("run:stop:"+sig.Schema, b); err != nil {
		return false, err
	}
	return true, nil
}

var stopHub = struct {
	mu       sync.Mutex
	handlers []func(StopSignal)
	started  bool
}{}

// SubscribeStop 注册停止信号的本地处理函数；进程内只保留一份 Redis 订阅。
//
// 订阅在第一次拿到 Redis 客户端时建立（Register 会补试），所以启动顺序不重要：
// 注册时 Redis 还没就绪，也不会永久失去订阅。
func SubscribeStop(handler func(StopSignal)) {
	if handler == nil {
		return
	}
	stopHub.mu.Lock()
	stopHub.handlers = append(stopHub.handlers, handler)
	stopHub.mu.Unlock()
	ensureStopSubscription()
}

func ensureStopSubscription() {
	stopHub.mu.Lock()
	started := stopHub.started
	stopHub.mu.Unlock()
	if started {
		return
	}
	sub, ok := currentStore().(stopSubscriber)
	if !ok {
		return
	}

	stopHub.mu.Lock()
	if stopHub.started {
		stopHub.mu.Unlock()
		return
	}
	stopHub.started = true
	stopHub.mu.Unlock()

	if _, err := sub.SubscribePattern(dispatchStop, stopChannelPattern); err != nil {
		// 订阅没建立起来：把标记放回去，等下一次 Register 再试
		stopHub.mu.Lock()
		stopHub.started = false
		stopHub.mu.Unlock()
		logErr("订阅运行停止频道失败: %v", err)
	}
}

func dispatchStop(channel string, payload []byte) {
	var sig StopSignal
	if err := json.Unmarshal(payload, &sig); err != nil {
		return
	}
	if sig.Schema == "" {
		sig.Schema = strings.TrimPrefix(channel, "run:stop:")
	}
	stopHub.mu.Lock()
	handlers := append([]func(StopSignal){}, stopHub.handlers...)
	stopHub.mu.Unlock()
	for _, h := range handlers {
		h(sig)
	}
}

// ---- 内部实现 ----

func currentStore() Store {
	if redis.Client == nil {
		return nil
	}
	return redis.Client // xredis.Client 满足 Store（只用到 hash / string 命令）
}

// ledgerKey 本租户的运行台账（xredis 前缀在客户端统一叠加）。
func ledgerKey(schema string) string { return "run:active:" + schema }

func snapshot(e Entry, now time.Time) Snapshot {
	return Snapshot{
		Kind:       e.Kind,
		RunID:      e.RunID,
		SessionID:  e.SessionID,
		TargetID:   e.TargetID,
		TargetName: e.TargetName,
		UserID:     e.UserID,
		RunType:    e.RunType,
		Instance:   LocalInstanceID(),
		StartedAt:  e.StartedAt.UnixMilli(),
		Heartbeat:  now.UnixMilli(),
	}
}

func record(s Store, e Entry, now time.Time) error {
	b, err := json.Marshal(snapshot(e, now))
	if err != nil {
		return err
	}
	key := ledgerKey(e.Schema)
	if err := s.HSetMultiple(key, map[string]interface{}{e.RunID: string(b)}); err != nil {
		return err
	}
	// 续期失败不影响这份台账可用（字段已经写进去了），下次心跳还会再试。
	_ = s.Expire(key, ledgerTTL)
	return nil
}

func drop(s Store, schema, runID string) error {
	key := ledgerKey(schema)
	if err := s.HDel(key, runID); err != nil {
		return err
	}
	// 最后一条走了就删键，别在键树里留空 hash。
	if n, err := s.HLen(key); err == nil && n == 0 {
		return s.Del(key)
	}
	return nil
}

func list(s Store, schema string, now time.Time) ([]Snapshot, error) {
	key := ledgerKey(schema)
	raw, err := s.HGetAll(key)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(raw))
	stale := make([]string, 0)
	for runID, val := range raw {
		var snap Snapshot
		if err := json.Unmarshal([]byte(val), &snap); err != nil {
			stale = append(stale, runID) // 坏数据：顺手清掉
			continue
		}
		if snap.Heartbeat == 0 || now.Sub(time.UnixMilli(snap.Heartbeat)) > runLedgerTTL {
			stale = append(stale, runID) // 实例崩溃 / 掉线留下的条目
			continue
		}
		if snap.RunID == "" {
			snap.RunID = runID
		}
		out = append(out, snap)
	}
	if len(stale) > 0 {
		if herr := s.HDel(key, stale...); herr == nil {
			if n, nerr := s.HLen(key); nerr == nil && n == 0 {
				_ = s.Del(key)
			}
		}
	}
	return out, nil
}

func localSnapshots(schema string, now time.Time) []Snapshot {
	local.mu.Lock()
	out := make([]Snapshot, 0, len(local.entries))
	for _, e := range local.entries {
		if e.Schema != schema {
			continue
		}
		snap := snapshot(e, now)
		snap.Local = true
		out = append(out, snap)
	}
	local.mu.Unlock()
	return out
}

func startHeartbeat() {
	local.once.Do(func() {
		go func() {
			t := time.NewTicker(runLedgerInterval)
			defer t.Stop()
			for range t.C {
				heartbeat()
			}
		}()
	})
}

// heartbeat 刷新本进程所有在跑条目的心跳（按租户一次往返）。
func heartbeat() {
	s := currentStore()
	if s == nil {
		return
	}
	local.mu.Lock()
	entries := make([]Entry, 0, len(local.entries))
	for _, e := range local.entries {
		entries = append(entries, e)
	}
	local.mu.Unlock()
	if len(entries) == 0 {
		return
	}

	now := time.Now()
	bySchema := make(map[string]map[string]interface{}, 1)
	for _, e := range entries {
		b, err := json.Marshal(snapshot(e, now))
		if err != nil {
			continue // 单条坏数据不该挡住整批心跳
		}
		m := bySchema[e.Schema]
		if m == nil {
			m = make(map[string]interface{})
			bySchema[e.Schema] = m
		}
		m[e.RunID] = string(b)
	}
	for schema, values := range bySchema {
		key := ledgerKey(schema)
		if err := s.HSetMultiple(key, values); err != nil {
			logErr("刷新运行台账心跳失败 schema=%s: %v", schema, err)
			return
		}
		// 心跳同时也是键的续期：活着的实例一直在，键就不会过期。
		_ = s.Expire(key, ledgerTTL)
	}
}

// errLogAt 台账错误限流：Redis 故障时心跳每 20s 一次，别把日志刷满。
var errLogAt atomic.Int64

func logErr(format string, args ...interface{}) {
	now := time.Now().Unix()
	last := errLogAt.Load()
	if now-last < 60 || !errLogAt.CompareAndSwap(last, now) {
		return
	}
	xlog.Error(format, args...)
}
