package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"sync"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/storage"
	"flame/tools/xlog"

	"github.com/google/uuid"
)

// PostgresRepository PostgreSQL 实现的会话存储
type PostgresRepository struct{}

// Create 创建会话（userID 可空），返回新会话 ID。
//
// 同时建好会话工作目录（users/{uid}/sessions/{sid}/）：它是文件工具默认根与 exec 的 cwd，
// 必须在第一次使用前存在（S3 无空目录，写 .keep 占位）。
func (r *PostgresRepository) Create(schema, ownerType, ownerID, userID, title string) (string, error) {
	// 会话数配额：控制台建会话超限直接拒绝（507），由人手工删旧会话 ——
	// 聊天记录不替用户做决定。协议端点的自动回收在 CreateWithID 里（那边调用方没有删除接口）。
	if err := quota.CheckUserSessions(schema, ownerType, userID); err != nil {
		return "", err
	}
	id := uuid.NewString()
	_, err := db.DB.Exec(`
		INSERT INTO `+schema+`.sessions (id, owner_type, owner_id, user_id, title)
		VALUES ($1,$2,$3,$4,$5)`,
		id, ownerType, ownerID, nullIfEmpty(userID), title)
	if err != nil {
		return "", err
	}
	ensureSessionDir(schema, userID, id)
	return id, nil
}

