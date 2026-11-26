package flows

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/flowcheck"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/flowdoc"
	"flame/nexus/shared/models"
	"flame/nexus/shared/quota"
	agentsrepo "flame/nexus/shared/repository/agents"
	"flame/nexus/shared/repository/flows"
	"flame/nexus/shared/repository/sessions"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/secureval"
	"flame/nexus/shared/versioning"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// Package-level repository instance.
var repo = repository.NewRepository()

// rawOf returns the raw bytes of a JSONB value (xsql.JSONB.Data may be []byte / RawMessage / string).
func rawOf(j xsql.JSONB) []byte {
	switch v := j.Data.(type) {
	case nil:
		return nil
	case []byte:
		return v
	case json.RawMessage:
		return []byte(v)
	case string:
		return []byte(v)
	default:
		b, _ := json.Marshal(j.Data)
		return b
	}
}

// maskedEnvVars is for responses: a type=secret value never falls back to plaintext, only has_value remains.
//
// If it cannot be masked, fail: never fall back to plaintext, which would mean the feature does not exist.
func maskedEnvVars(j xsql.JSONB) (xsql.JSONB, error) {
	masked, err := secureval.MaskEnvVars(rawOf(j))
	if err != nil {
		return xsql.JSONB{}, err
	}
	if len(masked) == 0 {
		return j, nil
	}
	return xsql.JSONB{Data: json.RawMessage(masked)}, nil
}

// validateFlowNameUnique checks the flow name: same character set as agent / skill (letters, digits, hyphens, max 64),
// and it shares the namespace with agents, so the two must not clash.
//
// The character set is not arbitrary: the flow name becomes the endpoint name (svc_endpoints.Create enforces equality),
// that is the MCP tool.name, the agent-side tool name local__<name> and the grant ref local:rpc:<name>.
// The last two are ASCII-only, so a non-ASCII name can be created but never exposed or granted - put non-ASCII text in the description.
func validateFlowNameUnique(info *auth.AuthInfo, name string) error {
	name = strings.TrimSpace(name)
	if !tools.ValidName(name) {
		return fmt.Errorf("invalid flow name %q: letters, digits and hyphens only (not starting with a hyphen, max 64); "+
			"the name becomes the external tool name local__<name>, so put a translated note in the description", name)
	}
	exist, err := agentsrepo.NewRepository().ExistsName(info, name, "")
	if err != nil {
		return err
	}
	if exist {
		return fmt.Errorf("flow name %q is already taken by an agent in this tenant: flows and agents share one namespace "+
			"(the endpoint name always equals the entity name, so a clash makes endpoint creation fail); pick another name", name)
	}
	return nil
}

