package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeEndpointsAdmin records what the tools asked for and replays a canned answer.
type fakeEndpointsAdmin struct {
	list    []EndpointState
	listErr error

	createdProtocol string
	createdExposed  bool
	createErr       error
	// exposedErr fails only the exposed=true call, so "create it unexposed instead" can be asserted.
	exposedErr error

	setProtocols []string
	setExposed   bool
	setErr       error

	deletedProtocol string
	deleteErr       error
}

func (f *fakeEndpointsAdmin) List(context.Context, string, string) ([]EndpointState, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.list, nil
}

func (f *fakeEndpointsAdmin) Create(_ context.Context, targetType, target, protocol string, exposed bool) (*EndpointState, error) {
	if exposed && f.exposedErr != nil {
		return nil, f.exposedErr
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.createdProtocol = protocol
	f.createdExposed = exposed
	return &EndpointState{TargetType: targetType, Target: target, Protocol: protocol, Exposed: exposed}, nil
}

func (f *fakeEndpointsAdmin) SetExposed(_ context.Context, targetType, target string, protocols []string, exposed bool) ([]EndpointState, error) {
	if f.setErr != nil {
		return nil, f.setErr
	}
	f.setProtocols = protocols
	f.setExposed = exposed
	return f.list, nil
}

func (f *fakeEndpointsAdmin) Delete(_ context.Context, _, target, protocol string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedProtocol = protocol
	return nil
}

// The eight tools must land in the two supervisor-only categories; a new tool in a plain category would show up in the
// grant catalog of ordinary agents.
func TestEndpointToolsAreSupervisorOnly(t *testing.T) {
	want := map[string]string{
		"create_agent_endpoint": CategoryAgentAdmin,
		"delete_agent_endpoint": CategoryAgentAdmin,
		"expose_agent_endpoint": CategoryAgentAdmin,
		"retract_agent_endpoint": CategoryAgentAdmin,
		"create_flow_endpoint":  CategoryFlowAdmin,
		"delete_flow_endpoint":  CategoryFlowAdmin,
		"expose_flow_endpoint":  CategoryFlowAdmin,
		"retract_flow_endpoint": CategoryFlowAdmin,
	}
	for name, category := range want {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if got := tool.Meta().Category; got != category {
			t.Errorf("%s category = %q, want %q", name, got, category)
		}
		if !supervisorOnlyCategories[category] {
			t.Errorf("%s category %q is not supervisor-only", name, category)
		}
	}
}

// protocol=rpc on a flow is a managed slot: the tool reports "nothing created" instead of writing.
func TestCreateFlowEndpointRPCIsManaged(t *testing.T) {
	f := &fakeEndpointsAdmin{}
	got, err := (&createFlowEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
		"name":     "weekly-report",
		"protocol": "rpc",
	})
	if err != nil {
		t.Fatalf("create_flow_endpoint(rpc) = %v", err)
	}
	m := got.(map[string]any)
	if m["created"] != false {
		t.Errorf("created = %v, want false", m["created"])
	}
	if !strings.Contains(fmt.Sprint(m["note"]), "expose_flow_endpoint") {
		t.Errorf("note should point at expose_flow_endpoint, got %v", m["note"])
	}
	if f.createdProtocol != "" {
		t.Errorf("the managed rpc slot must not be created, got %q", f.createdProtocol)
	}
}

// An existing slot is reported, not re-created: the model then goes straight to expose / retract.
func TestCreateAgentEndpointReportsExisting(t *testing.T) {
	f := &fakeEndpointsAdmin{list: []EndpointState{{TargetType: "agent", Target: "writer", Protocol: "mcp", Exposed: true}}}
	got, err := (&createAgentEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
		"name":     "writer",
		"protocol": "mcp",
	})
	if err != nil {
		t.Fatalf("create_agent_endpoint = %v", err)
	}
	m := got.(map[string]any)
	if m["created"] != false {
		t.Errorf("created = %v, want false", m["created"])
	}
	if f.createdProtocol != "" {
		t.Errorf("Create must not be called, got %q", f.createdProtocol)
	}
}

// Deleting an exposed endpoint has to name the next step instead of leaking a storage error.
func TestDeleteAgentEndpointExposedAsksForRetract(t *testing.T) {
	f := &fakeEndpointsAdmin{deleteErr: fmt.Errorf("%w: the endpoint is still exposed", ErrEndpointExposed)}
	_, err := (&deleteAgentEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
		"name":     "writer",
		"protocol": "mcp",
	})
	if err == nil {
		t.Fatal("an exposed endpoint must not be deletable")
	}
	if !strings.Contains(err.Error(), "retract_agent_endpoint") {
		t.Errorf("error should point at retract_agent_endpoint, got %v", err)
	}
}

