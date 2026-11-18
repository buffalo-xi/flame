package flow

import "sync"

// DraftRef 一次调试运行里「跟随了草稿」的实体。
//
// 落进运行记录（flow_run_records.drafts），用来回答「这次跑出来的是哪几份草稿」——
// 这正是「无脑传播草稿」丢掉的东西：scope 内换了内容，事后必须能说清当时用的是哪一份。
type DraftRef struct {
	Kind string `json:"kind"` // flow / agent / skill / tool
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"` // 例如「未发布」「有未发布修改」
}

// DebugScope 一次调试会话的作用域。
//
// 它只有两个来源：编辑页调试器（websocket 的 flowDebugSource）与 run_flow 试跑。
// 其余入口（MCP / RPC / agent 的 local__ 工具 / 用户聊天）构造执行上下文时一律不设，
// 因此天然拿不到 scope —— 不需要「记得别设置」，而是结构上就没有。
//
// 作用域沿**进程内内部调用**继承（flow → flow，将来还有 flow → agent → flow），
// 跨进程边界的调用（MCP / RPC / HTTP）不存在 scope。
//
// 语义是「草稿沿归属与依赖走，但不跨进程」：
//   - 被调实体对调试者可编辑、且未归档 → 用草稿，并记进 Drafts；
//   - 其余 → 用正式版本（没有正式版本就报错，不静默退回草稿）。
type DebugScope struct {
	// FollowDrafts 是否跟随依赖的草稿。调试面板上的开关，默认开：
	// 关掉就是「只跑我这一层」，用来确认「单独改 A 有没有问题」。
	FollowDrafts bool

	mu     sync.Mutex
	drafts []DraftRef
}

// NewDebugScope 新建一个调试作用域。
func NewDebugScope(followDrafts bool) *DebugScope {
	return &DebugScope{FollowDrafts: followDrafts}
}

// Enabled 是否是「跟草稿」的调试作用域；nil 安全（正式运行返回 false）。
func (s *DebugScope) Enabled() bool {
	return s != nil && s.FollowDrafts
}

// RecordDraft 记一条「本次跟随了谁的草稿」。同一实体只记一次，避免并发节点重复写入。
//
// 并发节点（mono / 并行）会从多个 goroutine 调用，所以加锁。
func (s *DebugScope) RecordDraft(kind, id, name, note string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.drafts {
		if d.Kind == kind && d.ID == id {
			return
		}
	}
	s.drafts = append(s.drafts, DraftRef{Kind: kind, ID: id, Name: name, Note: note})
}

// Drafts 返回本次跟随的草稿清单（副本，供落库）。
func (s *DebugScope) Drafts() []DraftRef {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.drafts) == 0 {
		return nil
	}
	out := make([]DraftRef, len(s.drafts))
	copy(out, s.drafts)
	return out
}
