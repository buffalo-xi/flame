package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/flowdoc"
	"flame/nexus/shared/models"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	flowruns "flame/nexus/shared/repository/flowruns"
	flows "flame/nexus/shared/repository/flows"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/versions"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// flowsManager implements tools.FlowsManager: it wraps the flows repository and the endpoint repository.
//
// The schema and the current user are frozen here and cannot be passed in from the tool layer - the same convention as agentManager /
// skillManager, so "which tenant may be read or written" never depends on what the model passed.
type flowsManager struct {
	schema string
	userID string
	repo   flows.Repository
	eps    svcendpoints.SvcEndpointRepository
	// agents is only used for the pre-check "a workflow name must not collide with an agent": the two entities share one namespace, and an
	// endpoint name is always equal to the entity name, so without this check creation would collide inside svc_endpoints.
	agents agents.Repository
}

// NewFlowsManager creates tools.FlowsManager (tenant schema and current user fixed).
func NewFlowsManager(schema, userID string) tools.FlowsManager {
	return &flowsManager{
		schema: schema,
		userID: userID,
		repo:   flows.NewPostgresRepository(),
		eps:    svcendpoints.NewPostgresRepository(),
		agents: agents.NewRepository(),
	}
}

func (m *flowsManager) auth() *auth.AuthInfo {
	// Carry userID: the repository uses authInfo.ID to write created_by / updated_by (an empty string becomes NULL)
	return &auth.AuthInfo{Schema: m.schema, ID: m.userID}
}

// Exists reports whether a workflow name is taken.
//
// Agents are checked too: both sides share one namespace (CreateFlow also checks agents internally; doing it early here is what produces a
// correctable error like "it collides with an agent" instead of a generic already-exists from the repository).
func (m *flowsManager) Exists(ctx context.Context, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	exists, err := m.repo.ExistsFlowName(m.auth(), name)
	if err != nil || exists {
		return exists, err
	}
	return m.agents.ExistsName(m.auth(), name, "")
}

// A newly created workflow keeps the editor's "new workflow" defaults (the initial form in frontend Flows.vue).
//
// Both retention switches are on: with store_runs off the run-record page stays empty forever and whoever triages it would
// only conclude "this flow has never run"; with session_memory off a conversation with a session_id silently loses its
// memory. The counts behind them are tenant quotas, not per-flow values.
const defaultFlowTimeout = 300 // seconds

// Create creates a workflow; when Endpoints is non-empty it also exposes those protocols as endpoints.
func (m *flowsManager) Create(ctx context.Context, in tools.FlowInput) (string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", errors.New("workflow name cannot be empty")
	}
	// The name becomes an endpoint name / the model-facing tool name local__<name>, so ASCII letters, digits and hyphens only.
	// The tool layer (create_flow) already blocks this once; the check here is a backstop for other callers - skipping it would leave another
	// hole where "the frontend cannot create it but a tool can".
	if !tools.ValidName(name) {
		return "", fmt.Errorf("workflow name %q is invalid: letters, digits and hyphens only (no leading hyphen, at most 64 characters); put the readable description in the description field", name)
	}
	description := strings.TrimSpace(in.Description)
	if description == "" {
		return "", errors.New("workflow description cannot be empty")
	}

	// The node count is a tenant-level quota and a static property of the diagram, so a tool-created workflow is held to
	// the same rule as one saved in the editor (canvas comments do not count).
	if doc, ok := flowdoc.Parse(in.Diagram); ok {
		if limit := flowconfig.Resolve(m.schema).MaxNodeCount; limit > 0 {
			if count := doc.CountNodes(flowdoc.CommentNodeType); count > limit {
				return "", fmt.Errorf("the diagram has %d nodes, above this tenant's limit of %d; split it into smaller workflows",
					count, limit)
			}
		}
	}

	// Quota: the same criterion as agent creation (CountFlows queries the database directly rather than adding a method to the repository
	// interface for one count)
	cnt, err := quota.CountFlows(m.schema)
	if err != nil {
		return "", err
	}
	if err := models.CheckQuota("flow", cnt, quota.Resolve(m.schema).MaxFlows); err != nil {
		return "", err
	}

	inst := &models.Flow{
		Name:              name,
		Diagram:           xsql.JSONB{Data: in.Diagram},
		Description:       xsql.String(description),
		Timeout:           defaultFlowTimeout,
		MaxExecCount:      -1,
		StoreRuns:         true,
		SessionMemory:     true,
	}
	if len(in.Arguments) > 0 {
		// Entry argument declaration: once exposed, the endpoint's input_schema is taken from this column
		// (CreateManagedRPC inside the repository's CreateFlow overwrites it).
		inst.Arguments = xsql.JSONB{Data: in.Arguments}
	}
	id, err := m.repo.CreateFlow(m.auth(), inst)
	if err != nil {
		return "", err
	}

	if err := m.expose(id, in.Endpoints); err != nil {
		// Roll back the definition just created when the endpoints could not be exposed: leaving a half-built flow that the user believes has
		// endpoints is far harder to triage than a plain error. Endpoints are deleted first: the fresh one may already be exposed, and
		// DeleteFlow would refuse.
		_ = m.eps.DeleteByTarget(m.auth(), "flow", id)
		_ = m.repo.DeleteFlow(m.auth(), id)
		return "", err
	}
	return id, nil
}

