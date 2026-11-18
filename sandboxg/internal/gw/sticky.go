package gw

import (
	"context"
	"sync"
	"time"
)

// StickyStore 粘性绑定存储：session_id → 后端 key。
//
// 多实例 gateway 需要共享存储（redis），单机用内存。接口抽象出「查询/抢占/续期」。
// 正确性不依赖粘性：绑定的唯一用途是「尽量把同一 session 打到同一实例」，
// sandboxd 侧懒建兜底。
type StickyStore interface {
	// Lookup 返回 session 绑定的后端 key，无绑定或已过期返回空串 + ok=false。
	// 命中时续期 ttl（惰性续期，等价 redis 的 EXPIRE）。
	Lookup(ctx context.Context, sessionID string, ttl time.Duration) (backendKey string, ok bool, err error)

	// Bind 原子抢占绑定：session → backendKey。返回是否抢到 —— false 表示已存在
	// 未过期绑定（并发下只有一个 gateway 能首次抢占成功）。调用方在抢不到时应
	// 重新 Lookup 拿到已绑定的后端，避免同一 session 打到多个实例。
	Bind(ctx context.Context, sessionID, backendKey string, ttl time.Duration) (grabbed bool, err error)

	// RemoveBackend 删掉指向该后端的所有绑定（健康摘除时调用）。
	RemoveBackend(ctx context.Context, backendKey string) error

	// Unbind 解掉一条绑定，但**仅当它仍指向 backendKey**。
	//
	// 「这个 session 已经没有长交互工具了」的唯一主动失效口（sandboxd 的
	// FrameSessionEmpty），其余情况只能靠 TTL。条件删除不是洁癖：解绑信号
	// 到达时 nexus 可能已经用同一 session_id 重新 open，那条新绑定不该被顺手删掉。
	Unbind(ctx context.Context, sessionID, backendKey string) error
}

// MemorySticky 单机内存实现（单实例 / 测试用）。
type MemorySticky struct {
	mu sync.Mutex
	m  map[string]memBinding
}

type memBinding struct {
	backend string
	expires time.Time
}

// NewMemorySticky 构造内存粘性存储。
func NewMemorySticky() *MemorySticky {
	return &MemorySticky{m: make(map[string]memBinding)}
}

func (s *MemorySticky) Lookup(ctx context.Context, sessionID string, ttl time.Duration) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[sessionID]
	if !ok {
		return "", false, nil
	}
	if time.Now().After(b.expires) {
		delete(s.m, sessionID)
		return "", false, nil
	}
	b.expires = time.Now().Add(ttl) // 惰性续期
	s.m[sessionID] = b
	return b.backend, true, nil
}

func (s *MemorySticky) Bind(ctx context.Context, sessionID, backendKey string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.m[sessionID]; ok && time.Now().Before(b.expires) {
		b.expires = time.Now().Add(ttl)
		s.m[sessionID] = b
		return false, nil // 已存在未过期：没抢到，保持原后端
	}
	s.m[sessionID] = memBinding{backend: backendKey, expires: time.Now().Add(ttl)}
	return true, nil
}

func (s *MemorySticky) Unbind(ctx context.Context, sessionID, backendKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.m[sessionID]; ok && b.backend == backendKey {
		delete(s.m, sessionID)
	}
	return nil
}

func (s *MemorySticky) RemoveBackend(ctx context.Context, backendKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sid, b := range s.m {
		if b.backend == backendKey {
			delete(s.m, sid)
		}
	}
	return nil
}
