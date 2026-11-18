package daemon

import (
	"strings"
	"testing"

	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
)

// resolveExecNetwork 是「部署上限 × 请求意图」这一层的裁决点。
// 租户许可不在这里（nexus 侧算完才传 egress=true），所以这里的输入只有三者之一：
//
//	nil   = 没表态（老 nexus / 非 exec 通道）
//	false = 明确要断网
//	true  = 要出网
func TestResolveExecNetwork(t *testing.T) {
	silenceLogs(t)

	ptr := func(b bool) *bool { return &b }

	cases := []struct {
		name    string
		policy  string
		mode    string
		in      *bool // 请求意图
		want    *bool // 裁决后写回的 spec.Egress（nil = 不写，交给 isolate 跟配置）
		wantErr string
	}{
		// off：部署级硬闸，谁都不能出网。
		{"off + 未表态 → 跟配置(断网)", NetworkOff, isolate.DockerName, nil, nil, ""},
		{"off + 明确断网", NetworkOff, isolate.DockerName, ptr(false), nil, ""},
		{"off + 要出网 → 拒绝", NetworkOff, isolate.DockerName, ptr(true), nil, "network=off"},

		// on：一律出网，请求里的 false 也不改变结果（控制权在部署方）。
		{"on + 未表态 → 跟配置(出网)", NetworkOn, isolate.DockerName, nil, nil, ""},
		{"on + 想断网也被忽略", NetworkOn, isolate.DockerName, ptr(false), nil, ""},

		// on-request：按请求裁决，缺省断网。
		{"on-request + 未表态 → 断网", NetworkOnRequest, isolate.DockerName, nil, ptr(false), ""},
		{"on-request + 明确断网", NetworkOnRequest, isolate.DockerName, ptr(false), ptr(false), ""},
		{"on-request + 要出网", NetworkOnRequest, isolate.DockerName, ptr(true), ptr(true), ""},

		// mode=none：宿主进程断不了网。要出网的放行（本来就能出），
		// 要断网的只能拒 —— 静默按出网跑就是撒谎。
		{"on-request + none + 要出网", NetworkOnRequest, isolate.NoneName, ptr(true), ptr(true), ""},
		{"on-request + none + 未表态 → 拒", NetworkOnRequest, isolate.NoneName, nil, nil, "mode=none"},
		{"on-request + none + 明确断网 → 拒", NetworkOnRequest, isolate.NoneName, ptr(false), nil, "mode=none"},

		{"未知策略", "maybe", isolate.DockerName, nil, nil, "未知策略"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &Config{Mode: tc.mode, Network: tc.policy}}
			spec := &sandbox.Spec{Egress: tc.in}

			err := s.resolveExecNetwork(spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v，want 含 %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错，got %v", err)
			}
			switch {
			case tc.want == nil && spec.Egress != nil:
				t.Errorf("spec.Egress 应保持 nil（交给 isolate 跟配置），got %v", *spec.Egress)
			case tc.want != nil && spec.Egress == nil:
				t.Errorf("spec.Egress 应被裁决成 %v，got nil", *tc.want)
			case tc.want != nil && *spec.Egress != *tc.want:
				t.Errorf("spec.Egress = %v, want %v", *spec.Egress, *tc.want)
			}
		})
	}
}
