package executors

import "testing"

const (
	testAgentKey = "a2222222-2222-2222-2222-222222222222|agent"
	testFlowKey  = "f1111111-1111-1111-1111-111111111111|flow"
)

// _call_chain has to be stripped from the run arguments: left in args it would become a flow variable
// ({{_call_chain}}) and would be stored with the session memory.
func TestExtractCallChainStripsArgument(t *testing.T) {
	in := map[string]interface{}{
		"topic":       "weekly report",
		"_call_chain": []interface{}{testAgentKey},
	}
	chain, clean := extractCallChain(in)

	if len(chain) != 1 || chain[0] != testAgentKey {
		t.Fatalf("chain = %v", chain)
	}
	if _, existed := clean["_call_chain"]; existed {
		t.Fatal("_call_chain must be stripped from the business arguments")
	}
	if clean["topic"] != "weekly report" {
		t.Fatalf("a business argument was damaged: %v", clean)
	}
	// The original map must not be modified: the caller may still use it (the same convention as extractCallDepth)
	if _, existed := in["_call_chain"]; !existed {
		t.Fatal("the input must not be modified")
	}
}

// With no chain the input is returned as is, so an ordinary call does not get a needless new map.
func TestExtractCallChainAbsent(t *testing.T) {
	in := map[string]interface{}{"topic": "x"}
	chain, clean := extractCallChain(in)
	if chain != nil {
		t.Fatalf("chain = %v, want nil", chain)
	}
	if len(clean) != 1 || clean["topic"] != "x" {
		t.Fatalf("clean = %v", clean)
	}
}

// Dirty elements are dropped rather than failing the whole chain: the chain is cooperative information, and one bad
// element must not reject a normal call.
func TestExtractCallChainDropsGarbage(t *testing.T) {
	in := map[string]interface{}{"_call_chain": []interface{}{42, "bad key", testFlowKey}}
	chain, _ := extractCallChain(in)
	if len(chain) != 1 || chain[0] != testFlowKey {
		t.Fatalf("chain = %v", chain)
	}
}
