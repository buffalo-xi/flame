package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

// Egress 必须原样穿过 Wire / Spec 往返，三态都要保得住 —— 尤其 nil ≠ false：
// 前者是「没表态，跟部署默认」，后者是「明确要断网」，sandboxd 对它们的处理不同
// （mode=none 下后者会被拒，前者按配置走）。
func TestWireSpecEgressRoundTrip(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	cases := []struct {
		name string
		in   *bool
	}{
		{"未表态", nil},
		{"要出网", ptr(true)},
		{"明确断网", ptr(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := (&Spec{Egress: tc.in}).Wire()
			if (w.Egress == nil) != (tc.in == nil) {
				t.Fatalf("Wire 后 nil 语义丢了：in=%v out=%v", tc.in, w.Egress)
			}

			got := w.Spec()
			if (got.Egress == nil) != (tc.in == nil) {
				t.Fatalf("Spec 还原后 nil 语义丢了：in=%v out=%v", tc.in, got.Egress)
			}
			if tc.in != nil && *got.Egress != *tc.in {
				t.Errorf("Egress = %v, want %v", *got.Egress, *tc.in)
			}
		})
	}
}

// 未表态时 JSON 上不能出现 egress 字段：旧版 sandboxd 不认识它，
// 而「多出来的 false」在抓包/日志里会被读成「调用方明确要求断网」。
func TestWireSpecEgressOmitEmpty(t *testing.T) {
	b, err := json.Marshal((&Spec{}).Wire())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "egress") {
		t.Errorf("未表态时不该出现 egress 字段：%s", b)
	}

	no := false
	b, err = json.Marshal((&Spec{Egress: &no}).Wire())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"egress":false`) {
		t.Errorf("明确断网时必须把 false 发出去：%s", b)
	}
}