// SetFlowStatus switches the flow state (1=active | 3=deprecated, see models.StatusXxx; there is no locked state, production is managed by releases).
//
// Switching to deprecated retracts every endpoint of the flow (exposed=false, the records stay):
// a deprecated flow must not serve outside. Switching back to active leaves the endpoints alone (exposure is independent of the definition state).
func SetFlowStatus(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		Status int `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	switch body.Status {
	case models.StatusNormal, models.StatusDeprecated:
	default:
		xhttp.ErrCode(c, "flow.invalid_status", nil, "status must be 1 (active) or 3 (deprecated); flows have no locked state, production is managed by releases", "")
		return
	}

	if err := repo.UpdateStatus(ai, id, body.Status); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "flow.not_found", nil, "flow instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Only switching to deprecated retracts the endpoints: a deprecated flow must not serve outside.
	// Switching to active does not retract: exposure and the definition lifecycle are separate, and restoring edits must not take production down.
	// The state is already changed, so a failed retract is logged instead of rolled back (the next switch or a manual retract recovers).
	if body.Status == models.StatusDeprecated {
		if uerr := svcendpoints.NewRepository().RetractByTarget(ai, "flow", id); uerr != nil {
			xlog.Error("failed to retract the flow endpoints id=%s: %v", id, uerr)
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id, "status": body.Status}, "")
}

// ListFlows lists flow instances.
func ListFlows(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// Decode the request parameters.
	var req repository.ListFlowsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}

	// Query the data.
	models, total, err := repo.ListFlows(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Current version numbers in one batch query (the cards show vN); a read failure does not affect the list itself.
	// Only CurrentVersion is filled here; Dirty needs a per-row definition comparison, so the detail handler owns it (fillVersionState).
	curVers, _ := versioning.CurrentVersions(authInfo.(*auth.AuthInfo).Schema, "flow_versions")
	for i := range models {
		models[i].CurrentVersion = curVers[models[i].ID]
	}

	// Build the response.
	response := gin.H{
		"items": models,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// validateDiagramLabels rejects a diagram whose node labels are not unique.
//
// label is the writable form of config.inputs.*.source_ref / call.target / wait.fork_node_id
// (tools and the executor both turn a label into a node id), so a duplicate makes those
// references point at the wrong node. The check sits at the save point, so no amount of
// canvas editing can get past it.
//
// An empty code means the diagram is fine; otherwise the caller turns the triple into an
// error response (code + params for the locale bundle, msg as the operator diagnostic).
func validateDiagramLabels(diagram json.RawMessage) (string, gin.H, string) {
	doc, ok := flowdoc.Parse(diagram)
	if !ok {
		return "", nil, "" // draft shapes are reported at run time
	}
	dups := doc.DuplicateLabels()
	if len(dups) == 0 {
		return "", nil, ""
	}
	names := strings.Join(dups, "; ")
	return "flow.duplicate_label", gin.H{"names": names}, "duplicate node labels: " + names
}

// validateDiagramNodeCount rejects a diagram with more nodes than the tenant quota allows.
//
// The node count is a static property of the definition: a run reads a fixed diagram, so a runtime check could never
// fire. The write boundary is the only place it can be enforced — flow save, asset install and the editor all go
// through here. Canvas comments are editor annotations that never reach the executor, so they take no part.
func validateDiagramNodeCount(limits flowconfig.FlowLimits, diagram json.RawMessage) (string, gin.H, string) {
	doc, ok := flowdoc.Parse(diagram)
	if !ok {
		return "", nil, ""
	}
	count := doc.CountNodes(flowdoc.CommentNodeType)
	if limits.MaxNodeCount <= 0 || count <= limits.MaxNodeCount {
		return "", nil, ""
	}
	return "flow.node_limit_exceeded",
		gin.H{"count": count, "limit": limits.MaxNodeCount},
		fmt.Sprintf("node count %d exceeds the tenant limit %d", count, limits.MaxNodeCount)
}

// flowFindings runs the shared static checks over a submitted diagram.
//
// An unparsable diagram is itself a finding rather than a failed request: the editor gets one response shape (counts +
// findings) whatever it sends, and "the body is not even a diagram" ends up in the same list as everything else.
func flowFindings(diagram json.RawMessage) []flowcheck.Finding {
	// langs is nil: the sandbox's workflow language list belongs to the agent side, and the editor only offers languages
	// from its own list - guessing here would report every lang when the snapshot is missing.
	findings, err := flowcheck.Diagram(diagram, nil)
	if err != nil {
		return []flowcheck.Finding{{
			Severity: flowcheck.SeverityError,
			Code:     flowcheck.CodeShape,
			Message:  err.Error(),
			Hint:     "the editor sends diagram as a JSON string; check the nodes / edges arrays",
		}}
	}
	if findings == nil {
		return []flowcheck.Finding{}
	}
	return findings
}

// ValidateFlow statically checks a diagram without saving it.
//
// The editor calls this before PUT /flows/:id so that the canvas and the agent's validate_flow tool report the same
// findings: shared/flowcheck is the single rule set, and a second opinion in the front end is how "the check passed but
// the run failed" becomes normal. Nothing is stored and nothing runs; severity is the contract - an error means the
// definition cannot run as written, a warning means it runs but probably not as intended.
func ValidateFlow(c *gin.Context) {
	var req struct {
		Diagram json.RawMessage `json:"diagram"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	if len(bytes.TrimSpace(req.Diagram)) == 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "diagram must not be empty", "")
		return
	}
	findings := flowFindings(req.Diagram)
	errs, warns := flowcheck.Summarize(findings)
	xhttp.OkJSON(c, "Success", gin.H{
		"ok":            errs == 0,
		"error_count":   errs,
		"warning_count": warns,
		"findings":      findings,
	}, "")
}

