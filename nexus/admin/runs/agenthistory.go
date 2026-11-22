package runs

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/agentruns"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Agent run history (admin/running/agent-history).
//
// A different data set from the workflow history next door: this reads agent_run_records, which is written
// once per agent run (including stateless MCP / RPC calls that carry no session), while the workflow page
// reads flow_run_records. The two are shown on separate pages and are never merged: one row here is "this
// agent answered this question in this run", not "this workflow executed these nodes".
//
// Deleting from this table never touches a conversation: the memory the model reads back lives in
// session_records, which this page neither reads nor writes.
var agentRunRepo = agentruns.NewRepository()

// agentHistoryItem is one row of the agent history list.
//
// It omits the stored answer preview (the detail endpoint returns it): twenty rows carrying 4 KiB answers
// each would turn one page into a hundred kilobytes, and the list column reads the question only.
type agentHistoryItem struct {
	ID              string     `json:"id"`
	AgentID         string     `json:"agent_id"`
	AgentName       string     `json:"agent_name"`
	SessionID       string     `json:"session_id"`
	SessionTitle    string     `json:"session_title"`
	RunID           string     `json:"run_id"`
	UserID          string     `json:"user_id"`
	UserName        string     `json:"user_name"` // joined in bulk; empty when the user was deleted or never identified
	RunType         string     `json:"run_type"`
	Status          string     `json:"status"`
	ErrMsg          string     `json:"err_msg"`
	DurationMs      int64      `json:"duration_ms"`
	StartedAt       xsql.Time  `json:"started_at"`
	FinishedAt      *xsql.Time `json:"finished_at"`
	Question        string     `json:"question"`
	QuestionSize    int        `json:"question_size"`
	AnswerSize      int        `json:"answer_size"`
	AnswerTruncated bool       `json:"answer_truncated"`
}

// agentHistoryDetail is the detail drawer's payload: the list row plus the stored answer preview.
type agentHistoryDetail struct {
	*agentHistoryItem
	Answer string `json:"answer"`
}

// agentHistoryQuery parses the filters shared by the list / export / cleanup endpoints.
//
// A malformed timestamp or session id is reported as ok=false instead of being dropped: on the DELETE path a
// silently missing bound widens the deletion beyond what the user saw and confirmed. The workflow history
// learned this the hard way (see history.go) - do not relax it here.
func agentHistoryQuery(c *gin.Context) (agentruns.Filter, bool) {
	f := agentruns.Filter{
		AgentID:   strings.TrimSpace(c.Query("agent_id")),
		SessionID: strings.TrimSpace(c.Query("session_id")),
		// Caller filter: "which runs did this user trigger". Empty means every caller, including the rows without an
		// identity (an anonymous RPC call, or a record written before the column had a value).
		UserID:  strings.TrimSpace(c.Query("user_id")),
		Status:  strings.TrimSpace(c.Query("status")),
		RunType: strings.TrimSpace(c.Query("run_type")),
	}
	// The session is a uuid column: reject a partial paste here (the console shows short ids) rather than let
	// PostgreSQL answer 22P02 with SQL noise.
	if f.SessionID != "" {
		if _, err := uuid.Parse(f.SessionID); err != nil {
			return f, false
		}
	}
	if v := c.Query("start_from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, false
		}
		f.From = &t
	}
	if v := c.Query("start_to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, false
		}
		f.To = &t
	}
	return f, true
}

// pageArgs reads limit / offset (limit defaults to 20 and is capped at 200).
func pageArgs(c *gin.Context) (int, int) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// agentNameMap fetches agent names in bulk. A missing name (the agent was deleted) stays empty so one
// orphan row does not fail the whole page.
func agentNameMap(schema string, ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	rows, err := db.DB.Query(
		`SELECT id::text, name FROM `+schema+`.agents WHERE id::text = ANY($1::text[])`, pq.Array(ids))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			out[id] = name
		}
	}
	return out
}