// ListByOwner 列出某 owner 的会话（updated_at 倒序）。userID 非空按用户过滤；limit<=0 不限制；keyword 非空按 title 模糊匹配。
func (r *PostgresRepository) ListByOwner(schema, ownerType, ownerID, userID string, limit int, keyword string) ([]models.Session, error) {
	where := "WHERE owner_type = $1 AND owner_id = $2"
	args := []interface{}{ownerType, ownerID}
	argIdx := 3
	if userID != "" {
		where += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, userID)
		argIdx++
	}
	if keyword != "" {
		where += fmt.Sprintf(" AND title ILIKE $%d", argIdx)
		args = append(args, "%"+keyword+"%")
		argIdx++
	}

	listSQL := `SELECT id, owner_type, owner_id, user_id, title, created_at, updated_at
		FROM ` + schema + `.sessions ` + where + ` ORDER BY updated_at DESC, created_at DESC`
	if limit > 0 {
		listSQL += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, limit)
	}

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.Session, 0)
	for rows.Next() {
		var s models.Session
		if err := rows.Scan(&s.ID, &s.OwnerType, &s.OwnerID, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// ListByUser 列出某用户指定 owner_type 的全部会话（updated_at 倒序）。limit<=0 不限制。
func (r *PostgresRepository) ListByUser(schema, ownerType, userID string, limit int) ([]models.Session, error) {
	where := "WHERE owner_type = $1 AND user_id = $2"
	args := []interface{}{ownerType, userID}
	argIdx := 3

	listSQL := `SELECT id, owner_type, owner_id, user_id, title, created_at, updated_at
		FROM ` + schema + `.sessions ` + where + ` ORDER BY updated_at DESC, created_at DESC`
	if limit > 0 {
		listSQL += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, limit)
	}

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.Session, 0)
	for rows.Next() {
		var s models.Session
		if err := rows.Scan(&s.ID, &s.OwnerType, &s.OwnerID, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// Get 按 ID 获取会话；不存在返回 (nil, nil)。
func (r *PostgresRepository) Get(schema, sessionID string) (*models.Session, error) {
	var s models.Session
	err := db.DB.QueryRow(`
		SELECT id, owner_type, owner_id, user_id, title, created_at, updated_at
		FROM `+schema+`.sessions
		WHERE id = $1`, sessionID).Scan(&s.ID, &s.OwnerType, &s.OwnerID, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// Lookup 按 ID 取会话并校验归属。见接口注释：三类失败分开，对外文案合流。
//
// 先校验 UUID 再查库：sessions.id 是 uuid 列，脏字符串会让 PG 报 22P02，
// 那种错误对调用方毫无意义，还会把 SQL 细节透到 HTTP 响应里。
func (r *PostgresRepository) Lookup(schema, sessionID, ownerType, ownerID string) (*models.Session, error) {
	sid, herr := ParseID(sessionID)
	if herr != nil {
		return nil, herr
	}
	sess, err := r.Get(schema, sid)
	if err != nil {
		return nil, err
	}
	if !sess.OwnedBy(ownerType, ownerID) {
		return nil, ErrSessionForbidden
	}
	return sess, nil
}

// UpdateTitle 更新标题并刷新 updated_at。
func (r *PostgresRepository) UpdateTitle(schema, sessionID, title string) error {
	_, err := db.DB.Exec(`UPDATE `+schema+`.sessions SET title = $1, updated_at = now() WHERE id = $2`, title, sessionID)
	return err
}

// Touch 刷新 updated_at。
func (r *PostgresRepository) Touch(schema, sessionID string) error {
	_, err := db.DB.Exec(`UPDATE `+schema+`.sessions SET updated_at = now() WHERE id = $1`, sessionID)
	return err
}

// EnsureExists 确保指定 id 的会话存在：不存在则创建（用指定 id），存在则刷新 updated_at。
func (r *PostgresRepository) EnsureExists(schema, id, ownerType, ownerID, userID, title string) error {
	_, err := db.DB.Exec(`
		INSERT INTO `+schema+`.sessions (id, owner_type, owner_id, user_id, title)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET updated_at = now()`,
		id, ownerType, ownerID, nullIfEmpty(userID), title)
	if err != nil {
		return err
	}
	ensureSessionDir(schema, userID, id)
	return nil
}

// CreateWithID 用调用方指定的 id 幂等建会话（协议端点传了不存在的 session_id 时使用）。
//
// 与 EnsureExists 的差别：冲突时先回读再校验可见性，绝不把调用挂到别人的会话上；
// 且走会话数配额（EnsureExists 这两件都没做）。
func (r *PostgresRepository) CreateWithID(schema, id, ownerType, ownerID, userID, title string) (*models.Session, error) {
	// 协议端点专用：超限时自动回收该用户最旧的会话腾位置（见 ensureSessionSlot）。
	if err := r.ensureSessionSlot(schema, ownerType, userID); err != nil {
		return nil, err
	}
	if _, err := db.DB.Exec(`
		INSERT INTO `+schema+`.sessions (id, owner_type, owner_id, user_id, title)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`,
		id, ownerType, ownerID, nullIfEmpty(userID), title); err != nil {
		return nil, err
	}
	// 回读：并发下另一个请求可能刚用同一个 id 建好（uuid 随机，概率极低但必须兜住）
	sess, err := r.Get(schema, id)
	if err != nil {
		return nil, err
	}
	if sess == nil || !sess.VisibleToUser(userID) {
		return nil, ErrSessionForbidden
	}
	ensureSessionDir(schema, userID, id)
	return sess, nil
}

// ensureSessionSlot 协议端点的会话数配额：超限时自动回收该用户在该 owner 类型下最旧的会话。
//
// 为什么只在这里回收：RPC / MCP 的会话由调用方自管生命周期（每次自带 UUID），
// 而它们没有删除会话的接口 —— 到上限后若一律拒绝，集成方就永久卡死。
// 控制台（Create）不走这里：那边超限返回 507，由人决定删哪个。
// 配额按 (owner_type, user_id) 分桶，端点凭证通常绑定了独立账号，回收不会挤掉控制台会话。
//
// 不回收、直接返回配额错误的情况：
//   - 上限 <= 0：0 = 禁止新建；-1 = 不限（CheckUserSessions 已放行，走不到这里）
//   - 回收后仍然超限（并发创建抢了位置）
//
// 已知取舍：只按 updated_at 最旧挑，不检查该会话是否有正在跑的 run。
// 正在跑的会话每轮结束都会刷新 updated_at，而回收目标是「最久没用过的」，
// 命中活会话的概率极低；要彻底杜绝得把 run 注册表从 service/agent 挪到 shared（跨包循环）。
func (r *PostgresRepository) ensureSessionSlot(schema, ownerType, userID string) error {
	err := quota.CheckUserSessions(schema, ownerType, userID)
	if err == nil {
		return nil
	}
	var qe *quota.ExceededError
	if !errors.As(err, &qe) || qe.Metric != "sessions" || qe.Limit <= 0 {
		return err
	}
	need := evictCount(int(qe.Used), int(qe.Limit))
	n, eerr := r.evictOldestSessions(schema, ownerType, userID, need)
	if eerr != nil {
		xlog.Error("自动回收会话失败 schema=%s owner=%s user=%s: %v", schema, ownerType, userID, eerr)
		return err
	}
	xlog.Info("会话数达到上限，自动回收最旧会话 schema=%s owner=%s user=%s used=%d limit=%d evicted=%d",
		schema, ownerType, userID, qe.Used, qe.Limit, n)
	// 复查：并发下可能仍超限，那就把配额错误如实返回（HTTP 507 / RPC -32000）
	return quota.CheckUserSessions(schema, ownerType, userID)
}

// evictCount 返回「把 used 降到 limit 以下」需要腾出的条数；limit<=0 表示不允许新建。
func evictCount(used, limit int) int {
	if limit <= 0 || used < limit {
		return 0
	}
	return used - limit + 1
}

// evictOldestSessions 按 updated_at 从旧到新删除 n 个会话（连带记录、目录、用量计数），返回实际删除数。
//
// 逐个走 Delete：每个会话要清「记录 + 行 + 目录 + 用量」四样，批量实现等于把 Delete 抄一遍。
// 单条失败跳过并继续，尽量腾出位置；够不够由调用方复查配额决定。
func (r *PostgresRepository) evictOldestSessions(schema, ownerType, userID string, n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	rows, err := db.DB.Query(`
		SELECT id FROM `+schema+`.sessions
		WHERE owner_type = $1 AND user_id = $2
		ORDER BY updated_at ASC
		LIMIT $3`, ownerType, userID, n)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, n)
	for rows.Next() {
		var id string
		if serr := rows.Scan(&id); serr != nil {
			rows.Close()
			return 0, serr
		}
		ids = append(ids, id)
	}
	if rerr := rows.Err(); rerr != nil {
		rows.Close()
		return 0, rerr
	}
	rows.Close()

	deleted := 0
	for _, id := range ids {
		if derr := r.Delete(schema, id); derr != nil {
			xlog.Error("回收会话失败 schema=%s session=%s: %v", schema, id, derr)
			continue
		}
		deleted++
	}
	return deleted, nil
}

// nullIfEmpty 空字符串转 nil（uuid 列不接受空串）。
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// Delete 删除会话（连带删除其 session_records 与会话工作目录）。
func (r *PostgresRepository) Delete(schema, sessionID string) error {
	// 先取 user_id：会话目录按 user 组织，删行后就无法定位了
	sess, _ := r.Get(schema, sessionID)

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM `+schema+`.session_records WHERE session_id = $1`, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+schema+`.sessions WHERE id = $1`, sessionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if sess != nil {
		removeSessionDir(schema, sess.UserID, sessionID)
		// 目录没了，会话用量计数也一并清掉（否则同名重建会继承旧用量）
		quota.Reset(context.Background(), schema, paths.UserSessionKey(schema, sess.UserID, sessionID))
	}
	return nil
}

// ============================================================================
// 会话工作目录生命周期（与 DB 行同步）
//   目录形态：users/{uid}/sessions/{sid}/；user 为空时不建（目录按 user 组织）。
// ============================================================================

var (
	sessStoreOnce sync.Once
	sessStore     storage.Storage
	sessStoreErr  error
)

// sessionStorage 懒加载会话目录所用的存储实例（与 agent 存储同配置）。
func sessionStorage() (storage.Storage, error) {
	sessStoreOnce.Do(func() {
		if config.Cfg == nil {
			sessStoreErr = fmt.Errorf("config not loaded")
			return
		}
		sessStore, sessStoreErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return sessStore, sessStoreErr
}

// ensureSessionDir 建会话工作目录（S3 无空目录，写 .keep 占位）。
func ensureSessionDir(schema, userID, sessionID string) {
	if userID == "" {
		return
	}
	st, err := sessionStorage()
	if err != nil || st == nil {
		return
	}
	key := path.Join(paths.UserSessionKey(schema, userID, sessionID), ".keep")
	_ = st.WriteFile(context.Background(), key, nil)
}

// removeSessionDir 删会话工作目录（不存在则跳过）。
func removeSessionDir(schema, userID, sessionID string) {
	if userID == "" {
		return
	}
	st, err := sessionStorage()
	if err != nil || st == nil {
		return
	}
	_ = st.DeletePrefix(context.Background(), paths.UserSessionKey(schema, userID, sessionID))
}
