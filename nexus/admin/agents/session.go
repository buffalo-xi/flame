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

// CreateSession opens a new session for the given agent.
func CreateSession(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	instance, err := repo.GetAgent(ai, id)
	if err != nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
		return
	}

	sessionID, err := sessions.NewRepository().Create(ai.Schema, models.SessionOwnerTypeAgent, instance.ID, ai.ID, "")
	if err != nil {
		respondQuotaErr(c, err) // session count exceeded -> 507; anything else -> 400
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"session_id": sessionID, "title": ""}, "")
}

// ListSessions lists the sessions of one agent (updated_at descending).
func ListSessions(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	keyword := c.Query("keyword")
	list, err := sessions.NewRepository().ListByOwner(ai.Schema, models.SessionOwnerTypeAgent, id, ai.ID, 50, keyword)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list the sessions: "+err.Error(), "")
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
	// Session-count quota alongside the list, so the panel can show "used / limit" before "new chat" hits 507.
	// The count is bucketed by (owner_type, user_id) - the same rule Create enforces - so it spans this user's agents.
	// A failed count degrades to 0 rather than breaking the list.
	used, _ := quota.CountUserSessions(ai.Schema, models.SessionOwnerTypeAgent, ai.ID)
	limit := quota.ResolveUserSessions(ai.Schema, ai.ID)

	xhttp.OkJSON(c, "Success", gin.H{
		"items":          items,
		"sessions_used":  used,
		"sessions_limit": limit,
	}, "")
}

// ListSessionMessages pages through the history of one session (old to new, 10 turns per page by default).
func ListSessionMessages(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	sid, perr := sessions.ParseID(c.Param("id"))
	if perr != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, perr.Error(), "")
		return
	}

	// Existence plus visibility: the original code only checked existence, so any user in
	// the tenant who obtained a UUID could read the full conversation of any agent / flow.
	// The owner is not checked here (this route is generic across agent / flow); scoping by
	// user matches ListSessions.
	sess, err := sessions.NewRepository().Get(ai.Schema, sid)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the session: "+err.Error(), "")
		return
	}
	if !sessionVisibleTo(sess, ai) {
		// Same wording as "not found": otherwise this endpoint becomes an sid probe
		xhttp.ErrCode(c, "session.not_found", nil, "Session not found", "")
		return
	}

	// Paging: limit is turns per page (default 10, max 100), before loads records older than that time
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

	// Fetch one extra row to know whether older records exist
	records, err := sessionrecords.NewRepository().ListBySessionPage(ai.Schema, sess.OwnerType, sess.OwnerID, sid, limit+1, before)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list the session records: "+err.Error(), "")
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

// pageRecords handles the "fetch one extra row to know whether older records exist" paging.
//
// records is what ListBySessionPage returns, in **old to new** order (the SQL first takes the
// newest limit+1 rows by created_at DESC, then reverses them). The extra row therefore sits at
// the **head of the slice** (the oldest) and must be dropped from the head - an earlier version
// used records[:limit] and cut the **newest** row instead: once a session had more than 10
// records, every open missed the turn that had just finished.
//
// hasMore says whether older records are still available.
func pageRecords(records []models.SessionRecord, limit int) ([]models.SessionRecord, bool) {
	if limit > 0 && len(records) > limit {
		return records[len(records)-limit:], true
	}
	return records, false
}

// sessionMessages turns session records into the message list the frontend wants (user /
// assistant alternating).
//
// Division of labour with core.RecordsToMessages: that one feeds the LLM context (role and
// content only); this one feeds the UI - assistant messages also carry duration_ms (execution
// time), status and error. Legacy rows have duration_ms = 0, which the frontend uses to hide
// that line; a legacy empty status has already been backfilled to ok by the column default.
//
// A stopped / failed turn **still produces an assistant message**: the output is often empty
// then, and filtering on "body present" would leave only the user's question in the history,
// with no trace of why the turn never answered.
func sessionMessages(records []models.SessionRecord) []gin.H {
	msgs := make([]gin.H, 0, len(records)*2)
	for _, rec := range records {
		// The user / assistant pair of one turn share the record's creation time: what the user
		// sees is "when this message was sent", not when the row was written. The frontend shows
		// HH:mm under each message from it.
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

// DeleteSession deletes a session along with its session records.
func DeleteSession(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	sid, perr := sessions.ParseID(c.Param("id"))
	if perr != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, perr.Error(), "")
		return
	}

	// Deletion needs the visibility check too: this endpoint also removes the session_records
	// and **the whole session workspace directory**, so skipping it would let any tenant user
	// wipe someone else's artifacts. Same rule as ListSessionMessages.
	sess, err := sessions.NewRepository().Get(ai.Schema, sid)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the session: "+err.Error(), "")
		return
	}
	if !sessionVisibleTo(sess, ai) {
		xhttp.ErrCode(c, "session.not_found", nil, "Session not found", "")
		return
	}

	if derr := sessions.NewRepository().Delete(ai.Schema, sid); derr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete the session: "+derr.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", sid, "")
}

// GetSessionUsage returns the current usage of a session workspace plus its two limits (for
// the usage bar in the workspace panel).
//
// It is a read of counters, not a directory scan: the number can lag behind reality until the
// next write recalibrates it, which is enough to say "almost full / already full" without
// scanning storage for a progress bar.
func GetSessionUsage(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)
	sid, perr := sessions.ParseID(c.Param("id"))
	if perr != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, perr.Error(), "")
		return
	}
	sess, err := sessions.NewRepository().Get(ai.Schema, sid)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the session: "+err.Error(), "")
		return
	}
	if !sessionVisibleTo(sess, ai) {
		xhttp.ErrCode(c, "session.not_found", nil, "Session not found", "")
		return
	}

	key := paths.UserSessionKey(ai.Schema, sess.UserID, sid)
	// Limits resolve against the session's owning user: the user_quotas override when present, the tenant value otherwise
	used, limit := quota.Snapshot(context.Background(), ai.Schema, key, sess.UserID)

	// Session count: tells the frontend whether a new session can be created. On failure it is
	// treated as 0/unknown rather than blocking the panel render.
	// The limit resolves per user too (user_quotas may override the tenant value).
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

// GetLatestSession returns the current user's most recent agent session (used to restore the
// last conversation's agent on entry).
func GetLatestSession(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)

	list, err := sessions.NewRepository().ListByUser(ai.Schema, models.SessionOwnerTypeAgent, ai.ID, 1)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list the sessions: "+err.Error(), "")
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
