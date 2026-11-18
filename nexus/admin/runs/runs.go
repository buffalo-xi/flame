package runs

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"flame/nexus/service/agent"
	"flame/nexus/service/flow/executors"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/runledger"
	"flame/tools/xhttp"
	"flame/tools/xlog"
)

// ListRunning 列出本租户当前在跑的运行（agent + flow，含其它实例）。
//
// 数据来自跨实例台账（Redis）：agent 的 runRegistry 是进程内的、flow 的
// flow_run_records 只在跑完才落库，两边「运行中」都不可见，所以才有这张表。
//
// 查询参数 kind=agent|flow 可过滤（留空 = 全部）。
// complete=false 表示台账没读全（Redis 未启用或报错），此时列表只剩本进程的那些；
// instance 是本实例标识，与条目里的 instance 相同即代表「就在这台机器上」。
func ListRunning(c *gin.Context) {
	ai, ok := authInfo(c)
	if !ok {
		return
	}

	kind := c.Query("kind")
	if kind != "" && kind != runledger.KindAgent && kind != runledger.KindFlow {
		xhttp.ErrJSON(c, "kind 只能是 agent 或 flow", nil, "")
		return
	}

	list, err := runledger.List(ai.Schema, kind)
	if err != nil {
		// 台账读失败不致命：runledger 已退回「本进程在跑的」，但要让调用方
		// 知道这份列表可能不全，而不是把「少了一半」当成事实。
		xlog.Error("查询运行台账失败 schema=%s: %v", ai.Schema, err)
	}
	fillUserNames(ai.Schema, list)
	xhttp.OkJSON(c, "Success", gin.H{
		"runs":     list,
		"instance": runledger.LocalInstanceID(),
		"complete": err == nil,
	}, "")
}

// fillUserNames 给台账条目补上用户展示名（台账里只有 user_id）。
//
// 为什么在读侧补、而不是写台账时带上：台账是 Redis 快照，用户改名后它不会跟着变；
// 让每个写入点（agent / workflow 的各入口）都去查用户表，代价也更高。
// 这里一次 IN 查询按 id 批量取，查不到（账号已删）就留空，前端回退显示 id。
func fillUserNames(schema string, list []runledger.Snapshot) {
	if len(list) == 0 {
		return
	}
	ids := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, r := range list {
		if r.UserID == "" {
			continue
		}
		if _, ok := seen[r.UserID]; ok {
			continue
		}
		seen[r.UserID] = struct{}{}
		ids = append(ids, r.UserID)
	}
	if len(ids) == 0 {
		return
	}

	// 优先 display_name，没填就用登录名（与「用户管理」页的展示口径一致）。
	//
	// 台账里的 user_id 是别处写进来的字符串，可能是非法 uuid（调试 / 旧数据），
	// 所以先在 Go 里过滤，再按 uuid 传给库：这样能用 id = ANY($1::uuid[]) 走 users 主键索引。
	// 若写成 id::text = ANY($1::text[])，列上套了函数，索引就废了，退化成全表扫描。
	//
	// pq.Array 不能省：驱动是 lib/pq，直接传切片会报 unsupported type，
	// 结果是这条查询每次都失败、名字全空（前端自动回退成 id，看起来就像"没做"）。
	uids := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if u, err := uuid.Parse(id); err == nil {
			uids = append(uids, u)
		}
	}
	if len(uids) == 0 {
		return
	}

	rows, err := db.DB.Query(`SELECT id::text, COALESCE(NULLIF(display_name, ''), name)
		FROM `+schema+`.users WHERE id = ANY($1::uuid[])`, pq.Array(uids))
	if err != nil {
		xlog.Error("查询运行台账用户名失败 schema=%s: %v", schema, err)
		return
	}
	defer rows.Close()

	names := make(map[string]string, len(ids))
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		names[id] = name
	}
	for i := range list {
		list[i].UserName = names[list[i].UserID]
	}
}

