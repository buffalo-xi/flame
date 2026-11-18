package executors

import "testing"

const (
	testAgentKey = "a2222222-2222-2222-2222-222222222222|agent"
	testFlowKey  = "f1111111-1111-1111-1111-111111111111|flow"
)

// _call_chain 必须从运行参数里剥离：它若留在 args 里会变成流程变量
// （{{_call_chain}}），也会跟着会话记忆落库。
func TestExtractCallChainStripsArgument(t *testing.T) {
	in := map[string]interface{}{
		"topic":       "周报",
		"_call_chain": []interface{}{testAgentKey},
	}
	chain, clean := extractCallChain(in)

	if len(chain) != 1 || chain[0] != testAgentKey {
		t.Fatalf("chain = %v", chain)
	}
	if _, existed := clean["_call_chain"]; existed {
		t.Fatal("_call_chain 必须从业务参数里剥离")
	}
	if clean["topic"] != "周报" {
		t.Fatalf("业务参数被破坏: %v", clean)
	}
	// 原始 map 不能被改：调用方可能还要用（同 extractCallDepth 的约定）
	if _, existed := in["_call_chain"]; !existed {
		t.Fatal("不应该修改入参")
	}
}

// 没有链时原样返回，避免给普通调用凭空造一个新 map。
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

// 脏元素丢弃而不是整条链失败：链是协作式信息，不能因为一个坏元素拒掉正常调用。
func TestExtractCallChainDropsGarbage(t *testing.T) {
	in := map[string]interface{}{"_call_chain": []interface{}{42, "bad key", testFlowKey}}
	chain, _ := extractCallChain(in)
	if len(chain) != 1 || chain[0] != testFlowKey {
		t.Fatalf("chain = %v", chain)
	}
}