// The flow RPC slot is refused before reaching storage, with the reason spelled out.
func TestDeleteFlowEndpointRPCIsRefused(t *testing.T) {
	f := &fakeEndpointsAdmin{}
	_, err := (&deleteFlowEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
		"name":     "weekly-report",
		"protocol": "rpc",
	})
	if err == nil {
		t.Fatal("deleting a managed rpc endpoint must be refused")
	}
	if !strings.Contains(err.Error(), "expose_flow_endpoint") {
		t.Errorf("error should point at expose_flow_endpoint / retract_flow_endpoint, got %v", err)
	}
	if f.deletedProtocol != "" {
		t.Errorf("Delete must not be called, got %q", f.deletedProtocol)
	}
}

// Protocols are lowercased and de-duplicated before they reach the manager; expose always names at least one.
func TestExposeAgentEndpointNormalizesProtocols(t *testing.T) {
	f := &fakeEndpointsAdmin{}
	_, err := (&exposeAgentEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
		"name":      "writer",
		"protocols": []any{"MCP", "mcp"},
	})
	if err != nil {
		t.Fatalf("expose_agent_endpoint = %v", err)
	}
	if strings.Join(f.setProtocols, ",") != "mcp" {
		t.Errorf("protocols = %v, want [mcp]", f.setProtocols)
	}
	if !f.setExposed {
		t.Error("expose must flip the switch on")
	}
}

// Retract without protocols means "all of them": the manager receives an empty list, not a single guess.
func TestRetractFlowEndpointWithoutProtocolsMeansAll(t *testing.T) {
	f := &fakeEndpointsAdmin{list: []EndpointState{{Protocol: "mcp", Target: "weekly-report"}, {Protocol: "rpc", Target: "weekly-report"}}}
	_, err := (&retractFlowEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
		"name": "weekly-report",
	})
	if err != nil {
		t.Fatalf("retract_flow_endpoint = %v", err)
	}
	if len(f.setProtocols) != 0 {
		t.Errorf("protocols = %v, want empty (= every endpoint)", f.setProtocols)
	}
	if f.setExposed {
		t.Error("retract must flip the switch off")
	}
}

// A protocol outside mcp / rpc is refused at the tool layer, before any storage call.
func TestEndpointToolsRejectUnknownProtocol(t *testing.T) {
	f := &fakeEndpointsAdmin{}
	_, err := (&createAgentEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
		"name":     "writer",
		"protocol": "llm",
	})
	if err == nil {
		t.Fatal("protocol=llm must be refused")
	}
	if f.createdProtocol != "" {
		t.Errorf("Create must not be called, got %q", f.createdProtocol)
	}
}

// A missing target and a never-released flow both have to be actionable, not raw.
func TestEndpointErrorsCarryNextStep(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: agent \"nobody\"", ErrTargetNotFound), "list_agents"},
		{fmt.Errorf("%w: no released version", ErrEndpointNotReleased), "release a version"},
	}
	for _, tc := range cases {
		f := &fakeEndpointsAdmin{createErr: tc.err}
		_, err := (&createAgentEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
			"name":     "writer",
			"protocol": "mcp",
		})
		if err == nil {
			t.Fatalf("%v: expected an error", tc.err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error %q should mention %q", tc.err, err, tc.want)
		}
	}
}

// A context without the manager must say so rather than panic.
func TestEndpointToolsWithoutManager(t *testing.T) {
	_, err := (&exposeFlowEndpointTool{}).Execute(context.Background(), &Context{}, map[string]any{
		"name":      "weekly-report",
		"protocols": []any{"mcp"},
	})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("want an 'unavailable' error, got %v", err)
	}
}

// Creating with exposed=true before any release must still leave the slot behind: only the switch needs a version, and
// failing outright forces a second create later - the caller has no way back to this point.
func TestCreateAgentEndpointDefersExposureWithoutRelease(t *testing.T) {
	f := &fakeEndpointsAdmin{exposedErr: fmt.Errorf("%w: no released version", ErrEndpointNotReleased)}
	out, err := (&createAgentEndpointTool{}).Execute(context.Background(), &Context{EndpointAdmin: f}, map[string]any{
		"name":     "writer",
		"protocol": "mcp",
		"exposed":  true,
	})
	if err != nil {
		t.Fatalf("create_agent_endpoint = %v", err)
	}
	res, _ := out.(map[string]any)
	if res["created"] != true {
		t.Errorf("the slot should still be created: %v", res)
	}
	rr, ok := res["release_required"].(map[string]any)
	if !ok {
		t.Fatalf("release_required missing: %v", res)
	}
	if rr["target_type"] != "agent" || rr["target"] != "writer" {
		t.Errorf("release_required does not name the target: %v", rr)
	}
	if f.createdExposed {
		t.Error("the retry must create the slot unexposed")
	}
}
