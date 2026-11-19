package tools

import (
	"strings"
	"testing"
)

// 常驻工具（shell / browser）的描述是模型决定「要不要开常驻进程」的唯一线索：
// 必须同时给出「什么时候才用」（一次性手段优先）与「用完怎么收」，
// 只有用法没有代价时，模型会拿 shell / browser 当免费的探索手段，
// 于是每轮对话都留下一个不再使用却一直占资源的常驻进程。
//
// 断言只钉**标识符**（工具名 / 参数值：exec、kill、close、snapshot）与「描述足够长」，
// 不钉散文原句：描述的语言会随产品主语言变化，中文句子不该成为接口契约。
// 那两件事（先试一次性手段、用完收掉）恰恰是靠这些标识符表达的。
func TestResidentToolDescriptionsMentionCost(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"shell", []string{"exec", "kill"}},
		{"browser", []string{"exec", "close", "snapshot"}},
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
		// 只写「是什么」不足以让模型避开常驻成本：代价 + 收尾两件事都要说到
		if n := len([]rune(desc)); n < 150 {
			t.Errorf("%s 的描述过短（%d 字符），常驻成本与收尾动作要说清：\n%s", c.name, n, desc)
		}
	}
}
