package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// pooledSession is one stdio session reused by the pool (one child process per conn.ID).
type pooledSession struct {
	session     *ClientSession
	connID      string
	fingerprint string // hash of command+args+work_dir+env, used to invalidate after a config change
	closed      bool   // the child process already exited
}

// SessionPool is the stdio session process pool: one conn.ID reuses one ClientSession / child process.
// streamable_http is a stateless short-lived connection and is never pooled.
type SessionPool struct {
	mu       sync.Mutex
	sessions map[string]*pooledSession
}

// sessionPool is the global singleton.
var sessionPool = &SessionPool{
	sessions: make(map[string]*pooledSession),
}

// GetSessionPool returns the global stdio session pool singleton.
func GetSessionPool() *SessionPool {
	return sessionPool
}

// Get fetches (or lazily creates) the stdio session of a connection.
// When the pool holds a session with the same fingerprint and the process is alive it is reused;
// otherwise a new one is created and replaces the old session.
func (p *SessionPool) Get(ctx context.Context, conn *models.RpcConnection) (*ClientSession, error) {
	if strings.ToLower(conn.Transport) != "stdio" {
		return nil, fmt.Errorf("SessionPool only supports the stdio transport, got: %s", conn.Transport)
	}

	fp := fingerprint(conn)

	p.mu.Lock()
	existing, ok := p.sessions[conn.ID]
	// Present, same fingerprint and process alive -> reuse.
	if ok && existing.fingerprint == fp && !existing.closed {
		session := existing.session
		p.mu.Unlock()
		return session, nil
	}
	p.mu.Unlock()

	// A new session is needed: invalidate the old one first.
	if ok {
		p.Invalidate(conn.ID)
	}

	session, err := NewClientSession(ctx, conn)
	if err != nil {
		return nil, err
	}

	ps := &pooledSession{
		session:     session,
		connID:      conn.ID,
		fingerprint: fp,
	}
	// Watch the child process exit and mark the session closed.
	go p.watchProcess(ps)

	p.mu.Lock()
	p.sessions[conn.ID] = ps
	p.mu.Unlock()
	return session, nil
}

// watchProcess waits for the child process to exit and sets the closed flag.
// Note: do not delete from the map here, a concurrent Get could misjudge; invalidation is explicit
// through Invalidate.
func (p *SessionPool) watchProcess(ps *pooledSession) {
	if ps.session == nil || ps.session.cmd == nil {
		return
	}
	_ = ps.session.cmd.Wait()
	p.mu.Lock()
	if cur, ok := p.sessions[ps.connID]; ok && cur == ps {
		cur.closed = true
	}
	p.mu.Unlock()
}

// Invalidate closes and removes the pooled session of a connection (called on a config change or disable).
func (p *SessionPool) Invalidate(connID string) {
	p.mu.Lock()
	ps, ok := p.sessions[connID]
	if !ok {
		p.mu.Unlock()
		return
	}
	delete(p.sessions, connID)
	p.mu.Unlock()

	if ps.session != nil {
		_ = ps.session.Close()
	}
}

// Close closes every pooled session and empties the pool (called on system shutdown).
func (p *SessionPool) Close() {
	p.mu.Lock()
	snapshot := make([]*pooledSession, 0, len(p.sessions))
	for _, ps := range p.sessions {
		snapshot = append(snapshot, ps)
	}
	p.sessions = make(map[string]*pooledSession)
	p.mu.Unlock()

	for _, ps := range snapshot {
		if ps.session != nil {
			_ = ps.session.Close()
		}
	}
}

// fingerprint computes a config fingerprint of the connection, used to decide whether the stdio child
// process must be rebuilt.
func fingerprint(conn *models.RpcConnection) string {
	raw, _ := json.Marshal(struct {
		Command string     `json:"command"`
		Args    xsql.JSONB `json:"args"`
		WorkDir string     `json:"work_dir"`
		Env     xsql.JSONB `json:"env"`
	}{conn.Command, conn.Args, conn.WorkDir, conn.Env})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum)
}
