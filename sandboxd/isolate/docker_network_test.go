package isolate

import (
	"strings"
	"testing"

	"flame/tools/sandbox"
)

// containerArgs 只做翻译，不做策略：策略（部署上限 × 请求意图）已经在
// sandboxd 的 handleExec 里判完，这里只把「这次要不要断网」翻译成 --network。
//
// Start 与 Acquire 共用这一个函数，所以这组用例同时钉住两条路径：
//   - 单工：egress 每请求生效；
//   - 双工：egress 只在建单元那次生效（之后的复用改不了，也不需要改）。
func TestDockerContainerArgsNetwork(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	s := &dockerSandbox{opts: Options{Network: "bridge"}}
	cases := []struct {
		name   string
		egress *bool
		want   string
	}{
		{"未表态 → 跟配置", nil, "bridge"},
		{"要出网 → 用配置里的真实网络", ptr(true), "bridge"},
		{"明确断网 → none", ptr(false), "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := s.containerArgs(&sandbox.Spec{Egress: tc.egress}, sandbox.Limits{})
			if !strings.Contains(strings.Join(args, " "), "--network "+tc.want) {
				t.Errorf("--network 应为 %q，实际参数：%v", tc.want, args)
			}
		})
	}

	// 配置本身就是 none 时，egress=true 也只能得到 none —— 裁决层漏判的兜底。
	// 宁可断网，不做意外出网。
	s = &dockerSandbox{opts: Options{Network: "none"}}
	args := s.containerArgs(&sandbox.Spec{Egress: ptr(true)}, sandbox.Limits{})
	if !strings.Contains(strings.Join(args, " "), "--network none") {
		t.Errorf("配置为 none 时不该给出网，实际参数：%v", args)
	}
}