// stopRequest 终止一条运行。run_id 必填；session_id 用于 agent 的会话级兜底。
type stopRequest struct {
	Kind      string `json:"kind"` // agent | flow，缺省 agent
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
}

// StopRun 终止一条正在跑的运行（agent 或 flow）。
//
// 顺序：先在本进程试，再把意图广播给所有实例 —— 因为运行只存在于跑它的那台机器的
// 内存里，而这条 HTTP 请求可能被负载均衡分到任意实例上。reason 说明命中情况：
//
//	stopped     本实例停到了
//	broadcast   本实例没命中（运行在别的实例上），已广播，由那边的实例执行
//	unreachable 本实例没命中，且广播不可用（Redis 未启用/不可用）
func StopRun(c *gin.Context) {
	ai, ok := authInfo(c)
	if !ok {
		return
	}

	var req stopRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "请求格式错误: "+err.Error(), nil, "")
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = runledger.KindAgent
	}
	if kind != runledger.KindAgent && kind != runledger.KindFlow {
		xhttp.ErrJSON(c, "kind 只能是 agent 或 flow", nil, "")
		return
	}
	if req.RunID == "" && req.SessionID == "" {
		xhttp.ErrJSON(c, "run_id 或 session_id 至少给一个", nil, "")
		return
	}
	if kind == runledger.KindFlow && req.RunID == "" {
		// 工作流没有「按会话停」的语义（一个会话可能同时跑多条流程）
		xhttp.ErrJSON(c, "工作流只能按 run_id 终止", nil, "")
		return
	}

	// 先看台账：这条跑在哪台、是不是本机（顺便判断是不是已经结束了）
	owner, local, known, lerr := ownerOf(ai.Schema, req.RunID)
	if lerr != nil {
		xlog.Error("查询运行台账失败 schema=%s: %v", ai.Schema, lerr)
	}

	stopped := 0
	if kind == runledger.KindAgent {
		if req.RunID != "" {
			if agent.StopRunByID(req.RunID) {
				stopped = 1
			}
		} else {
			stopped = agent.StopSession(req.SessionID)
		}
	} else if executors.StopRun(req.RunID) {
		stopped = 1
	}

	// 本实例没命中时，几乎一定是运行在别的实例上：广播给所有实例，谁持有谁执行。
	// 命中过也照发一次 —— 同一会话可能还有别的实例上的子运行，取消本身是幂等的。
	broadcast, berr := runledger.BroadcastStop(runledger.StopSignal{
		Schema:    ai.Schema,
		Kind:      kind,
		RunID:     req.RunID,
		SessionID: req.SessionID,
	})
	if berr != nil {
		xlog.Error("广播停止信号失败 schema=%s run=%s: %v", ai.Schema, req.RunID, berr)
	}

	reason := "unreachable"
	switch {
	case stopped > 0:
		reason = "stopped"
	case broadcast:
		reason = "broadcast"
	}

	// 字段名用 broadcast 而不是 published：项目里 publish 专指资产的「上架」，别混词
	xhttp.OkJSON(c, "Success", gin.H{
		"stopped":   stopped,
		"reason":    reason,
		"instance":  owner,
		"local":     local,
		"known":     known,
		"broadcast": broadcast,
	}, "")
}

// ownerOf 从台账里查这条运行落在哪台实例上。
// known=false 表示台账里没有它：可能已结束，也可能台账不可用（看 err）。
func ownerOf(schema, runID string) (instance string, local, known bool, err error) {
	if runID == "" {
		return "", false, false, nil
	}
	list, lerr := runledger.List(schema, "")
	if lerr != nil {
		return "", false, false, lerr
	}
	for _, run := range list {
		if run.RunID != runID {
			continue
		}
		return run.Instance, run.Local, true, nil
	}
	return "", false, false, nil
}

// authInfo 取当前登录租户（管理端中间件已注入）。
func authInfo(c *gin.Context) (*auth.AuthInfo, bool) {
	value, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return nil, false
	}
	return value.(*auth.AuthInfo), true
}
