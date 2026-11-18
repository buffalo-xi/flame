package callchain

import (
	"context"
	"strings"
	"testing"
)

// 固定两个实体：F（flow）调 A（agent）。
// 元素形如 <id>|<kind>，说明段只为可读性，不参与判重。
var (
	idF    = "f1111111-1111-1111-1111-111111111111"
	idA    = "a2222222-2222-2222-2222-222222222222"
	flowF  = Key(KindFlow, idF)
	agentA = Key(KindAgent, idA)
)

// TestSiblingRepeatIsNotLoop F 先后两次调 A（f a a）不是循环：
// 平级调用各自从「父链」派生，链里从来没有 A 自己。
func TestSiblingRepeatIsNotLoop(t *testing.T) {
	parent := []string{flowF}
	for i := 0; i < 2; i++ {
		if Contains(parent, agentA) {
			t.Fatalf("第 %d 次并列调用被误判为循环", i+1)
		}
		// 派生出的子链带上了 A，但不能改到父链（否则第二次就会命中）
		child := Append(parent, agentA)
		if len(child) != 2 || ID(child[1]) != idA {
			t.Fatalf("子链 = %v", child)
		}
		if len(parent) != 1 {
			t.Fatalf("父链被改写: %v", parent)
		}
	}
}

// TestReorderIsLoop f → a → f 必须被拒：A 的祖先链里已经有 F。
func TestReorderIsLoop(t *testing.T) {
	chainAtA := Append([]string{flowF}, agentA)
	if !Contains(chainAtA, flowF) {
		t.Fatal("f→a→f 应当命中，实际未命中")
	}
}

// TestSelfCallIsLoop 自调用（a → a）同样命中：在 A 内部再调 A，链里已有 A。
func TestSelfCallIsLoop(t *testing.T) {
	if !Contains([]string{agentA}, agentA) {
		t.Fatal("a→a 应当命中")
	}
}

// TestContainsIgnoresNote 判重只比 id：说明缺省、说明不同、大小写不同都算同一实体。
func TestContainsIgnoresNote(t *testing.T) {
	chain := []string{idA + "|t0/agent"}
	cases := []struct {
		key  string
		want bool
		why  string
	}{
		{idA, true, "说明可缺省"},
		{idA + "|flow", true, "说明不同不影响身份"},
		{strings.ToUpper(idA), true, "大小写不敏感"},
		{idA + "|whatever", true, "说明内容不校验语义"},
		{"a2222222-2222-2222-2222-222222222223", false, "不同 id 不能命中"},
		{"", false, "空 key 恒 false"},
	}
	for _, c := range cases {
		if got := Contains(chain, c.key); got != c.want {
			t.Errorf("Contains(%q) = %v, want %v（%s）", c.key, got, c.want, c.why)
		}
	}
}

// TestKeyAndID Key 生成 <id>|<kind>；ID 取竖线前的部分。
func TestKeyAndID(t *testing.T) {
	if flowF != idF+"|flow" {
		t.Fatalf("Key = %q", flowF)
	}
	if ID(flowF) != idF {
		t.Fatalf("ID = %q", ID(flowF))
	}
	if ID(idA) != idA {
		t.Fatal("裸 id 的 ID 应当就是它自己")
	}
	// 缺 kind 时退化成裸 id：仍可判重，只是少一段上下文
	if Key("", "x") != "x" {
		t.Errorf("缺 kind 应退化为裸 id, got %q", Key("", "x"))
	}
	if Key(KindAgent, "") != "" {
		t.Error("缺 id 应当返回空串（无身份可言）")
	}
	// 统一小写：DB 里是小写，URL / 前端可能传大写
	if Key("Agent", "ABC") != "abc|agent" {
		t.Errorf("Key 未归一化大小写: %q", Key("Agent", "ABC"))
	}
}

// TestValid 元素形态：id 段只允许标识符，说明段允许 "/"，空段一律非法。
func TestValid(t *testing.T) {
	valid := []string{
		flowF,
		agentA,
		"abc",
		"abc|agent",
		// 说明段只做字符白名单，不校验结构：对端若带了 schema 或实例后缀，
		// 旧版本也要接受（丢掉整条链等于漏检）。
		"abc|t0/agent",
		"abc|agent@nexus-2",
	}
	for _, k := range valid {
		if !Valid(k) {
			t.Errorf("%q 应当合法", k)
		}
	}
	invalid := []string{
		"",
		"|t0/agent",                     // id 为空
		"abc|",                          // 说明为空
		"bad key",                       // 空格
		"bad:key",                       // 冒号
		"abc|bad key",                   // 说明里含空格
		strings.Repeat("a", maxIDLen+1), // id 超长
	}
	for _, k := range invalid {
		if Valid(k) {
			t.Errorf("%q 应当判为非法", k)
		}
	}
}

// TestNormalizeDropsGarbage 脏元素丢弃而不是拒掉整次调用：
// 链是协作式信息，不能因为一个坏元素就让正常请求 400。
func TestNormalizeDropsGarbage(t *testing.T) {
	got := Normalize([]any{flowF, 42, "bad key", agentA})
	if len(got) != 2 || ID(got[0]) != idF || ID(got[1]) != idA {
		t.Fatalf("Normalize = %v", got)
	}
}

// TestNormalizeLowercases 对端 / 前端可能给大写，判重前统一成小写，
// 否则同一个实体会因为写法不同而漏检。
func TestNormalizeLowercases(t *testing.T) {
	got := Normalize([]any{"ABC-1|Agent"})
	if len(got) != 1 || got[0] != "abc-1|agent" {
		t.Fatalf("Normalize = %v", got)
	}
}

// TestParseParamsTooLong 超长必须拒（tooLong=true），不能截断：
// 被截掉的那几段里可能正好藏着要检测的重入。
func TestParseParamsTooLong(t *testing.T) {
	raw := make([]any, 0, MaxLen+1)
	for i := 0; i <= MaxLen; i++ {
		raw = append(raw, agentA)
	}
	params := map[string]any{"_meta": map[string]any{MetaKey: raw}}
	if _, tooLong := ParseParams(params); !tooLong {
		t.Fatal("超过上限的链应当报 tooLong")
	}
	if _, tooLong := ParseParams(map[string]any{"_meta": map[string]any{MetaKey: []any{flowF}}}); tooLong {
		t.Fatal("正常长度不该报 tooLong")
	}
	if _, tooLong := ParseParams(nil); tooLong {
		t.Fatal("没有 params 时不该报 tooLong")
	}
}

// TestContextRoundTrip 出站调用靠 ctx 带链（不加函数参数），必须能取回。
func TestContextRoundTrip(t *testing.T) {
	ctx := With(context.Background(), []string{flowF})
	if got := From(ctx); len(got) != 1 || got[0] != flowF {
		t.Fatalf("From = %v", got)
	}
	if got := From(context.Background()); got != nil {
		t.Fatalf("空 ctx 应返回 nil, got %v", got)
	}
	// 空链不写 ctx：避免给普通调用凭空多一层 value
	if got := From(With(context.Background(), nil)); got != nil {
		t.Fatalf("空链不该写入 ctx, got %v", got)
	}
}