// expose opens these protocols to the outside (written to svc_endpoints).
//
// Creating the flow already added an unexposed RPC endpoint (CreateManagedRPC), so this is "make sure it exists and flip the switch": a direct
// Create would hit the (target, protocol) unique constraint.
//
// It does not write flows.endpoints: that column is a derived mirror of svc_endpoints, rewritten as a whole by the repository's syncEndpoints
// after every add / delete - writing it directly would be overwritten, looking exposed while having no entry point at all.
func (m *flowsManager) expose(flowID string, protocols []string) error {
	if len(protocols) == 0 {
		return nil
	}
	existing, err := m.eps.ListAll(m.auth(), &svcendpoints.ListSvcEndpointsRequest{
		TargetType: "flow",
		TargetID:   flowID,
	})
	if err != nil {
		return fmt.Errorf("failed to query flow endpoints: %w", err)
	}
	endpointIDs := make(map[string]string, len(existing))
	for i := range existing {
		endpointIDs[existing[i].Protocol] = existing[i].ID
	}

	for _, p := range protocols {
		if id, ok := endpointIDs[p]; ok {
			// The auto-added RPC endpoint is in this class: only flip the switch, never recreate
			if err := m.eps.UpdateExposed(m.auth(), id, true); err != nil {
				return fmt.Errorf("failed to expose the %s endpoint: %w", p, err)
			}
			continue
		}
		ep := &models.SvcEndpoint{
			TargetType: "flow",
			TargetID:   flowID,
			Protocol:   p,
			Exposed:    xsql.Bool(true),
		}
		if _, err := m.eps.Create(m.auth(), ep); err != nil {
			return fmt.Errorf("failed to create the %s endpoint: %w", p, err)
		}
	}
	return nil
}

// List lists every workflow of this tenant. Limit=-1 is the repository's "take everything" convention; the tool layer truncates for display.
func (m *flowsManager) List(ctx context.Context) ([]tools.FlowInfo, error) {
	list, _, err := m.repo.ListFlows(m.auth(), &flows.ListFlowsRequest{Page: 1, Limit: -1})
	if err != nil {
		return nil, err
	}
	out := make([]tools.FlowInfo, 0, len(list))
	for i := range list {
		out = append(out, flowInfo(&list[i]))
	}
	return out, nil
}

// Get takes the details by id first and falls back to name; missing returns (nil, nil) (consistent with AgentManager.Get, the caller reports).
func (m *flowsManager) Get(ctx context.Context, id, name string) (*tools.FlowDetail, error) {
	f, err := m.find(ctx, id, name)
	if err != nil || f == nil {
		return nil, err
	}
	diagram, err := f.Diagram.MarshalJSON()
	if err != nil {
		return nil, err
	}
	detail := &tools.FlowDetail{FlowInfo: flowInfo(f), Diagram: json.RawMessage(diagram)}
	// Leave it empty (omitempty) when nothing was declared: the tool layer can then tell "no arguments" from "arguments that cannot be read".
	if !f.Arguments.IsNil() {
		args, err := f.Arguments.MarshalJSON()
		if err != nil {
			return nil, err
		}
		detail.Arguments = json.RawMessage(args)
	}
	return detail, nil
}

// UpdateDiagram replaces only the diagram: name / description / limits / breakpoints are untouched.
//
// Why not UpdateFlow: that rewrites the whole row, and GetFlow does not load breakpoints, so editing the diagram through it would clear them.
// The edge tools should only touch the diagram, hence the repository's narrow update.
func (m *flowsManager) UpdateDiagram(ctx context.Context, id, name string, diagram json.RawMessage) (*tools.FlowInfo, error) {
	f, err := m.find(ctx, id, name)
	if err != nil || f == nil {
		return nil, err
	}
	if err := m.repo.UpdateFlowDiagram(m.auth(), f.ID, xsql.JSONB{Data: diagram}); err != nil {
		return nil, err
	}
	info := flowInfo(f)
	return &info, nil
}

