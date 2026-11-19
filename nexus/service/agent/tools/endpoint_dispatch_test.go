package tools

import (
	"context"
	"strings"
	"testing"
)

// stubEndpointRunner is a stand-in for the local endpoint execution channel (it owns local__).
type stubEndpointRunner struct {
	called bool
	in     LocalCall
}

func (s *stubEndpointRunner) Has(name string) bool {
	return strings.HasPrefix(name, ExternalPrefixLocal)
}

func (s *stubEndpointRunner) Invoke(_ context.Context, in LocalCall) (any, error) {
	s.called = true
	s.in = in
	return map[string]any{"content": "ok", "is_error": false}, nil
}

// local__<endpoint> has to go to the EndpointRunner, not the externalToolRunner -
// the latter only knows mcp__ / rpc__, so forwarding it there would only produce "not an executable external connection tool".
func TestExecuteToolDelegatesToEndpointRunner(t *testing.T) {
	ext := &stubExternalRunner{}
	ep := &stubEndpointRunner{}
	c := &Context{
		Schema: "t0", AgentName: "poet", AgentID: "agent-1",
		UserID: "user-1", SessionID: "sess-1", RunID: "run-1",
		CallDepth:    2,
		AllowedTools: map[string]bool{"local__daily-report": true},
		Externals:    ext,
		Endpoints:    ep,
	}

	got, err := ExecuteTool(context.Background(), c, "local__daily-report", map[string]any{"topic": "x"})
	if err != nil {
		t.Fatalf("ExecuteTool = %v", err)
	}
	if !ep.called {
		t.Fatal("the local endpoint runner was not invoked")
	}
	if ext.called {
		t.Fatal("local__ must not be taken over by the external connection runner")
	}
	// Identity must be forwarded: the invoked flow lands in the caller's tenant / session venue.
	if ep.in.Schema != "t0" || ep.in.UserID != "user-1" || ep.in.SessionID != "sess-1" || ep.in.RunID != "run-1" {
		t.Errorf("identity was not forwarded correctly: %+v", ep.in)
	}
	if ep.in.AgentID != "agent-1" || ep.in.ToolName != "local__daily-report" {
		t.Errorf("tool context was not forwarded correctly: %+v", ep.in)
	}
	if ep.in.Args["topic"] != "x" {
		t.Errorf("the arguments were not forwarded: %v", ep.in.Args)
	}
	// The stack depth must be forwarded: a local endpoint runs at depth +1, and the agent -> flow -> agent cycle is bounded by
	// it. Drop this and the execution side always sees 0, which means the guard rail does not exist.
	if ep.in.CallDepth != 2 {
		t.Errorf("CallDepth = %d, want 2", ep.in.CallDepth)
	}
	// The output limit matches the external tools: an endpoint may return a whole report.
	if ep.in.MaxOutput != DefaultMaxOutput {
		t.Errorf("maxOutput = %d, want %d", ep.in.MaxOutput, DefaultMaxOutput)
	}
	if _, ok := got.(map[string]any); !ok {
		t.Fatalf("result type = %T", got)
	}
}

// An unauthorized call is still blocked before dispatch: the endpoint table is not even queried (the same as external connections).
func TestExecuteToolRejectsUnauthorizedLocalBeforeInvoke(t *testing.T) {
	ep := &stubEndpointRunner{}
	c := &Context{
		Schema: "t0", AgentName: "poet",
		AllowedTools: map[string]bool{"read_file": true},
		Endpoints:    ep,
	}

	_, err := ExecuteTool(context.Background(), c, "local__daily-report", nil)
	if err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("an unauthorized call should be refused by the grant gate, got %v", err)
	}
	if ep.called {
		t.Fatal("an unauthorized call must not reach the endpoint runner")
	}
}
