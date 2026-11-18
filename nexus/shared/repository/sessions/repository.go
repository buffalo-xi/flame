package sessions

import (
	"errors"
	"strings"

	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// 会话解析的三类失败。分开是为了让入口层能给出不同的协议错误码，
// 同时保证「不存在」与「不是你的」在**对外文案**上永远合流（见 Lookup）。
var (
	// ErrSessionRequired 需要 session_id 但没给。
	ErrSessionRequired = errors.New("session_id is required")
	// ErrSessionInvalid session_id 不是合法 UUID。
	// 必须先挡住：sessions.id / session_records.session_id 都是 uuid 列，
	// 把脏字符串交给 PG 只会换来一句「invalid input syntax for type uuid」。
	ErrSessionInvalid = errors.New("session_id is not a valid UUID")
	// ErrSessionForbidden 会话不存在，或不属于这个 owner。
	// 两种情况刻意共用同一个错误：分开就等于提供了一个探测 sid 是否存在的接口。
	ErrSessionForbidden = errors.New("session not found or not owned by this owner")
)

// ParseID 规范化并校验客户端给来的会话 id：空 → ErrSessionRequired；非 UUID → ErrSessionInvalid。
//
// 任何要把 sid 交给 SQL 的地方都必须先过这一道 —— sessions.id / session_records.session_id
// 都是 uuid 列，脏字符串换来的是 PG 的 22P02，那既不是给调用方看的错误，
// 也会把「这个值被当成 id 用了」的实现细节漏到响应里。
func ParseID(sessionID string) (string, error) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", ErrSessionRequired
	}
	if _, err := uuid.Parse(sid); err != nil {
		return "", ErrSessionInvalid
	}
	return sid, nil
}

// ResolveForUser 按「会话归属用户」解析 session_id —— 外部入口（HTTP chat / MCP / RPC）的统一判据。
//
// 与 Lookup 的区别：只校验 user_id，不校验 owner。会话归属的是用户，
// owner_type / owner_id 只记录创建者；父子 agent 共用同一个 session 时 owner 必然对不上，
// 用 owner 收口会把 call_agent 的合法级联整体拒掉。
//
// 失败口径与 Lookup 一致：空 → ErrSessionRequired；非 UUID → ErrSessionInvalid；
// 不存在或不属于该用户 → ErrSessionForbidden（两者同一句话，防探测）。
func ResolveForUser(schema, sessionID, userID string) (*models.Session, error) {
	id, err := ParseID(sessionID)
	if err != nil {
		return nil, err
	}
	sess, err := NewRepository().Get(schema, id)
	if err != nil {
		return nil, err
	}
	if !sess.VisibleToUser(userID) {
		return nil, ErrSessionForbidden
	}
	return sess, nil
}

// ResolveOrCreateForUser 按用户口径解析会话；不存在则用该 id 创建（受会话数配额约束）。
//
// 协议端点（RPC / MCP）专用：调用方自带 UUID，首次调用即建档。
// 与 ResolveForUser 一样只校验 user_id —— 会话归属用户，owner 只记录创建者。
func ResolveOrCreateForUser(schema, sessionID, ownerType, ownerID, userID, title string) (*models.Session, error) {
	id, err := ParseID(sessionID)
	if err != nil {
		return nil, err
	}
	repo := NewRepository()
	sess, err := repo.Get(schema, id)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		if !sess.VisibleToUser(userID) {
			return nil, ErrSessionForbidden
		}
		return sess, nil
	}
	return repo.CreateWithID(schema, id, ownerType, ownerID, userID, title)
}

// Repository 会话存储接口（泛化：agent + flow）
type Repository interface {
	// Create 创建会话（userID 可空），返回会话 ID。
	// 会话数超限返回 *quota.ExceededError（控制台据此回 507，由人删旧会话后再试）。
	Create(schema, ownerType, ownerID, userID, title string) (string, error)
	// CreateWithID 用指定 id 幂等建会话：冲突时回读并校验可见性，且走会话数配额。
	// 协议端点（调用方自带 UUID）用这个，不要用 EnsureExists。
	// 与 Create 的差别：超限时自动回收该用户最旧的会话再建（端点没有删除接口）。
	CreateWithID(schema, id, ownerType, ownerID, userID, title string) (*models.Session, error)
	// ListByOwner 列出某 owner 的会话（updated_at 倒序）。
	// userID 非空按用户过滤；limit<=0 不限制；keyword 非空按 title 模糊匹配。
	ListByOwner(schema, ownerType, ownerID, userID string, limit int, keyword string) ([]models.Session, error)
	// ListByUser 列出某用户指定 owner_type 的全部会话（updated_at 倒序）。limit<=0 不限制。
	ListByUser(schema, ownerType, userID string, limit int) ([]models.Session, error)
	// Get 按 ID 获取会话；不存在返回 (nil, nil)
	Get(schema, sessionID string) (*models.Session, error)
	// Lookup 按 ID 取会话并校验「创建者」(ownerType, ownerID)：
	// 空 → ErrSessionRequired；非 UUID → ErrSessionInvalid；
	// 不存在或不属于 (ownerType, ownerID) → ErrSessionForbidden（两者同一句话，防探测）。
	//
	// 外部入口（HTTP chat / MCP / RPC）请用 ResolveForUser 按 user 收口：
	// 会话归属的是用户，owner 只记录创建者；父子 agent 共用同一 session 时 owner 对不上。
	// 这里保留 owner 维度，供「服务端自签票据」这类没有登录态、只确认创建者的场景。
	Lookup(schema, sessionID, ownerType, ownerID string) (*models.Session, error)
	// UpdateTitle 更新标题并刷新 updated_at
	UpdateTitle(schema, sessionID, title string) error
	// Touch 刷新 updated_at（对话后调用）
	Touch(schema, sessionID string) error
	// EnsureExists 确保指定 id 的会话存在：不存在则创建（用指定 id），存在则刷新 updated_at
	EnsureExists(schema, id, ownerType, ownerID, userID, title string) error
	// Delete 删除会话（连带删除其 session_records）
	Delete(schema, sessionID string) error
}
