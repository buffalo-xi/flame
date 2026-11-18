package sessions

import "testing"

// evictCount 是「协议端点建会话」的回收条数计算：从 used 降到 limit 以下要删几条。
// 这里钉住边界，尤其是 limit<=0（0 = 禁止新建，-1 = 不限）时绝不能算成「删光所有会话」。
func TestEvictCount(t *testing.T) {
	cases := []struct {
		name      string
		used, lim int
		want      int
	}{
		{"已达上限：腾一格", 10, 10, 1},
		{"超了 2 条：腾 3 格", 12, 10, 3},
		{"没超：不腾", 9, 10, 0},
		{"空库：不腾", 0, 10, 0},
		{"上限 0（禁止新建）：不腾，交给 507 路径", 5, 0, 0},
		{"不限（-1）：不腾", 5, -1, 0},
	}
	for _, c := range cases {
		if got := evictCount(c.used, c.lim); got != c.want {
			t.Errorf("%s: evictCount(%d,%d) = %d, 期望 %d", c.name, c.used, c.lim, got, c.want)
		}
	}
}