// sessionTitleMap fetches session titles in bulk. Stateless runs have no session and simply miss.
func sessionTitleMap(schema string, ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	rows, err := db.DB.Query(
		`SELECT id::text, title FROM `+schema+`.sessions WHERE id::text = ANY($1::text[])`, pq.Array(ids))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, title string
		if rows.Scan(&id, &title) == nil {
			out[id] = title
		}
	}
	return out
}

// userNameMap resolves run callers in bulk, the same way agentNameMap resolves agent names: a page of 20 rows would
// otherwise mean 20 lookups, and a user deleted after the run must not blank the row.
func userNameMap(schema string, ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	rows, err := db.DB.Query(
		`SELECT id::text, COALESCE(NULLIF(display_name, ''), name) FROM `+schema+`.users WHERE id::text = ANY($1::text[])`,
		pq.Array(ids))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			out[id] = name
		}
	}
	return out
}

// toAgentHistoryItems attaches the agent name, the session title and the caller name the page shows next to the raw ids.
func toAgentHistoryItems(schema string, records []*models.AgentRunRecord) []*agentHistoryItem {
	agentIDs := make([]string, 0, len(records))
	sessionIDs := make([]string, 0, len(records))
	userIDs := make([]string, 0, len(records))
	for _, r := range records {
		agentIDs = append(agentIDs, r.AgentID)
		if r.SessionID != "" {
			sessionIDs = append(sessionIDs, r.SessionID)
		}
		if r.UserID != "" {
			userIDs = append(userIDs, r.UserID)
		}
	}
	agents := agentNameMap(schema, agentIDs)
	sessions := sessionTitleMap(schema, sessionIDs)
	users := userNameMap(schema, userIDs)

	items := make([]*agentHistoryItem, 0, len(records))
	for _, r := range records {
		items = append(items, toAgentHistoryItem(r, agents[r.AgentID], sessions[r.SessionID], users[r.UserID]))
	}
	return items
}

func toAgentHistoryItem(r *models.AgentRunRecord, agentName, sessionTitle, userName string) *agentHistoryItem {
	return &agentHistoryItem{
		ID:              r.ID,
		AgentID:         r.AgentID,
		AgentName:       agentName,
		SessionID:       r.SessionID,
		SessionTitle:    sessionTitle,
		RunID:           r.ID, // the run identity is the row id; the JSON key stays run_id for existing consumers
		UserID:          r.UserID,
		UserName:        userName,
		RunType:         r.RunType,
		Status:          r.Status,
		ErrMsg:          r.ErrMsg,
		DurationMs:      r.DurationMs,
		StartedAt:       r.StartedAt,
		FinishedAt:      r.FinishedAt,
		Question:        r.Question,
		QuestionSize:    r.QuestionSize,
		AnswerSize:      r.AnswerSize,
		AnswerTruncated: r.AnswerTruncated,
	}
}

// ListAgentHistory lists past agent runs across agents and sessions.
// GET /admin/running/agent-history?agent_id=&status=&run_type=&start_from=&start_to=&limit=&offset=
func ListAgentHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	f, ok := agentHistoryQuery(c)
	if !ok {
		xhttp.ErrCode(c, "common.invalid_params", nil, "start_from / start_to must be RFC3339 timestamps; session_id must be a UUID", "")
		return
	}
	limit, offset := pageArgs(c)

	records, total, err := agentRunRepo.ListRuns(info.Schema, f, limit, offset)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"items":  toAgentHistoryItems(info.Schema, records),
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}, "")
}

// GetAgentHistory returns one run including the stored answer preview.
// GET /admin/running/agent-history/:id
func GetAgentHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}

	rec, err := agentRunRepo.GetRun(info.Schema, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if rec == nil {
		xhttp.ErrCode(c, "run.not_found", nil, "Run record not found", "")
		return
	}

	agents := agentNameMap(info.Schema, []string{rec.AgentID})
	var sessions map[string]string
	if rec.SessionID != "" {
		sessions = sessionTitleMap(info.Schema, []string{rec.SessionID})
	}
	var users map[string]string
	if rec.UserID != "" {
		users = userNameMap(info.Schema, []string{rec.UserID})
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"run": &agentHistoryDetail{
			agentHistoryItem: toAgentHistoryItem(rec, agents[rec.AgentID], sessions[rec.SessionID], users[rec.UserID]),
			Answer:           rec.Answer,
		},
	}, "")
}

