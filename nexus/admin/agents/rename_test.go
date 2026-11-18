package agents

import (
	"strings"
	"testing"
)

// 重命名的合法性判定：全是纯字符串规则，所以能直接测，不需要 gin / 存储。
func TestCheckRename(t *testing.T) {
	cases := []struct {
		old, new, want string
	}{
		{"", "a.md", "path is required"},
		{"a.md", "", "new_path is required"},
		{"a.md", "a.md", "新名字和原名相同"},
		{"a.md", "b.md", ""},
		{"a.md", "docs/b.md", ""}, // 换目录也在支持范围内（后端不限制，前端暂不暴露）
		{"a.md", ".hidden", "名字不能以 . 开头（会被列表隐藏）"},
		{"a.md", "dir/.hidden", "名字不能以 . 开头（会被列表隐藏）"},
		// 自嵌套：dir → dir/sub 会先复制成功、再在删旧前缀时把新位置一起删掉
		{"dir", "dir/sub", "不能移动到自己的子目录里"},
		{"dir", "dir/sub/x.md", "不能移动到自己的子目录里"},
		// 只是名字前缀相同，不是子树 —— 别误伤
		{"dir", "dir2", ""},
		{"dir", "dirx/y", ""},
		{"uploads/a.txt", "uploads/b.txt", ""},
	}
	for _, c := range cases {
		if got := checkRename(c.old, c.new); got != c.want {
			t.Errorf("checkRename(%q, %q) = %q, want %q", c.old, c.new, got, c.want)
		}
	}
}

// 条目路径规则由新建与重命名共用，改动这里会同时影响两个接口。
func TestCheckEntryPath(t *testing.T) {
	long := strings.Repeat("文", 121)
	cases := []struct {
		clean, want string
	}{
		{"", "path is required"},
		{"a.md", ""},
		{"a/b.md", ""},
		{".keep", "名字不能以 . 开头（会被列表隐藏）"},
		{"dir/.keep", "名字不能以 . 开头（会被列表隐藏）"},
		{"a." + "keep", ""}, // 只有首字符是 . 才算隐藏
		{long, "名字过长（上限 120 字符）"},
		{strings.Repeat("文", 120), ""},
		{strings.Repeat("a/", 15) + "x", ""},                // 16 段：上限内
		{strings.Repeat("a/", 16) + "x", "目录层级过深（上限 16 层）"}, // 17 段：超一层
	}
	for _, c := range cases {
		if got := checkEntryPath(c.clean); got != c.want {
			t.Errorf("checkEntryPath(%q) = %q, want %q", c.clean, got, c.want)
		}
	}
}
