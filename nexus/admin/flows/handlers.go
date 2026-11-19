package flows

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/models"
	"flame/nexus/shared/quota"
	agentsrepo "flame/nexus/shared/repository/agents"
	"flame/nexus/shared/repository/flows"
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
	raw := bytes.TrimSpace(diagram)
	if len(raw) == 0 {
		return "", nil, ""
	}
	// The canvas submits a JSON string (the front end stringifies once) and tools write an
	// object; both shapes have to be recognised.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", nil, ""
		}
		raw = []byte(s)
	}

	var doc struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Data struct {
				Label string `json:"label"`
			} `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, "" // shape problems are not ours (an empty or non-object diagram is handled at run time)
	}

	seen := make(map[string]string, len(doc.Nodes))
	var dups []string
	for _, n := range doc.Nodes {
		label := strings.TrimSpace(n.Data.Label)
		if label == "" {
			continue
		}
		if first, ok := seen[label]; ok {
			dups = append(dups, fmt.Sprintf("%s (nodes %s and %s)", label, first, n.ID))
			continue
		}
		seen[label] = n.ID
	}
	if len(dups) == 0 {
		return "", nil, ""
	}
	names := strings.Join(dups, "; ")
	return "flow.duplicate_label", gin.H{"names": names}, "duplicate node labels: " + names
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
	if err := models.CheckQuota("flow", cnt, quota.Resolve(schema).MaxFlows); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
		MaxRecursionDepth int             `json:"max_recursion_depth"`
		MaxNodeCount      int             `json:"max_node_count"`
		MaxExecCount      int             `json:"max_exec_count"`
		MaxRunRecords     int             `json:"max_run_records"`
		MaxSessionRecords int             `json:"max_session_records"`
		Breakpoints       []string        `json:"breakpoints"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Apply the flow config defaults and check them against the limits.
	if err := validateFlowLimits(authInfo.(*auth.AuthInfo), &req.Timeout, &req.MaxRecursionDepth, &req.MaxNodeCount, &req.MaxExecCount, &req.MaxRunRecords, &req.MaxSessionRecords); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

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
		MaxRecursionDepth: xsql.Int(req.MaxRecursionDepth),
		MaxNodeCount:      xsql.Int(req.MaxNodeCount),
		MaxExecCount:      xsql.Int(req.MaxExecCount),
		MaxRunRecords:     xsql.Int(req.MaxRunRecords),
		MaxSessionRecords: xsql.Int(req.MaxSessionRecords),
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
		"max_recursion_depth": Instance.MaxRecursionDepth,
		"max_node_count":      Instance.MaxNodeCount,
		"max_exec_count":      Instance.MaxExecCount,
		"max_run_records":     Instance.MaxRunRecords,
		"max_session_records": Instance.MaxSessionRecords,
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
		MaxRecursionDepth int             `json:"max_recursion_depth"`
		MaxNodeCount      int             `json:"max_node_count"`
		MaxExecCount      int             `json:"max_exec_count"`
		MaxRunRecords     int             `json:"max_run_records"`
		MaxSessionRecords int             `json:"max_session_records"`
		Breakpoints       []string        `json:"breakpoints"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Apply the flow config defaults and check them against the limits.
	if err := validateFlowLimits(authInfo.(*auth.AuthInfo), &req.Timeout, &req.MaxRecursionDepth, &req.MaxNodeCount, &req.MaxExecCount, &req.MaxRunRecords, &req.MaxSessionRecords); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

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
		MaxRecursionDepth: xsql.Int(req.MaxRecursionDepth),
		MaxNodeCount:      xsql.Int(req.MaxNodeCount),
		MaxExecCount:      xsql.Int(req.MaxExecCount),
		MaxRunRecords:     xsql.Int(req.MaxRunRecords),
		MaxSessionRecords: xsql.Int(req.MaxSessionRecords),
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
		"max_recursion_depth": Instance.MaxRecursionDepth,
		"max_node_count":      Instance.MaxNodeCount,
		"max_exec_count":      Instance.MaxExecCount,
		"max_run_records":     Instance.MaxRunRecords,
		"max_session_records": Instance.MaxSessionRecords,
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
func validateFlowLimits(authInfo *auth.AuthInfo, timeout, maxRecursionDepth, maxNodeCount, maxExecCount, maxRunRecords, maxSessionRecords *int) error {
	limits := flowconfig.Resolve(authInfo.Schema)

	// timeout: reject anything above max (-1 means unlimited).
	if *timeout > 0 && limits.MaxTimeout > 0 && *timeout > limits.MaxTimeout {
		return fmt.Errorf("timeout exceeds system limit: %d > %d", *timeout, limits.MaxTimeout)
	}

	// max_recursion_depth
	if *maxRecursionDepth > 0 && limits.MaxRecursionDepth > 0 && *maxRecursionDepth > limits.MaxRecursionDepth {
		return fmt.Errorf("max_recursion_depth exceeds system limit: %d > %d", *maxRecursionDepth, limits.MaxRecursionDepth)
	}

	// max_node_count
	if *maxNodeCount > 0 && limits.MaxNodeCount > 0 && *maxNodeCount > limits.MaxNodeCount {
		return fmt.Errorf("max_node_count exceeds system limit: %d > %d", *maxNodeCount, limits.MaxNodeCount)
	}

	// max_exec_count
	if *maxExecCount > 0 && limits.MaxExecCount > 0 && *maxExecCount > limits.MaxExecCount {
		return fmt.Errorf("max_exec_count exceeds system limit: %d > %d", *maxExecCount, limits.MaxExecCount)
	}

	// max_run_records: 0 stays unset (the system level takes over at run time); reject above the system limit (-1 means unlimited).
	if *maxRunRecords > 0 && limits.MaxRunRecords > 0 && *maxRunRecords > limits.MaxRunRecords {
		return fmt.Errorf("max_run_records exceeds system limit: %d > %d", *maxRunRecords, limits.MaxRunRecords)
	}

	// max_session_records: -1 / 0 disables session memory (allowed); once set it must be >= 1; reject above the system limit.
	if *maxSessionRecords == 0 {
		return fmt.Errorf("max_session_records must be >= 1 when set, or -1 to disable")
	}
	if *maxSessionRecords > 0 && limits.MaxSessionRecords > 0 && *maxSessionRecords > limits.MaxSessionRecords {
		return fmt.Errorf("max_session_records exceeds system limit: %d > %d", *maxSessionRecords, limits.MaxSessionRecords)
	}

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
