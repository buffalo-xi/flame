package agent

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
)

// local__ belongs to the local endpoint runner only and does not overlap the mcp__ / rpc__ prefixes.
func TestLocalEndpointRunnerHas(t *testing.T) {
	r := &localEndpointRunner{}
	for _, name := range []string{"local__daily-report", "local__x"} {
		if !r.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"mcp__fs__read_file", "rpc__erp__create_order", "read_file", "custom__x"} {
		if r.Has(name) {
			t.Errorf("Has(%q) = true, want false", name)
		}
	}
}

// A missing tenant context must fail outright: without a schema there is no way to locate the tenant tables holding the endpoint.
func TestLocalEndpointRunnerRequiresSchema(t *testing.T) {
	_, err := (&localEndpointRunner{}).Invoke(context.Background(), tools.LocalCall{ToolName: "local__daily-report"})
	if err == nil || !strings.Contains(err.Error(), "tenant context") {
		t.Fatalf("want a tenant-context error, got %v", err)
	}
}
