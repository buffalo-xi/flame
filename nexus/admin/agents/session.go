package agents

import (
	"context"
	"strconv"
	"time"

	"flame/nexus/service/agent/core"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	sessionrecords "flame/nexus/shared/repository/sessionrecords"
	"flame/nexus/shared/repository/sessions"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// CreateSession 为指定 agent 新建会话。
func CreateSession(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	instance, err := repo.GetAgent(ai, id)
	if err != nil {
		xhttp.ErrJSON(c, "Agent instance not found", nil, "")
		return
	}

	sessionID, err := sessions.NewRepository().Create(ai.Schema, models.SessionOwnerTypeAgent, instance.ID, ai.ID, "")
	if err != nil {
		respondQuotaErr(c, err) // 会话数超限 → 507；其他错误 → 400
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"session_id": sessionID, "title": ""}, "")
}

// ListSessions 列出某 agent 的会话（updated_at 倒序）。
func ListSessions(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	keyword := c.Query("keyword")
	list, err := sessions.NewRepository().ListByOwner(ai.Schema, models.SessionOwnerTypeAgent, id, ai.ID, 50, keyword)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	items := make([]gin.H, 0, len(list))
	for _, s := range list {
		items = append(items, gin.H{
			"id":         s.ID,
			"title":      s.Title,
			"created_at": s.CreatedAt,
			"updated_at": s.UpdatedAt,
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}

// ListSessionMessages 分页列出某会话的历史消息（旧→新，默认每页 10 轮）。
func ListSessionMessages(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	sid, perr := sessions.ParseID(c.Param("id"))
	if perr != nil {
		xhttp.ErrJSON(c, perr.Error(), nil, "")
		return
	}

	// 存在性 + 可见性：原先只看「存在」，于是租户内任何用户拿到一个 UUID
	// 就能读到任意 agent / flow 的完整对话。这里不校验 owner（这条路由是
	// agent / flow 泛化的），只按 user 收口 —— 与 ListSessions 同一口径。
	sess, err := sessions.NewRepository().Get(ai.Schema, sid)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to get session: "+err.Error(), nil, "")
		return
	}
	if !sessionVisibleTo(sess, ai) {
		// 与「不存在」同一句话：否则这里就是一个探测 sid 是否存在的接口
		xhttp.ErrJSON(c, "Session not found", nil, "")
		return
	}

	// 分页：limit 每页轮数（默认 10，上限 100），before 加载早于该时间的记录
	limit := 10
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	var before *time.Time
	if v := c.Query("before"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			before = &t
		}
	}

	// 多取一条判断是否还有更早记录
	records, err := sessionrecords.NewRepository().ListBySessionPage(ai.Schema, sess.OwnerType, sess.OwnerID, sid, limit+1, before)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	records, hasMore := pageRecords(records, limit)

	messages := sessionMessages(records)

	oldestCreatedAt := ""
	if len(records) > 0 {
		oldestCreatedAt = time.Time(records[0].CreatedAt).Format(time.RFC3339Nano)
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"messages":          messages,
		"has_more":          hasMore,
		"oldest_created_at": oldestCreatedAt,
	}, "")
}

// pageRecords 处理「多取一条判断是否还有更早记录」的分页结果。
//
// 入参 records 是 ListBySessionPage 的返回值，顺序是**旧 → 新**（SQL 先按 created_at DESC
// 取最新的 limit+1 条，再整体反转）。所以多出来的那条落在**切片头部**（最旧），
// 必须从头部丢 —— 曾经写成 records[:limit] 从尾部截，砍掉的恰好是**最新一条**：
// 会话超过 10 条记录后，每次打开会话都看不到刚跑完的那一轮。
//
// 返回 hasMore：是否还有更早的记录可翻。
func pageRecords(records []models.SessionRecord, limit int) ([]models.SessionRecord, bool) {
	if limit > 0 && len(records) > limit {
		return records[len(records)-limit:], true
	}
	return records, false
}

