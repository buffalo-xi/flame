package gw

import (
	"context"
	"sync"
	"time"
)

// StickyStore is the sticky binding store: session key → instance key.
//
// A multi-instance gateway needs shared storage (redis); a single instance uses memory. The interface abstracts
// "lookup / grab / renew".
//
// The binding is stricter than sandboxg's: a browser session carries cookies, tabs and a half-filled form, and there is no
// way to move that state to another instance. A binding that expires while the lease is still alive therefore builds a
// second Chromium with a second, empty profile — which is why sticky_ttl_sec must outlive the pool's idle reclaim.
type StickyStore interface {
	// Lookup returns the instance key a session is bound to; with no binding, or an expired one, it returns an empty
	// string with ok=false. A hit renews the ttl (lazy renewal, equivalent to redis EXPIRE).
	Lookup(ctx context.Context, sessionID string, ttl time.Duration) (backendKey string, ok bool, err error)

	// Bind grabs the binding atomically: session → backendKey. It reports whether the grab succeeded — false means an
	// unexpired binding already exists (under concurrency only one gateway wins the first grab). The caller should Lookup
	// again after losing, to pick up the already bound instance.
	Bind(ctx context.Context, sessionID, backendKey string, ttl time.Duration) (grabbed bool, err error)

	// RemoveBackend deletes every binding that points at this instance (called on health eviction).
	RemoveBackend(ctx context.Context, backendKey string) error

	// Unbind drops one binding, but **only while it still points at backendKey**.
	//
	// This is the proactive invalidation path for "this session no longer has a browser there" (FrameSessionEmpty);
	// everything else relies on the TTL. The conditional delete matters: by the time the signal arrives, nexus may already
	// have re-opened with the same session key, and that new binding must not be swept away with it.
	Unbind(ctx context.Context, sessionID, backendKey string) error
}

// MemorySticky is the single-process in-memory implementation (single gateway / tests).
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
		return false, nil // already present and unexpired: not grabbed, keep the original instance
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
