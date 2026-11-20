package gw

import (
	"context"
	"sync"
	"time"
)

// StickyStore is the sticky binding store: session_id → backend key.
//
// A multi-instance gateway needs shared storage (redis); a single instance uses memory. The interface
// abstracts "lookup / grab / renew".
// Correctness does not depend on stickiness: the only purpose of a binding is "send the same session to
// the same instance as far as possible", and sandboxd's lazy creation is the backstop.
type StickyStore interface {
	// Lookup returns the backend key a session is bound to; with no binding, or an expired one, it returns
	// an empty string with ok=false. A hit renews the ttl (lazy renewal, equivalent to redis EXPIRE).
	Lookup(ctx context.Context, sessionID string, ttl time.Duration) (backendKey string, ok bool, err error)

	// Bind grabs the binding atomically: session → backendKey. It reports whether the grab succeeded —
	// false means an unexpired binding already exists (under concurrency only one gateway wins the first
	// grab). The caller should Lookup again after losing, to pick up the already bound backend and avoid
	// sending the same session to several instances.
	Bind(ctx context.Context, sessionID, backendKey string, ttl time.Duration) (grabbed bool, err error)

	// RemoveBackend deletes every binding that points at this backend (called on health eviction).
	RemoveBackend(ctx context.Context, backendKey string) error

	// Unbind drops one binding, but **only while it still points at backendKey**.
	//
	// This is the only proactive invalidation path for "this session no longer has a long-interaction tool"
	// (sandboxd's FrameSessionEmpty); everything else relies on the TTL. The conditional delete is not
	// pedantry: by the time the unbind signal arrives, nexus may already have re-opened with the same
	// session_id, and that new binding must not be swept away with it.
	Unbind(ctx context.Context, sessionID, backendKey string) error
}

// MemorySticky is the single-process in-memory implementation (single instance / tests).
type MemorySticky struct {
	mu sync.Mutex
	m  map[string]memBinding
}

type memBinding struct {
	backend string
	expires time.Time
}

// NewMemorySticky creates an in-memory sticky store.
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
	b.expires = time.Now().Add(ttl) // lazy renewal
	s.m[sessionID] = b
	return b.backend, true, nil
}

func (s *MemorySticky) Bind(ctx context.Context, sessionID, backendKey string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.m[sessionID]; ok && time.Now().Before(b.expires) {
		b.expires = time.Now().Add(ttl)
		s.m[sessionID] = b
		return false, nil // already present and unexpired: not grabbed, keep the original backend
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