// CleanupAgentHistory deletes run records in bulk by filter.
// DELETE /admin/running/agent-history?agent_id=&status=&run_type=&start_from=&start_to=
//
// At least one filter is required: this endpoint can delete every run record of a tenant, so accepting it
// without conditions amounts to "wipe the log in one click". The repository refuses the same thing; this
// rejects it earlier with a readable message.
func CleanupAgentHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	f, ok := agentHistoryQuery(c)
	if !ok {
		xhttp.ErrCode(c, "common.invalid_params", nil, "start_from / start_to must be RFC3339 timestamps; session_id must be a UUID", "")
		return
	}
	if f.Empty() {
		xhttp.ErrCode(c, "run.filter_required", nil, "provide at least one filter (agent / status / run type / time range); an unconditional wipe is not allowed", "")
		return
	}

	deleted, err := agentRunRepo.DeleteRuns(info.Schema, f)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": deleted}, "")
}

// DeleteAgentHistory deletes one run record.
// DELETE /admin/running/agent-history/:id
func DeleteAgentHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}

	deleted, err := agentRunRepo.DeleteRun(info.Schema, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if deleted == 0 {
		xhttp.ErrCode(c, "run.not_found", nil, "Run record not found", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": deleted}, "")
}

// ExportAgentHistory exports a CSV by filter.
// GET /admin/running/agent-history/export?<the same filters as the list>
//
// The cap is 5000 rows: an export is for a person to read in Excel, not a full archive. It fetches page by
// page so one SQL statement never loads the whole table into memory.
func ExportAgentHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	f, ok := agentHistoryQuery(c)
	if !ok {
		xhttp.ErrCode(c, "common.invalid_params", nil, "start_from / start_to must be RFC3339 timestamps; session_id must be a UUID", "")
		return
	}

	const (
		exportMax = 5000
		pageSize  = 500
	)
	records := make([]*models.AgentRunRecord, 0, pageSize)
	for offset := 0; offset < exportMax; {
		batch, total, err := agentRunRepo.ListRuns(info.Schema, f, pageSize, offset)
		if err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		records = append(records, batch...)
		offset += pageSize
		if len(batch) < pageSize || len(records) >= total || len(records) >= exportMax {
			break
		}
	}

	// Write the headers only after the data is fetched, so a mid-way failure still returns a normal error envelope
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition",
		fmt.Sprintf(`attachment; filename="agent-runs-%s.csv"`, time.Now().Format("20060102-150405")))
	// UTF-8 BOM: without it Excel garbles non-ASCII text
	if _, err := c.Writer.Write([]byte("\xEF\xBB\xBF")); err != nil {
		return
	}

	items := toAgentHistoryItems(info.Schema, records)

	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"run_id", "agent_id", "agent_name", "session_id", "session_title", "user_id", "user_name",
		"status", "run_type", "duration_ms", "started_at", "finished_at", "question", "answer_size", "answer_truncated", "err_msg"})
	for _, it := range items {
		finished := ""
		if it.FinishedAt != nil {
			finished = time.Time(*it.FinishedAt).Format("2006-01-02 15:04:05")
		}
		_ = w.Write([]string{
			it.RunID,
			it.AgentID,
			it.AgentName,
			it.SessionID,
			it.SessionTitle,
			it.UserID,
			it.UserName,
			it.Status,
			it.RunType,
			strconv.FormatInt(it.DurationMs, 10),
			time.Time(it.StartedAt).Format("2006-01-02 15:04:05"),
			finished,
			it.Question,
			strconv.Itoa(it.AnswerSize),
			strconv.FormatBool(it.AnswerTruncated),
			it.ErrMsg,
		})
	}
	w.Flush()
}
