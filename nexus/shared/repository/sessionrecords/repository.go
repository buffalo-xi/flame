package sessionrecords

import (
	"time"

	"flame/nexus/shared/models"
)

// Repository 会话记录存储接口
type Repository interface {
	// Append 追加一条记录，并按 maxN 裁剪该 session 超出的旧记录（maxN<=0 不裁剪）
	Append(schema string, rec *models.SessionRecord, maxN int) error
	// ListBySession 读取某 owner + session 的历史记录（旧→新）。
	// limit<=0 不限制条数；since 非空时只取 created_at >= since 的记录。
	ListBySession(schema, ownerType, ownerID, sessionID string, limit int, since *time.Time) ([]models.SessionRecord, error)
	// ListBySessionPage 分页读取历史记录：取 created_at < before 的最近 limit 条，返回旧→新。
	// before 为 nil 表示从最新开始；limit<=0 不限制。
	ListBySessionPage(schema, ownerType, ownerID, sessionID string, limit int, before *time.Time) ([]models.SessionRecord, error)
}