// UpdateArguments replaces only the entry argument declaration: name / description / diagram / limits / breakpoints are untouched.
//
// Why not UpdateFlow: that rewrites the whole row, and GetFlow does not load breakpoints, so editing the arguments through it would clear them
// (the same trap as UpdateDiagram). The endpoint input_schema is synced by the repository in the same transaction, so the tool layer does not
// have to refresh it by hand.
func (m *flowsManager) UpdateArguments(ctx context.Context, id, name string, arguments json.RawMessage) (*tools.FlowInfo, error) {
	f, err := m.find(ctx, id, name)
	if err != nil || f == nil {
		return nil, err
	}
	if err := m.repo.UpdateFlowArguments(m.auth(), f.ID, xsql.JSONB{Data: arguments}); err != nil {
		return nil, err
	}
	info := flowInfo(f)
	return &info, nil
}

// Delete deletes a workflow and returns the deleted row; missing returns (nil, nil).
//
// The entity is returned so the tool layer can echo "the real id + name": the name may point at another definition after "rename / delete then
// recreate with the same name", and a destructive operation must leave a trace that can be checked. The missing-case error text also lives in
// the tool layer, consistent with the other tools.
func (m *flowsManager) Delete(ctx context.Context, id, name string) (*tools.FlowInfo, error) {
	f, err := m.find(ctx, id, name)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, nil
	}
	// Endpoints are handled by DeleteFlow in one place: unexposed ones (including the auto-added RPC) are deleted with the flow; when an
	// exposed endpoint remains the repository refuses - that is the signal "the outside is still using it", and a deletion started by a model
	// must not cut it off quietly; the user retracts it first. The run history goes with the row for the same reason: the repository
	// deletes flow_run_records / flow_node_records in the same transaction.
	if err := m.repo.DeleteFlow(m.auth(), f.ID); err != nil {
		// A released version means the flow is live: the repository refuses, and the model must not try to work around
		// it - taking a service offline is the user's call (unrelease in the version panel), never a side effect of a
		// delete the model started.
		var released *versions.ErrHasReleasedVersion
		if errors.As(err, &released) {
			return nil, fmt.Errorf("v%d is still released, so %q is live and cannot be deleted; ask the user to unrelease it in the version panel first",
				released.Version, f.Name)
		}
		return nil, err
	}
	deleted := flowInfo(f)
	return &deleted, nil
}

// RunTest is the trial run: after locating the flow it goes to the execution channel registered by executors and really runs it.
//
// exposed is not checked (the object of a trial run usually has no endpoint yet), but a deprecated flow is refused on the same criterion as the
// runtime - "the test passes and production cannot run" is harder to triage than a plain refusal.
func (m *flowsManager) RunTest(ctx context.Context, ref string, args map[string]any, sessionID string, callDepth int, followDrafts bool) (*tools.FlowRunResult, error) {
	f, err := m.find(ctx, ref, ref)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, nil // the tool layer reports "does not exist"
	}
	if !models.IsRunnable(int(f.Status)) {
		return nil, fmt.Errorf("workflow %q is deprecated and cannot run", f.Name)
	}
	if runFlowTest == nil {
		return nil, errFlowTestUnavailable
	}
	return runFlowTest(ctx, m.auth(), f.ID, args, sessionID, callDepth, followDrafts)
}

// GetRun reads one run record (the run plus its node records).
//
// The data has the same source as the frontend "run records" page (flow_run_records / flow_node_records written by executors.persistRun), so an
// agent can look up not only its own run_flow runs but also a frontend debug run or a run triggered by another agent, by run_id - which is the
// real entry point when triaging.
func (m *flowsManager) GetRun(ctx context.Context, runID string) (*tools.FlowRunDetail, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, errors.New("a run id is required")
	}
	// flow_run_records.id is a uuid column too: a non-UUID can never match, but the error is 22P02 rather than no rows, which would smear a
	// SQL error in the model's face. Treat it as "missing" and let the tool layer say "run_id does not exist" in plain words.
	if !isUUID(runID) {
		return nil, nil
	}
	run, nodes, err := flowruns.NewRepository().GetRunDetail(m.schema, runID)
	if err != nil {
		// Not found is not an error: the tool layer gives a "run_id does not exist" hint, and an sql error is never thrown at the model
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if run == nil {
		return nil, nil
	}

	out := &tools.FlowRunDetail{
		FlowID:     run.FlowID,
		RunID:      run.ID,
		Status:     run.Status,
		RunType:    run.RunType,
		ErrMsg:     run.ErrMsg,
		Result:     run.Result,
		DurationMs: run.DurationMs,
		StartedAt:  formatRunTime(run.StartedAt),
		RunArgs:    decodeRunVar(run.RunArgs),
		FlowVars:   decodeRunVar(run.RunFlowVars),
		NodeTotal:  len(nodes),
		Nodes:      make([]tools.FlowRunNode, 0, len(nodes)),
	}
	if run.FinishedAt != nil {
		out.FinishedAt = formatRunTime(*run.FinishedAt)
	}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, toFlowRunNode(n))
	}
	return out, nil
}

