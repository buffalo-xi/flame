package toolgrants

import (
	"testing"

	"flame/nexus/service/agent/toolgrant"
)

// 超级智能体的合并必须**保留**不可用标记，而不是把不可用项过滤掉：
// 内置工具是固有基线，平台禁用时仍要列出来（标「平台未开通」），
// 直接藏掉会让用户以为工具丢了。
func TestMergeSupervisorBuiltinsKeepsUnavailable(t *testing.T) {
	browser := toolgrant.ToolInfo{
		Ref: "builtin:browser", Name: "browser",
		Available:         false,
		UnavailableKind:   toolgrant.UnavailableCapability,
		UnavailableReason: "平台未开通",
	}
	read := toolgrant.ToolInfo{Ref: "builtin:read_file", Name: "read_file", Available: true}
	plugin := toolgrant.ToolInfo{Ref: "plugin:web_search", Name: "web_search", Available: true}

	out := mergeSupervisorBuiltins([]toolgrant.ToolInfo{plugin}, nil, []toolgrant.ToolInfo{browser, read})
	if len(out) != 3 {
		t.Fatalf("合并后应有 3 项（插件 + 2 个内置），got %d: %+v", len(out), out)
	}

	var got *toolgrant.ToolInfo
	for i := range out {
		if out[i].Ref == "builtin:browser" {
			got = &out[i]
		}
	}
	if got == nil {
		t.Fatal("不可用的内置工具被丢掉了 —— 预览会显示成「工具不存在」而不是「平台未开通」")
	}
	if got.Available {
		t.Error("browser 被标成可用：能力闸在合并这一步丢了")
	}
	if got.UnavailableKind != toolgrant.UnavailableCapability {
		t.Errorf("归因 = %q, want %q", got.UnavailableKind, toolgrant.UnavailableCapability)
	}
}

// deny 优先：内置基线也不能覆盖显式拒绝。
func TestMergeSupervisorBuiltinsDenyWins(t *testing.T) {
	browser := toolgrant.ToolInfo{Ref: "builtin:browser", Name: "browser", Available: false}
	out := mergeSupervisorBuiltins(nil, []string{"builtin:browser"}, []toolgrant.ToolInfo{browser})
	if len(out) != 0 {
		t.Fatalf("deny 项仍被合并进来：%+v", out)
	}
}

// 告警只列不可用项，且合成一条（逐条加会刷屏）。
func TestUnavailableRefs(t *testing.T) {
	list := []toolgrant.ToolInfo{
		{Ref: "builtin:read_file", Available: true},
		{Ref: "builtin:browser", Available: false, UnavailableKind: toolgrant.UnavailableCapability},
		{Ref: "builtin:exec", Available: false, UnavailableKind: toolgrant.UnavailableCapability},
	}
	got := unavailableRefs(list)
	if len(got) != 2 || got[0] != "builtin:browser" || got[1] != "builtin:exec" {
		t.Fatalf("不可用 refs = %v, want [builtin:browser builtin:exec]", got)
	}
	if len(unavailableRefs(nil)) != 0 {
		t.Error("空列表应返回空")
	}
}
