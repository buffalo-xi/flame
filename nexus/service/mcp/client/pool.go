package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"flame/nexus/shared/models"
	"flame/tools/xsql"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// nowUnix 返回当前 Unix 秒级时间戳（供池内 lastUsed 记录）
func nowUnix() int64 {
	return time.Now().Unix()
}

// pooledSession 池中复用的一个 stdio 会话（一个 conn.ID 对应一个子进程）
type pooledSession struct {
	session     *mcp.ClientSession
	transport   *mcp.CommandTransport
	connID      string
	fingerprint string // command+args+work_dir+env 的哈希，配置变更后用于失效
	lastUsed    int64
	closed      bool // 子进程已退出
}

// SessionPool stdio 会话进程池：一个 conn.ID 复用一个 ClientSession / 子进程。
// streamable_http 为无状态短连接，不进池。
type SessionPool struct {
	mu       sync.Mutex
	sessions map[string]*pooledSession

	// progressRoutes 维护 progressToken -> onProgress 回调的映射，用于把
	// server 推送的 progress 通知路由回「发起本次调用的 onProgress」。
	// token 是调用级唯一（uuid string），跨 session 共享（stdio 复用同一子进程）。
	progressMu     sync.RWMutex
	progressRoutes map[string]func(*mcp.ProgressNotificationParams)
}

// sessionPool 全局单例
var sessionPool = &SessionPool{
	sessions:       make(map[string]*pooledSession),
	progressRoutes: make(map[string]func(*mcp.ProgressNotificationParams)),
}

// GetSessionPool 返回全局 stdio 会话进程池单例
func GetSessionPool() *SessionPool {
	return sessionPool
}

// Get 获取（或懒创建）某连接对应的 stdio 会话。
// 若池中已有同指纹会话且进程存活，则复用；否则新建并替换旧会话。
func (p *SessionPool) Get(ctx context.Context, conn *models.McpConnection) (*mcp.ClientSession, error) {
	if strings.ToLower(conn.Transport) != "stdio" {
		return nil, fmt.Errorf("SessionPool 仅支持 stdio transport，收到: %s", conn.Transport)
	}

	fp := fingerprint(conn)

	p.mu.Lock()
	existing, ok := p.sessions[conn.ID]
	// 已有且指纹一致且进程未退出 -> 复用
	if ok && existing.fingerprint == fp && !existing.closed {
		existing.lastUsed = nowUnix()
		session := existing.session
		p.mu.Unlock()
		return session, nil
	}
	p.mu.Unlock()

	// 需要新建：先失效旧会话
	if ok {
		p.Invalidate(conn.ID)
	}

	session, transport, err := newStdioSession(ctx, conn)
	if err != nil {
		return nil, err
	}

	ps := &pooledSession{
		session:     session,
		transport:   transport,
		connID:      conn.ID,
		fingerprint: fp,
		lastUsed:    nowUnix(),
	}
	// 监听子进程退出，标记为 closed
	go p.watchProcess(ps)

	p.mu.Lock()
	p.sessions[conn.ID] = ps
	p.mu.Unlock()
	return session, nil
}

// newStdioSession 建立一个新的 stdio 会话（与 NewClientSession 对 stdio 路径等价，但保留 transport 引用）
// 同时注入 ProgressNotificationHandler，按 progressToken 查表分发到对应 onProgress。
func newStdioSession(ctx context.Context, conn *models.McpConnection) (*mcp.ClientSession, *mcp.CommandTransport, error) {
	transport, err := newCommandTransport(conn)
	if err != nil {
		return nil, nil, err
	}
	client := mcp.NewClient(ClientImpl, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			p := sessionPool
			key := fmt.Sprint(req.Params.ProgressToken)
			p.progressMu.RLock()
			cb, ok := p.progressRoutes[key]
			p.progressMu.RUnlock()
			if ok && cb != nil {
				cb(req.Params)
			}
		},
	})
	session, err := connectSession(ctx, client, transport, conn)
	if err != nil {
		return nil, nil, err
	}
	ct, _ := transport.(*mcp.CommandTransport)
	return session, ct, nil
}

// RegisterProgress 注册 progressToken 对应的 onProgress 回调（调用前调用）。
func (p *SessionPool) RegisterProgress(token string, onProgress func(*mcp.ProgressNotificationParams)) {
	p.progressMu.Lock()
	p.progressRoutes[token] = onProgress
	p.progressMu.Unlock()
}

// UnregisterProgress 注销 progressToken 对应的回调（调用结束后调用）。
func (p *SessionPool) UnregisterProgress(token string) {
	p.progressMu.Lock()
	delete(p.progressRoutes, token)
	p.progressMu.Unlock()
}

// watchProcess 监听子进程退出，置 closed 标志。
// 注意：不要在此处从 map 中删除，避免并发 Get 误判；失效由 Invalidate 显式处理。
func (p *SessionPool) watchProcess(ps *pooledSession) {
	if ps.transport == nil || ps.transport.Command == nil {
		return
	}
	_ = ps.transport.Command.Wait()
	p.mu.Lock()
	if cur, ok := p.sessions[ps.connID]; ok && cur == ps {
		cur.closed = true
	}
	p.mu.Unlock()
}

// Invalidate 主动关闭并移除指定连接对应的池内会话（配置变更或禁用时调用）
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

// Close 关闭池内所有会话并清空（系统退出时调用）
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

// fingerprint 计算连接配置指纹，用于判断 stdio 子进程是否需要重建
func fingerprint(conn *models.McpConnection) string {
	raw, _ := json.Marshal(struct {
		Command string     `json:"command"`
		Args    xsql.JSONB `json:"args"`
		WorkDir string     `json:"work_dir"`
		Env     xsql.JSONB `json:"env"`
	}{conn.Command, conn.Args, conn.WorkDir, conn.Env})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum)
}
