package tools

import (
	"strings"
	"testing"
)

// 常驻工具（shell / browser）的描述是模型决定「要不要开常驻进程」的唯一线索：
// 必须同时给出「什么时候才用」（一次性手段优先）与「用完怎么收」，
// 只有用法没有代价时，模型会拿 shell / browser 当免费的探索手段，
// 于是每轮对话都留下一个不再使用却一直占资源的常驻进程。
func TestResidentToolDescriptionsMentionCost(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"shell", []string{"exec", "kill", "跨对话保留"}},
		{"browser", []string{"常驻", "close", "exec"}},
	}
	for _, c := range cases {
		tool, ok := Get(c.name)
		if !ok {
			t.Fatalf("工具 %s 未注册", c.name)
		}
		desc := tool.Meta().Description
		for _, w := range c.want {
			if !strings.Contains(desc, w) {
				t.Errorf("%s 的描述缺少 %q:\n%s", c.name, w, desc)
			}
		}
	}
}
