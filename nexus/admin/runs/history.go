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

// 跨流程的运行历史（admin/running/history）。
//
// 与 flows.ListRuns 的区别只有一个：flow_id 可以不给。
// flowruns.ListRuns 本来就支持空 flowId 跨流程查，这个能力一直没暴露出来，
// 导致「今天全租户跑了多少、失败几条」只能在每个工作流的编辑器里逐个翻。
var historyRepo = flowruns.NewRepository()

// historyItem 历史列表的一行。
// 刻意不带 progress / run_flow_vars / run_args 这些大字段：列表页用不上，
// 带上会让一页 20 条变成几 MB，详情仍然走 /admin/flows/:id/runs/:runId。
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

// historyQuery 解析列表 / 导出 / 清理共用的筛选参数。
func historyQuery(c *gin.Context) (flowID, runType, status string, from, to *time.Time) {
	flowID = c.Query("flow_id")
	runType = c.Query("run_type")
	status = c.Query("status")
	// 时间范围走 RFC3339，与 flows.ListRuns 保持同一套约定
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

// flowNameMap 批量取工作流名。
//
// 跨流程列表只拿到 flow_id，直接甩 id 给界面等于让用户去别处对照。
// 名字取不到（工作流已删）就留空，不因为一条脏数据让整页失败。
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

// ListHistory 跨流程的历史运行列表
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
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"items":  toHistoryItems(info.Schema, records),
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}, "")
}

// CleanupHistory 按条件批量清理历史运行记录
// DELETE /admin/running/history?flow_id=&status=&start_from=&start_to=
//
// 必须至少给一个筛选条件：这个接口一次能删掉整个租户的运行记录，
// 无条件下发等于「一键清库」。repository 里也做了同样的拒绝，这里提前给出能看懂的话。
func CleanupHistory(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}

	flowID, _, status, from, to := historyQuery(c)
	if flowID == "" && status == "" && from == nil && to == nil {
		xhttp.ErrJSON(c, "至少要给一个筛选条件（工作流 / 状态 / 时间范围），不允许无条件清空", nil, "")
		return
	}

	deleted, err := historyRepo.DeleteRunsByFilter(info.Schema, flowID, status, from, to)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": deleted}, "")
}

// ExportHistory 按筛选条件导出 CSV
// GET /admin/running/history/export?<与列表相同的筛选参数>
//
// 上限 5000 行：导出是给人看、给 Excel 打开的，不是全量归档 —— 真要全量应当直接查库。
// 分页拉取，避免一条 SQL 把整张表读进内存。
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
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		records = append(records, batch...)
		offset += pageSize
		if len(batch) < pageSize || len(records) >= total || len(records) >= exportMax {
			break
		}
	}

	// 数据取完再写响应头：中途失败还能正常返回错误信封
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition",
		fmt.Sprintf(`attachment; filename="runs-%s.csv"`, time.Now().Format("20060102-150405")))
	// UTF-8 BOM：不加的话 Excel 打开中文列名是乱码
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