// GetFlow returns one flow instance.
func GetFlow(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	s, err := repo.GetFlow(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "flow.not_found", nil, "flow instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Mask the response: a type=secret value in env_vars never goes out in plaintext.
	// This only hides what is seen in passing (screenshots / devtools / a screen recording), not from someone
	// who can edit the flow - accidental leaks are far more common than deliberate ones.
	if s != nil {
		masked, merr := maskedEnvVars(s.EnvVars)
		if merr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to mask the environment variables: "+merr.Error(), "")
			return
		}
		s.EnvVars = masked
	}
	xhttp.OkJSON(c, "Success", s, "")
}

// CreateFlow creates a flow instance.
func CreateFlow(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// Quota check: the per-tenant flow cap.
	schema := authInfo.(*auth.AuthInfo).Schema
	cnt, err := quota.CountFlows(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if qerr := models.CheckQuota("flow", cnt, quota.Resolve(schema).MaxFlows); qerr != nil {
		// A full quota is not an internal error: report it as quota.flow_limit_reached so the UI can word it.
		le := qerr.(*models.LimitError)
		xhttp.ErrCode(c, "quota.flow_limit_reached", le.Params(), qerr.Error(), "")
		return
	}

	// Define the request payload.
	var req struct {
		Name              string          `json:"name"`
		Diagram           json.RawMessage `json:"diagram"`
		Description       string          `json:"description"`
		Arguments         json.RawMessage `json:"arguments"`
		Outputs           json.RawMessage `json:"outputs"`
		EnvVars           json.RawMessage `json:"env_vars"`
		FlowVars          json.RawMessage `json:"flow_vars"`
		Timeout           int             `json:"timeout"`
		MaxExecCount      int             `json:"max_exec_count"`
		StoreRuns         *bool           `json:"store_runs"`
		SessionMemory     *bool           `json:"session_memory"`
		Breakpoints       []string        `json:"breakpoints"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Apply the flow config defaults and check them against the limits.
	if err := validateFlowLimits(authInfo.(*auth.AuthInfo), &req.Timeout, &req.MaxExecCount); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// A missing flag means "on": older clients never sent them, and defaulting to off would silently stop writing run
	// records and session memory.
	storeRuns := req.StoreRuns == nil || *req.StoreRuns
	sessionMemory := req.SessionMemory == nil || *req.SessionMemory

	// Check the flow description.
	if err := validateDescription(req.Description); err != nil {
		xhttp.ErrCode(c, "flow.description_required", nil, err.Error(), "")
		return
	}

	// Convert to the Instance struct.
	Instance := models.Flow{
		Name:              req.Name,
		Diagram:           xsql.JSONB{Data: req.Diagram},
		Description:       xsql.String(req.Description),
		Arguments:         xsql.JSONB{Data: req.Arguments},
		Outputs:           xsql.JSONB{Data: req.Outputs},
		EnvVars:           xsql.JSONB{Data: req.EnvVars},
		FlowVars:          xsql.JSONB{Data: req.FlowVars},
		Timeout:           xsql.Int(req.Timeout),
		MaxExecCount:      xsql.Int(req.MaxExecCount),
		StoreRuns:         storeRuns,
		SessionMemory:     sessionMemory,
		Breakpoints:       req.Breakpoints,
	}

	// Name check: valid (letters, digits, hyphens) and no clash with an agent, before anything is written.
	req.Name = strings.TrimSpace(req.Name)
	if err := validateFlowNameUnique(authInfo.(*auth.AuthInfo), req.Name); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Node labels must be unique: a duplicate makes source_ref / call.target / fork_node_id point at the wrong node.
	if code, params, msg := validateDiagramLabels(req.Diagram); code != "" {
		xhttp.ErrCode(c, code, params, msg, "")
		return
	}

	// The node count is a static property of the definition, so it is checked here rather than at run time. The quota
	// row is read through the same Redis-backed cache as the limits check above.
	if code, params, msg := validateDiagramNodeCount(flowconfig.Resolve(authInfo.(*auth.AuthInfo).Schema), req.Diagram); code != "" {
		xhttp.ErrCode(c, code, params, msg, "")
		return
	}

	// Insert the row.
	id, err := repo.CreateFlow(authInfo.(*auth.AuthInfo), &Instance)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Mask the response: a secret never goes to the front end (the plaintext above is what was stored; only the body changes).
	maskedEnv, merr := maskedEnvVars(Instance.EnvVars)
	if merr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to mask the environment variables: "+merr.Error(), "")
		return
	}
	Instance.EnvVars = maskedEnv

	// Return the created flow instance.
	data := gin.H{
		"id":                  id,
		"created_by":          authInfo.(*auth.AuthInfo).ID,
		"updated_by":          authInfo.(*auth.AuthInfo).ID,
		"created_by_name":     authInfo.(*auth.AuthInfo).Name,
		"updated_by_name":     authInfo.(*auth.AuthInfo).Name,
		"name":                Instance.Name,
		"diagram":             Instance.Diagram,
		"description":         Instance.Description,
		"arguments":           Instance.Arguments,
		"outputs":             Instance.Outputs,
		"env_vars":            Instance.EnvVars,
		"flow_vars":           Instance.FlowVars,
		"timeout":             Instance.Timeout,
		"max_exec_count":      Instance.MaxExecCount,
		"store_runs":          Instance.StoreRuns,
		"session_memory":      Instance.SessionMemory,
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateFlow updates a flow instance.
func UpdateFlow(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	// Define the request payload.
	var req struct {
		Name              string          `json:"name"`
		Diagram           json.RawMessage `json:"diagram"`
		Description       string          `json:"description"`
		Arguments         json.RawMessage `json:"arguments"`
		Outputs           json.RawMessage `json:"outputs"`
		EnvVars           json.RawMessage `json:"env_vars"`
		FlowVars          json.RawMessage `json:"flow_vars"`
		Timeout           int             `json:"timeout"`
		MaxExecCount      int             `json:"max_exec_count"`
		StoreRuns         *bool           `json:"store_runs"`
		SessionMemory     *bool           `json:"session_memory"`
		Breakpoints       []string        `json:"breakpoints"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Apply the flow config defaults and check them against the limits.
	if err := validateFlowLimits(authInfo.(*auth.AuthInfo), &req.Timeout, &req.MaxExecCount); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// A missing flag means "on": older clients never sent them, and defaulting to off would silently stop writing run
	// records and session memory.
	storeRuns := req.StoreRuns == nil || *req.StoreRuns
	sessionMemory := req.SessionMemory == nil || *req.SessionMemory

	// Check the flow description.
	if err := validateDescription(req.Description); err != nil {
		xhttp.ErrCode(c, "flow.description_required", nil, err.Error(), "")
		return
	}

	// Convert to the Instance struct.
	Instance := models.Flow{
		Name:              req.Name,
		Diagram:           xsql.JSONB{Data: req.Diagram},
		Description:       xsql.String(req.Description),
		Arguments:         xsql.JSONB{Data: req.Arguments},
		Outputs:           xsql.JSONB{Data: req.Outputs},
		EnvVars:           xsql.JSONB{Data: req.EnvVars},
		FlowVars:          xsql.JSONB{Data: req.FlowVars},
		Timeout:           xsql.Int(req.Timeout),
		MaxExecCount:      xsql.Int(req.MaxExecCount),
		StoreRuns:         storeRuns,
		SessionMemory:     sessionMemory,
		Breakpoints:       req.Breakpoints,
	}

	// Name handling: keep the old one when absent; when it changes, validate (shape + no agent clash) before writing.
	req.Name = strings.TrimSpace(req.Name)
	old, oldErr := repo.GetFlow(authInfo.(*auth.AuthInfo), id)
	if oldErr == nil && old != nil {
		if req.Name == "" {
			req.Name = old.Name
		} else if old.Name != req.Name {
			if err := validateFlowNameUnique(authInfo.(*auth.AuthInfo), req.Name); err != nil {
				xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
				return
			}
		}
	} else if req.Name != "" {
		// Validate even when the old row cannot be read (concurrent delete and so on): the name ends up in
		// flows.name and in the endpoint name, so one failed read must not bypass the character-set and clash checks.
		if err := validateFlowNameUnique(authInfo.(*auth.AuthInfo), req.Name); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
	}
	// The name above may have been filled from the old row or come from an untrimmed input, so store the
	// resolved req.Name: otherwise the stored name keeps its spaces while the endpoint name is migrated trimmed.
	Instance.Name = req.Name

	// Node labels must be unique: a duplicate makes source_ref / call.target / fork_node_id point at the wrong node.
	if code, params, msg := validateDiagramLabels(req.Diagram); code != "" {
		xhttp.ErrCode(c, code, params, msg, "")
		return
	}

	// The node count is a static property of the definition, so it is checked here rather than at run time.
	if code, params, msg := validateDiagramNodeCount(flowconfig.Resolve(authInfo.(*auth.AuthInfo).Schema), req.Diagram); code != "" {
		xhttp.ErrCode(c, code, params, msg, "")
		return
	}

	// An empty secret means "keep the old value": the plaintext never goes out, so the front end submits an empty one.
	// The old value has to be restored here, or one save that only changed the description would wipe the key.
	if len(req.EnvVars) > 0 && oldErr == nil && old != nil {
		merged, merr := secureval.MergeEnvVarSecrets(rawOf(old.EnvVars), req.EnvVars)
		if merr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to merge the environment variables: "+merr.Error(), "")
			return
		}
		Instance.EnvVars = xsql.JSONB{Data: json.RawMessage(merged)}
	}

	// Update the row.
	err := repo.UpdateFlow(authInfo.(*auth.AuthInfo), id, &Instance)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "flow.not_found", nil, "flow instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Mask the response: a secret never goes to the front end (the merged plaintext above is what was stored).
	maskedEnv, merr := maskedEnvVars(Instance.EnvVars)
	if merr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to mask the environment variables: "+merr.Error(), "")
		return
	}
	Instance.EnvVars = maskedEnv

	data := gin.H{
		"id":                  id,
		"created_by":          authInfo.(*auth.AuthInfo).ID,
		"updated_by":          authInfo.(*auth.AuthInfo).ID,
		"created_by_name":     authInfo.(*auth.AuthInfo).Name,
		"updated_by_name":     authInfo.(*auth.AuthInfo).Name,
		"name":                Instance.Name,
		"diagram":             Instance.Diagram,
		"description":         Instance.Description,
		"arguments":           Instance.Arguments,
		"outputs":             Instance.Outputs,
		"env_vars":            Instance.EnvVars,
		"flow_vars":           Instance.FlowVars,
		"timeout":             Instance.Timeout,
		"max_exec_count":      Instance.MaxExecCount,
		"store_runs":          Instance.StoreRuns,
		"session_memory":      Instance.SessionMemory,
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// DeleteFlow deletes a flow instance.
func DeleteFlow(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	// Delete every protocol endpoint attached to this flow first (target_type=flow).
	if err := svcendpoints.NewRepository().DeleteByTarget(authInfo.(*auth.AuthInfo), "flow", id); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete endpoints: "+err.Error(), "")
		return
	}

	// Delete the row.
	err := repo.DeleteFlow(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "flow.not_found", nil, "flow instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// The flow's sessions go with it: once the owner is gone the console can no longer list (or delete) them, yet they keep
	// counting against the user's session quota and keep their workspaces on disk. Best-effort: the row is already gone, so
	// a failure is logged rather than rolled back.
	ai := authInfo.(*auth.AuthInfo)
	if n, derr := sessions.NewRepository().DeleteByOwner(ai.Schema, models.SessionOwnerTypeFlow, id); derr != nil {
		xlog.Error("failed to delete the sessions of the deleted flow schema=%s flow=%s: %v", ai.Schema, id, derr)
	} else if n > 0 {
		xlog.Info("deleted %d session(s) together with flow schema=%s flow=%s", n, ai.Schema, id)
	}

	// The run history goes with it. flow_run_records / flow_node_records carry no foreign key (unlike agent_run_records,
	// which cascades from agents), so without this the rows would survive forever: unreachable from the flow's own pages,
	// yet still listed by the cross-flow history with a dangling flow_id. Best-effort for the same reason as the sessions
	// above: the row is already gone, so a failure is logged instead of rolled back.
	if n, derr := runRepo.DeleteRuns(ai.Schema, id); derr != nil {
		xlog.Error("failed to delete the run records of the deleted flow schema=%s flow=%s: %v", ai.Schema, id, derr)
	} else if n > 0 {
		xlog.Info("deleted %d run record(s) together with flow schema=%s flow=%s", n, ai.Schema, id)
	}

	xhttp.OkJSON(c, "flow instance deleted successfully", id, "")
}

// validateDescription rejects an empty description (whitespace only counts as empty).
func validateDescription(description string) error {
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("the flow description must not be empty")
	}
	return nil
}

// validateFlowLimits applies the defaults and checks them against the system limits.
// -1 means unlimited; 0 means "use the system default".
// validateFlowLimits applies the defaults and checks them against the tenant limits.
// -1 means unlimited; 0 means "use the tenant default".
//
// max_recursion_depth and max_node_count are not in the list: both are tenant-level policy and no longer part of the
// flow definition (the depth limit is read at run time, the node count is checked at the write boundary).
func validateFlowLimits(authInfo *auth.AuthInfo, timeout, maxExecCount *int) error {
	limits := flowconfig.Resolve(authInfo.Schema)

	// timeout: reject anything above max (-1 means unlimited).
	if *timeout > 0 && limits.MaxTimeout > 0 && *timeout > limits.MaxTimeout {
		return fmt.Errorf("timeout exceeds the tenant limit: %d > %d", *timeout, limits.MaxTimeout)
	}

	// max_exec_count
	if *maxExecCount > 0 && limits.MaxExecCount > 0 && *maxExecCount > limits.MaxExecCount {
		return fmt.Errorf("max_exec_count exceeds the tenant limit: %d > %d", *maxExecCount, limits.MaxExecCount)
	}

	// Retention is not validated here: the flow only carries the two switches (store_runs / session_memory) and the
	// counts are the tenant's own quota, so there is no per-flow number that could exceed anything.

	return nil
}

// GetBreakpoints returns the node_ids that carry a breakpoint.
func GetBreakpoints(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	id := c.Param("id")
	list, err := repo.ListBreakpoints(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"breakpoints": list}, "")
}

// SetBreakpoint adds or removes one breakpoint (persisted immediately during a run so a refresh does not lose it).
func SetBreakpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	id := c.Param("id")
	var req struct {
		NodeID string `json:"node_id"`
		On     bool   `json:"on"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	if req.NodeID == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "node_id is required", "")
		return
	}
	if err := repo.SetBreakpoint(authInfo.(*auth.AuthInfo), id, req.NodeID, req.On); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", nil, "")
}
