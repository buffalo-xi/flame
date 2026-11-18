package executors

import (
	"errors"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/capability"
)

// 工作流 code 节点的能力闸。
//
// 为什么要有：它走 /v1/code 通道（服务端托管解释器），与 agent 的 exec 工具不是同一条
// 通道，在补这道闸之前，平台关掉执行能力后 agent 跑不了命令、流程里的 code 节点却照样能跑。
func TestEnsureCodeCapability(t *testing.T) {
	old := capabilitySnapshot
	t.Cleanup(func() { capabilitySnapshot = old })

	// 只开 exec、关掉 code：正是「agent 不能跑命令，但工作流还能跑」的漏
	capabilitySnapshot = func(string) map[string]bool {
		return map[string]bool{capability.Exec: true}
	}

	var ce *capabilityError
	err := ensureCodeCapability("acme")
	if !errors.As(err, &ce) {
		t.Fatalf("code 能力未开通时应拒绝，got %v", err)
	}
	if !strings.Contains(err.Error(), capability.Code) {
		t.Errorf("错误信息应指出缺哪一档能力，got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "插件与能力") {
		t.Errorf("错误信息应说明去哪里开通，got %q", err.Error())
	}
	if got := scriptErrCode(err); got != flow.ErrCodeCapability {
		t.Errorf("错误码 = %q, want %q（不能被当成脚本报错重试）", got, flow.ErrCodeCapability)
	}

	// 无租户语义（单测 / 内部调用）：没有平台策略可言，放行
	if err := ensureCodeCapability(""); err != nil {
		t.Errorf("schema 为空时不该拦，got %v", err)
	}

	// 开通后放行
	capabilitySnapshot = func(string) map[string]bool {
		return map[string]bool{capability.Code: true}
	}
	if err := ensureCodeCapability("acme"); err != nil {
		t.Errorf("开通后应放行，got %v", err)
	}
}

// 快照读失败时 Snapshot 是 fail-closed（返回空 map），闸门必须跟着拒绝。
func TestEnsureCodeCapabilityFailClosed(t *testing.T) {
	old := capabilitySnapshot
	t.Cleanup(func() { capabilitySnapshot = old })

	capabilitySnapshot = func(string) map[string]bool { return map[string]bool{} }
	if err := ensureCodeCapability("acme"); err == nil {
		t.Fatal("快照为空（读失败 / 未开通）时应拒绝")
	}
}

// 过渡期兼容：旧键 sandbox.script 必须被归一成 sandbox.code 后仍能放行，
// 否则「代码先上、库还没迁」的那段时间工作流 code 节点会全挂。
func TestEnsureCodeCapabilityLegacyKey(t *testing.T) {
	old := capabilitySnapshot
	t.Cleanup(func() { capabilitySnapshot = old })

	capabilitySnapshot = func(string) map[string]bool {
		return map[string]bool{capability.Normalize(capability.LegacyScript): true}
	}
	if err := ensureCodeCapability("acme"); err != nil {
		t.Errorf("旧键行应经 Normalize 生效，got %v", err)
	}
}

// schema 从 ExecContext 取；缺 Ctx 时不猜、直接放行。
func TestSchemaOf(t *testing.T) {
	if got := schemaOf(nil); got != "" {
		t.Errorf("nil 节点应返回空 schema，got %q", got)
	}
	if got := schemaOf(&flow.NodeResult{}); got != "" {
		t.Errorf("无 Ctx 应返回空 schema，got %q", got)
	}
	nr := &flow.NodeResult{Ctx: &flow.ExecContext{Schema: "acme"}}
	if got := schemaOf(nr); got != "acme" {
		t.Errorf("schemaOf = %q, want acme", got)
	}
}
