package globalvars

import "testing"

// 没有租户上下文（平台级 / 单测）时原样返回：不属于任何租户，没有可解析的值。
func TestExpandWithoutSchema(t *testing.T) {
	in := "数据库地址是 ${global_vars.DB_HOST}"
	out, err := Expand("", in)
	if err != nil {
		t.Fatalf("无 schema 不该报错: %v", err)
	}
	if out != in {
		t.Fatalf("无 schema 应原样返回，实际 %q", out)
	}
}

// 没有引用时不查库（也不该报错）：绝大多数文档里根本没有 ${global_vars.X}。
func TestExpandWithoutRefs(t *testing.T) {
	out, err := Expand("t0", "# 标题\n没有任何引用")
	if err != nil {
		t.Fatalf("无引用不该报错: %v", err)
	}
	if out != "# 标题\n没有任何引用" {
		t.Fatalf("内容被改动: %q", out)
	}
}

func TestDocRefs(t *testing.T) {
	text := "用 ${global_vars.DB_HOST} 和 ${global_vars.DB_HOST}，再来 ${global_vars.REGION}"
	got := DocRefs(text)
	want := []string{"DB_HOST", "REGION"}
	if len(got) != len(want) {
		t.Fatalf("DocRefs = %v, want %v（需要去重）", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DocRefs = %v, want %v（保持出现顺序）", got, want)
		}
	}
}

// 变量名必须符合字符集：带点 / 中括号的写法不应被当成引用（防止误伤示例代码）。
func TestDocRefsIgnoresInvalidNames(t *testing.T) {
	if got := DocRefs("${global_vars.a.b} ${global_vars.中文}"); len(got) != 0 {
		t.Fatalf("非法变量名不该被识别，实际 %v", got)
	}
}


