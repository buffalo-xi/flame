package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	flows "flame/nexus/shared/repository/flows"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/tools/xsql"
)

// endpointsAdmin implements tools.EndpointsAdmin: endpoint administration for the supervisor tools.
//
// The same layering as flowsManager: the tenant schema and the current user are frozen at construction, so the tool
// layer cannot pass in "which tenant to write". Storage errors carrying meaning (managed slot, still exposed, never
// released) are translated into the tools package's sentinels, because that is where the "what to do next" wording
// lives.
type endpointsAdmin struct {
	schema string
	userID string
	agents agents.Repository
	flows  flows.Repository
	eps    svcendpoints.SvcEndpointRepository
}

// NewEndpointsAdmin creates tools.EndpointsAdmin (tenant schema and current user fixed).
func NewEndpointsAdmin(schema, userID string) tools.EndpointsAdmin {
	return &endpointsAdmin{
		schema: schema,
		userID: userID,
		agents: agents.NewRepository(),
		flows:  flows.NewPostgresRepository(),
		eps:    svcendpoints.NewPostgresRepository(),
	}
}

func (m *endpointsAdmin) auth() *auth.AuthInfo {
	return &auth.AuthInfo{Schema: m.schema, ID: m.userID}
}

// targetID resolves the entity name to its uuid. Both names live in one namespace (an endpoint name always equals the
// entity name), so the caller never passes an id.
func (m *endpointsAdmin) targetID(targetType, name string) (string, error) {
	name = strings.TrimSpace(name)
	switch targetType {
	case "agent":
		list, _, err := m.agents.ListAgents(m.auth(), &agents.ListAgentsRequest{Name: name, Page: 1, Limit: -1})
		if err != nil {
			return "", err
		}
		for i := range list {
			// The list request is a fuzzy match; an exact case-insensitive comparison decides.
			if strings.EqualFold(list[i].Name, name) {
				return list[i].ID, nil
			}
		}
		return "", fmt.Errorf("%w: agent %q", tools.ErrTargetNotFound, name)
	case "flow":
		list, _, err := m.flows.ListFlows(m.auth(), &flows.ListFlowsRequest{Name: name, Page: 1, Limit: -1})
		if err != nil {
			return "", err
		}
		for i := range list {
			if strings.EqualFold(list[i].Name, name) {
				return list[i].ID, nil
			}
		}
		return "", fmt.Errorf("%w: workflow %q", tools.ErrTargetNotFound, name)
	}
	return "", fmt.Errorf("target_type must be agent or flow, got %q", targetType)
}

// translate maps the endpoint repository's typed errors onto the tool-facing sentinels.
func translateEndpointError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, svcendpoints.ErrManagedEndpoint):
		return fmt.Errorf("%w: %v", tools.ErrEndpointManaged, err)
	case errors.Is(err, svcendpoints.ErrEndpointExposed):
		return fmt.Errorf("%w: %v", tools.ErrEndpointExposed, err)
	case errors.Is(err, svcendpoints.ErrNoReleasedVersion):
		return fmt.Errorf("%w: %v", tools.ErrEndpointNotReleased, err)
	}
	return err
}

// List returns every endpoint of the named target, in the repository's order.
func (m *endpointsAdmin) List(_ context.Context, targetType, target string) ([]tools.EndpointState, error) {
	id, err := m.targetID(targetType, target)
	if err != nil {
		return nil, err
	}
	rows, err := m.eps.ListAll(m.auth(), &svcendpoints.ListSvcEndpointsRequest{TargetType: targetType, TargetID: id})
	if err != nil {
		return nil, err
	}
	out := make([]tools.EndpointState, 0, len(rows))
	for i := range rows {
		out = append(out, tools.EndpointState{
			TargetType: targetType,
			Target:     target,
			Protocol:   rows[i].Protocol,
			Exposed:    rows[i].Exposed.Bool(),
		})
	}
	return out, nil
}

// Create adds one endpoint slot. Exposing at creation time goes through the repository's validation (deprecated target
// / never released), which is why exposed is passed straight into the row.
func (m *endpointsAdmin) Create(_ context.Context, targetType, target, protocol string, exposed bool) (*tools.EndpointState, error) {
	id, err := m.targetID(targetType, target)
	if err != nil {
		return nil, err
	}
	ep := &models.SvcEndpoint{
		TargetType: targetType,
		TargetID:   id,
		Protocol:   protocol,
		Exposed:    xsql.Bool(exposed),
	}
	if _, err := m.eps.Create(m.auth(), ep); err != nil {
		return nil, translateEndpointError(err)
	}
	return &tools.EndpointState{TargetType: targetType, Target: target, Protocol: protocol, Exposed: exposed}, nil
}

// SetExposed flips the switch on the listed protocols (empty = every endpoint of the target, used by retract).
//
// A protocol that does not exist is reported instead of silently skipped: "exposed nothing" would read like success
// while the outside entrance the caller expected is still closed.
func (m *endpointsAdmin) SetExposed(ctx context.Context, targetType, target string, protocols []string, exposed bool) ([]tools.EndpointState, error) {
	id, err := m.targetID(targetType, target)
	if err != nil {
		return nil, err
	}
	rows, err := m.eps.ListAll(m.auth(), &svcendpoints.ListSvcEndpointsRequest{TargetType: targetType, TargetID: id})
	if err != nil {
		return nil, err
	}
	byProtocol := make(map[string]*models.SvcEndpoint, len(rows))
	for i := range rows {
		byProtocol[rows[i].Protocol] = &rows[i]
	}

	// Empty protocols = every endpoint of the target, in the repository's order.
	if len(protocols) == 0 {
		protocols = make([]string, 0, len(rows))
		for i := range rows {
			protocols = append(protocols, rows[i].Protocol)
		}
	}
	for _, p := range protocols {
		if _, ok := byProtocol[p]; !ok {
			return nil, fmt.Errorf("%w: %s %q has no %s endpoint; create it first", tools.ErrEndpointMissing, targetType, target, p)
		}
	}

	for _, p := range protocols {
		row := byProtocol[p]
		if row.Exposed.Bool() == exposed {
			continue // already in the requested state: no write, no error
		}
		if err := m.eps.UpdateExposed(m.auth(), row.ID, exposed); err != nil {
			return nil, translateEndpointError(err)
		}
	}

	// Re-read so the caller sees the state that is actually stored (the switch write also touches updated_at).
	return m.List(ctx, targetType, target)
}

// Delete removes one endpoint slot. The repository refuses a managed (flow RPC) or still-exposed endpoint; both are
// translated so the caller gets the next step rather than a raw storage error.
func (m *endpointsAdmin) Delete(_ context.Context, targetType, target, protocol string) error {
	id, err := m.targetID(targetType, target)
	if err != nil {
		return err
	}
	rows, err := m.eps.ListAll(m.auth(), &svcendpoints.ListSvcEndpointsRequest{TargetType: targetType, TargetID: id})
	if err != nil {
		return err
	}
	endpointID := ""
	for i := range rows {
		if rows[i].Protocol == protocol {
			endpointID = rows[i].ID
			break
		}
	}
	if endpointID == "" {
		return fmt.Errorf("%w: %s %q has no %s endpoint", tools.ErrEndpointMissing, targetType, target, protocol)
	}
	return translateEndpointError(m.eps.Delete(m.auth(), endpointID))
}
