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

// ListRunning lists the runs currently executing in this tenant (agent plus flow, other
// instances included).
//
// The data comes from the cross-instance ledger (Redis): the agent's runRegistry is in-process
// and a flow's flow_run_records is only written when it finishes, so "running" is invisible on
// both sides - hence this table.
//
// The query parameter kind=agent|flow filters (empty means all).
// complete=false means the ledger was not read fully (Redis disabled or erroring), so the list
// holds only this process's runs; instance is this instance's ID, and an entry whose instance
// matches it is "on this very machine".
func ListRunning(c *gin.Context) {
	ai, ok := authInfo(c)
	if !ok {
		return
	}

	kind := c.Query("kind")
	if kind != "" && kind != runledger.KindAgent && kind != runledger.KindFlow {
		xhttp.ErrCode(c, "run.invalid_kind", nil, "kind must be agent or flow", "")
		return
	}

	list, err := runledger.List(ai.Schema, kind)
	if err != nil {
		// A ledger read failure is not fatal: runledger already fell back to "what this process
		// is running", but the caller has to know the list may be incomplete rather than take
		// "half of it" as the truth.
		xlog.Error("failed to query the run ledger schema=%s: %v", ai.Schema, err)
	}
	fillUserNames(ai.Schema, list)
	xhttp.OkJSON(c, "Success", gin.H{
		"runs":     list,
		"instance": runledger.LocalInstanceID(),
		"complete": err == nil,
	}, "")
}

// fillUserNames adds the display name to ledger entries (the ledger only holds user_id).
//
// Why fill on the read side instead of carrying it when writing: the ledger is a Redis snapshot
// and does not follow a rename; making every write site (each agent / workflow entry) query the
// users table would also cost more.
// This does one IN query keyed by id; a miss (account deleted) is left empty and the frontend
// falls back to the id.
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

	// display_name first, the login name when empty (same rule as the user management page).
	//
	// The ledger's user_id is a string written elsewhere and may not be a valid uuid (debugging /
	// legacy data), so filter in Go first and pass uuids to the database: that keeps
	// id = ANY($1::uuid[]) on the users primary key index. Writing id::text = ANY($1::text[])
	// would wrap the column in a function, kill the index and degrade to a full table scan.
	//
	// pq.Array cannot be skipped: the driver is lib/pq and passing a plain slice reports
	// unsupported type, which makes this query fail every time and leaves every name empty (the
	// frontend then falls back to the id, so it looks as if the feature was never built).
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
		xlog.Error("failed to query run ledger user names schema=%s: %v", schema, err)
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

// stopRequest stops one run. run_id is required; session_id is the session-level fallback for agents.
type stopRequest struct {
	Kind      string `json:"kind"` // agent | flow, defaults to agent
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
}

// StopRun stops a running execution (agent or flow).
//
// Order: try this process first, then broadcast the intent to every instance - a run only lives
// in the memory of the machine executing it, while this HTTP request may be load-balanced to any
// instance. reason describes the outcome:
//
//	stopped     this instance stopped it
//	broadcast   this instance missed (the run is elsewhere); broadcast sent, that instance will act
//	unreachable this instance missed and broadcast is unavailable (Redis disabled / down)
func StopRun(c *gin.Context) {
	ai, ok := authInfo(c)
	if !ok {
		return
	}

	var req stopRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "malformed request: "+err.Error(), "")
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = runledger.KindAgent
	}
	if kind != runledger.KindAgent && kind != runledger.KindFlow {
		xhttp.ErrCode(c, "run.invalid_kind", nil, "kind must be agent or flow", "")
		return
	}
	if req.RunID == "" && req.SessionID == "" {
		xhttp.ErrCode(c, "run.target_required", nil, "provide at least one of run_id and session_id", "")
		return
	}
	if kind == runledger.KindFlow && req.RunID == "" {
		// Flows have no "stop by session" semantics (one session may run several flows at once)
		xhttp.ErrCode(c, "run.flow_stop_needs_run_id", nil, "a flow can only be stopped by run_id", "")
		return
	}

	// Check the ledger first: which machine runs this, and whether it is this one (which also
	// tells whether it has already finished)
	owner, local, known, lerr := ownerOf(ai.Schema, req.RunID)
	if lerr != nil {
		xlog.Error("failed to query the run ledger schema=%s: %v", ai.Schema, lerr)
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

	// A miss on this instance almost always means the run is on another one: broadcast to every
	// instance and whoever holds it acts. It is sent even after a hit - the same session may have
	// child runs on other instances, and cancellation is idempotent.
	broadcast, berr := runledger.BroadcastStop(runledger.StopSignal{
		Schema:    ai.Schema,
		Kind:      kind,
		RunID:     req.RunID,
		SessionID: req.SessionID,
	})
	if berr != nil {
		xlog.Error("failed to broadcast the stop signal schema=%s run=%s: %v", ai.Schema, req.RunID, berr)
	}

	reason := "unreachable"
	switch {
	case stopped > 0:
		reason = "stopped"
	case broadcast:
		reason = "broadcast"
	}

	// The field is named broadcast, not published: in this project publish only means listing an
	// asset, and the vocabulary must not be mixed
	xhttp.OkJSON(c, "Success", gin.H{
		"stopped":   stopped,
		"reason":    reason,
		"instance":  owner,
		"local":     local,
		"known":     known,
		"broadcast": broadcast,
	}, "")
}

// ownerOf looks up which instance a run is on in the ledger.
// known=false means the ledger does not have it: it may have finished, or the ledger is
// unavailable (see err).
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

// authInfo reads the signed-in tenant (injected by the admin middleware).
func authInfo(c *gin.Context) (*auth.AuthInfo, bool) {
	value, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	return value.(*auth.AuthInfo), true
}