// sessionMessages 把会话记录转成前端要的消息列表（user/assistant 交替）。
//
// 与 core.RecordsToMessages 的分工：那份给 LLM 当上下文（只要 role/content）；
// 这份给 UI —— 助手消息多带 duration_ms（执行用时）、status 与 error。
// 存量行 duration_ms 为 0，前端据此不显示那一行；存量行 status 已被列默认值补成 ok。
//
// 被终止 / 出错的一轮**也要出一条 assistant 消息**：那时 output 往往是空的，
// 若按「有正文才出消息」过滤，历史里就只剩用户那句提问，看不出这轮为什么没有回答。
func sessionMessages(records []models.SessionRecord) []gin.H {
	msgs := make([]gin.H, 0, len(records)*2)
	for _, rec := range records {
		// 同一轮的 user / assistant 共用记录的创建时间：用户看到的是「这条消息什么时候发的」，
		// 而不是数据库写入时间。前端据此在消息下方显示 HH:mm。
		createdAt := time.Time(rec.CreatedAt).Format(time.RFC3339)
		msgs = append(msgs, gin.H{
			"role":       "user",
			"content":    core.RecordUserContent(rec),
			"created_at": createdAt,
		})
		status := rec.Status
		if status == "" {
			status = models.SessionStatusOK
		}
		if rec.Output != "" || status != models.SessionStatusOK {
			msgs = append(msgs, gin.H{
				"role":        "assistant",
				"content":     rec.Output,
				"duration_ms": rec.DurationMs,
				"status":      status,
				"error":       rec.Error,
				"created_at":  createdAt,
			})
		}
	}
	return msgs
}

// DeleteSession 删除会话（连带删除其会话记录）。
func DeleteSession(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	sid, perr := sessions.ParseID(c.Param("id"))
	if perr != nil {
		xhttp.ErrJSON(c, perr.Error(), nil, "")
		return
	}

	// 删除同样要过可见性：这个接口会连带删掉 session_records 与**整个会话工作区目录**，
	// 不校验就等于任何租户用户都能清空别人的产物。口径与 ListSessionMessages 一致。
	sess, err := sessions.NewRepository().Get(ai.Schema, sid)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to get session: "+err.Error(), nil, "")
		return
	}
	if !sessionVisibleTo(sess, ai) {
		xhttp.ErrJSON(c, "Session not found", nil, "")
		return
	}

	if derr := sessions.NewRepository().Delete(ai.Schema, sid); derr != nil {
		xhttp.ErrJSON(c, derr.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", sid, "")
}

// GetSessionUsage 返回会话工作区的当前用量与两个上限（工作区面板的用量条用）。
//
// 只读计数，不扫描目录：数字可能比真实值滞后到下一次写入校准，但足够用来提示
// 「快满了/已经满了」，不需要为了一个进度条去扫一遍存储。
func GetSessionUsage(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	sid, perr := sessions.ParseID(c.Param("id"))
	if perr != nil {
		xhttp.ErrJSON(c, perr.Error(), nil, "")
		return
	}
	sess, err := sessions.NewRepository().Get(ai.Schema, sid)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to get session: "+err.Error(), nil, "")
		return
	}
	if !sessionVisibleTo(sess, ai) {
		xhttp.ErrJSON(c, "Session not found", nil, "")
		return
	}

	key := paths.UserSessionKey(ai.Schema, sess.UserID, sid)
	// 上限按会话归属用户解析：user_quotas 有覆盖时用覆盖值，否则回退租户值
	used, limit := quota.Snapshot(context.Background(), ai.Schema, key, sess.UserID)

	// 会话数：给前端提示「还能不能新建」，失败时按 0/未知处理，不阻断面板渲染。
	// 上限同样按用户解析（user_quotas 可覆盖租户值）。
	sessionsUsed, _ := quota.CountUserSessions(ai.Schema, models.SessionOwnerTypeAgent, ai.ID)
	sessionsLimit := quota.ResolveUserSessions(ai.Schema, ai.ID)

	xhttp.OkJSON(c, "Success", gin.H{
		"used_bytes":       used,
		"limit_bytes":      limit,
		"sessions_used":    sessionsUsed,
		"sessions_limit":   sessionsLimit,
		"storage_limited":  limit != -1,
		"sessions_limited": sessionsLimit != -1,
	}, "")
}

// GetLatestSession 返回当前用户最近一条 agent 会话（用于进入时恢复最近对话的 agent）。
func GetLatestSession(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)

	list, err := sessions.NewRepository().ListByUser(ai.Schema, models.SessionOwnerTypeAgent, ai.ID, 1)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if len(list) == 0 {
		xhttp.OkJSON(c, "Success", gin.H{"session": nil}, "")
		return
	}
	s := list[0]
	xhttp.OkJSON(c, "Success", gin.H{
		"session": gin.H{
			"id":         s.ID,
			"agent_id":   s.OwnerID,
			"title":      s.Title,
			"updated_at": s.UpdatedAt,
		},
	}, "")
}