// toFlowRunNode converts a stored node record into the tool response body.
func toFlowRunNode(n *models.FlowNodeRecord) tools.FlowRunNode {
	return tools.FlowRunNode{
		NodeID:      n.NodeID,
		Type:        n.NodeType,
		Status:      n.Status,
		ErrCode:     n.ErrCode,
		ErrMsg:      n.ErrMsg,
		ErrStack:    n.ErrStack,
		RetryCount:  n.RetryCount,
		ErrorAction: n.ErrorAction,
		DurationMs:  n.DurationMs,
		InputVars:   decodeRunVar(n.InputVars),
		OutputVars:  decodeRunVar(n.OutputVars),
		LocalVars:   decodeRunVar(n.LocalVars),
		Extra:       decodeRunVar(n.Extra),
		Logs:        decodeRunLogs(n.RunLogs),
		// Truncation already happened at write time: tell the caller "the value is not complete", so nobody asserts the data is only this
		Truncated: n.InputVarsTruncated || n.OutputVarsTruncated || n.LocalVarsTruncated || n.RunLogsTruncated,
	}
}

// decodeRunVar decodes jsonb into a directly readable object; bad data is handed back as a string, so at least nothing is lost.
func decodeRunVar(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

// decodeRunLogs turns run_logs (a JSON array of strings) into multi-line text for the model to read.
func decodeRunLogs(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return string(raw)
	}
	return strings.Join(lines, "\n")
}

func formatRunTime(t xsql.Time) string {
	if time.Time(t).IsZero() {
		return ""
	}
	return time.Time(t).Format(time.RFC3339)
}

// find locates one workflow row (with its diagram). id first, name as the fallback.
//
// The repository has no "get by name": the list query deliberately skips the diagram (to avoid scanning the whole table with a big JSON), so a
// name hit still needs a second read of the full row. Adding a method to the shared interface for one read-only tool is not worth it.
func (m *flowsManager) find(ctx context.Context, id, name string) (*models.Flow, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" && name == "" {
		return nil, errors.New("a workflow id or name is required")
	}
	// Only a UUID shape is looked up as an id. flows.id is a uuid column: putting a name into `WHERE id = $1` does not yield "no such row",
	// it makes Postgres report 22P02 (invalid input syntax for type uuid), so the name fallback would never be reached and the "id or name"
	// promised in the tool documentation would be a lie. The tool layer always passes (ref, ref), so this is the only place deciding the shape.
	if id != "" && isUUID(id) {
		f, err := m.repo.GetFlow(m.auth(), id)
		switch {
		case err == nil:
			return f, nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	if name == "" {
		return nil, nil
	}

	list, _, err := m.repo.ListFlows(m.auth(), &flows.ListFlowsRequest{Page: 1, Limit: -1, Name: name})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return m.repo.GetFlow(m.auth(), list[i].ID)
		}
	}
	return nil, nil
}

// isUUID reports whether ref has the id shape.
//
// The reason is in find: flows.id / flow_run_records.id are uuid columns, and a non-UUID value does not "find nothing" - it fails the whole
// statement. Splitting in the application layer is what makes the name fallback really executable.
func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// flowInfo compresses the model row into the tool-layer view: endpoints only list the exposed protocols (protocol + switch is the storage shape,
// while the model wants "which entry points exist").
func flowInfo(f *models.Flow) tools.FlowInfo {
	eps := make([]string, 0, len(f.Endpoints))
	for _, ep := range f.Endpoints {
		if ep.Exposed {
			eps = append(eps, ep.Protocol)
		}
	}
	return tools.FlowInfo{
		ID:          f.ID,
		Name:        f.Name,
		Description: string(f.Description),
		Status:      int(f.Status),
		Endpoints:   eps,
	}
}
