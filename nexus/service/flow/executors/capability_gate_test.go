package executors

import (
	"errors"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/capability"
)

// The capability gate of a workflow code node.
//
// Why it has to exist: it goes through the /v1/code channel (a server-hosted interpreter), which is not the same
// channel as the agent's exec tool — before this gate was added, disabling the execution capability stopped an
// agent from running commands while a code node inside a flow still ran.
func TestEnsureCodeCapability(t *testing.T) {
	old := capabilitySnapshot
	t.Cleanup(func() { capabilitySnapshot = old })

	// exec on, code off: exactly the hole where "the agent cannot run a command but a workflow still can"
	capabilitySnapshot = func(string) map[string]bool {
		return map[string]bool{capability.Exec: true}
	}

	var ce *capabilityError
	err := ensureCodeCapability("acme")
	if !errors.As(err, &ce) {
		t.Fatalf("it must be refused while the code capability is not granted, got %v", err)
	}
	if !strings.Contains(err.Error(), capability.Code) {
		t.Errorf("the message should name the missing capability, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Plugins & capabilities") {
		t.Errorf("the message should say where to enable it, got %q", err.Error())
	}
	if got := scriptErrCode(err); got != flow.ErrCodeCapability {
		t.Errorf("error code = %q, want %q (it must not be treated as a retryable script failure)", got, flow.ErrCodeCapability)
	}

	// No tenant semantics (unit tests / internal calls): there is no platform policy, so it passes
	if err := ensureCodeCapability(""); err != nil {
		t.Errorf("an empty schema should not be blocked, got %v", err)
	}

	// Granted: it passes
	capabilitySnapshot = func(string) map[string]bool {
		return map[string]bool{capability.Code: true}
	}
	if err := ensureCodeCapability("acme"); err != nil {
		t.Errorf("it should pass once granted, got %v", err)
	}
}

// When the snapshot cannot be read, Snapshot is fail-closed (an empty map) and the gate has to refuse with it.
func TestEnsureCodeCapabilityFailClosed(t *testing.T) {
	old := capabilitySnapshot
	t.Cleanup(func() { capabilitySnapshot = old })

	capabilitySnapshot = func(string) map[string]bool { return map[string]bool{} }
	if err := ensureCodeCapability("acme"); err == nil {
		t.Fatal("an empty snapshot (read failed / not granted) must be refused")
	}
}

// Transitional compatibility: the old key sandbox.script has to be normalised to sandbox.code and still pass,
// otherwise during "the code shipped, the database has not migrated yet" every workflow code node fails.
func TestEnsureCodeCapabilityLegacyKey(t *testing.T) {
	old := capabilitySnapshot
	t.Cleanup(func() { capabilitySnapshot = old })

	capabilitySnapshot = func(string) map[string]bool {
		return map[string]bool{capability.Normalize(capability.LegacyScript): true}
	}
	if err := ensureCodeCapability("acme"); err != nil {
		t.Errorf("the legacy key row should take effect through Normalize, got %v", err)
	}
}

// schema comes from ExecContext; without a Ctx nothing is guessed and it passes.
func TestSchemaOf(t *testing.T) {
	if got := schemaOf(nil); got != "" {
		t.Errorf("a nil node should yield an empty schema, got %q", got)
	}
	if got := schemaOf(&flow.NodeResult{}); got != "" {
		t.Errorf("no Ctx should yield an empty schema, got %q", got)
	}
	nr := &flow.NodeResult{Ctx: &flow.ExecContext{Schema: "acme"}}
	if got := schemaOf(nr); got != "acme" {
		t.Errorf("schemaOf = %q, want acme", got)
	}
}
