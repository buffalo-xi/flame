package runs

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	flowruns "flame/nexus/shared/repository/flowruns"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// Cross-flow run history (admin/running/history).
//
// The only difference from flows.ListRuns is that flow_id is optional.
// flowruns.ListRuns already supports an empty flowId to query across flows, but that ability was
// never exposed, so "how many runs did the tenant have today, how many failed" could only be
// answered by paging through each workflow's editor.
var historyRepo = flowruns.NewRepository()

// historyItem is one row of the history list.
// It deliberately omits the large fields (progress / run_flow_vars / run_args): the list page does
// not use them, and carrying them would turn 20 rows into several MB. Details still come from
// /admin/flows/:id/runs/:runId.
type historyItem struct {
	ID         string     `json:"id"`
	FlowID     string     `json:"flow_id"`
	FlowName   string     `json:"flow_name"`
	Status     string     `json:"status"`
	RunType    string     `json:"run_type"`
	ErrMsg     string     `json:"err_msg"`
	NodeCount  int        `json:"node_count"`
	DurationMs int64      `json:"duration_ms"`
	StartedAt  xsql.Time  `json:"started_at"`
	FinishedAt *xsql.Time `json:"finished_at"`
}

// historyQuery parses the filters shared by the list / export / cleanup endpoints.
func historyQuery(c *gin.Context) (flowID, runType, status string, from, to *time.Time) {
	flowID = c.Query("flow_id")
	runType = c.Query("run_type")
	status = c.Query("status")
	// The time range is RFC3339, the same convention as flows.ListRuns
	if v := c.Query("start_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = &t
		}
	}
	if v := c.Query("start_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = &t
		}
	}
	return
}

// flowNameMap fetches workflow names in bulk.
//
// A cross-flow list only has flow_id, and handing raw ids to the UI forces the user to look them
// up elsewhere. A missing name (the workflow was deleted) is left empty, so one dirty row does
// not fail the whole page.
func flowNameMap(schema string, ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	rows, err := db.DB.Query(
		`SELECT id::text, name FROM `+schema+`.flows WHERE id::text = ANY($1::text[])`, pq.Array(ids))
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

func toHistoryItems(schema string, records []*models.FlowRunRecord) []*historyItem {
	ids := make([]string, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.FlowID)
	}
	names := flowNameMap(schema, ids)

	items := make([]*historyItem, 0, len(records))
	for _, r := range records {
		items = append(items, &historyItem{
			ID:         r.ID,
			FlowID:     r.FlowID,
			FlowName:   names[r.FlowID],
			Status:     r.Status,
			RunType:    r.RunType,
			ErrMsg:     r.ErrMsg,
			NodeCount:  r.NodeCount,
			DurationMs: r.DurationMs,
			StartedAt:  r.StartedAt,
			FinishedAt: r.FinishedAt,
		})
	}
	return items
}

// ListHistory lists historical runs across flows
// GET /admin/running/history?flow_id=&status=&run_type=&start_from=&start_to=&limit=&offset=
func ListHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	flowID, runType, status, from, to := historyQuery(c)
	records, total, err := historyRepo.ListRuns(info.Schema, flowID, runType, status, from, to, limit, offset)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"items":  toHistoryItems(info.Schema, records),
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}, "")
}

// CleanupHistory deletes historical run records in bulk by filter
// DELETE /admin/running/history?flow_id=&status=&start_from=&start_to=
//
// At least one filter is required: this endpoint can delete every run record of a tenant, so
// accepting it without conditions amounts to "wipe the database in one click". The repository
// refuses the same thing; this rejects it earlier with a readable message.
func CleanupHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}

	flowID, _, status, from, to := historyQuery(c)
	if flowID == "" && status == "" && from == nil && to == nil {
		xhttp.ErrCode(c, "run.filter_required", nil, "provide at least one filter (workflow / status / time range); an unconditional wipe is not allowed", "")
		return
	}

	deleted, err := historyRepo.DeleteRunsByFilter(info.Schema, flowID, status, from, to)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": deleted}, "")
}

// ExportHistory exports a CSV by filter
// GET /admin/running/history/export?<the same filters as the list>
//
// The cap is 5000 rows: an export is for a person to read in Excel, not a full archive - a real
// archive should query the database directly. It fetches page by page so one SQL statement never
// loads the whole table into memory.
func ExportHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}

	flowID, runType, status, from, to := historyQuery(c)

	const (
		exportMax = 5000
		pageSize  = 500
	)
	records := make([]*models.FlowRunRecord, 0, pageSize)
	for offset := 0; offset < exportMax; {
		batch, total, err := historyRepo.ListRuns(info.Schema, flowID, runType, status, from, to, pageSize, offset)
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
		fmt.Sprintf(`attachment; filename="runs-%s.csv"`, time.Now().Format("20060102-150405")))
	// UTF-8 BOM: without it Excel garbles non-ASCII text
	if _, err := c.Writer.Write([]byte("\xEF\xBB\xBF")); err != nil {
		return
	}

	names := flowNameMap(info.Schema, func() []string {
		ids := make([]string, 0, len(records))
		for _, r := range records {
			ids = append(ids, r.FlowID)
		}
		return ids
	}())

	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"run_id", "flow_id", "flow_name", "status", "run_type",
		"node_count", "duration_ms", "started_at", "finished_at", "err_msg"})
	for _, r := range records {
		finished := ""
		if r.FinishedAt != nil {
			finished = time.Time(*r.FinishedAt).Format("2006-01-02 15:04:05")
		}
		_ = w.Write([]string{
			r.ID,
			r.FlowID,
			names[r.FlowID],
			r.Status,
			r.RunType,
			strconv.Itoa(r.NodeCount),
			strconv.FormatInt(r.DurationMs, 10),
			time.Time(r.StartedAt).Format("2006-01-02 15:04:05"),
			finished,
			r.ErrMsg,
		})
	}
	w.Flush()
}
