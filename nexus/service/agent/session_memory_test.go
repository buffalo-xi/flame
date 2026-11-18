package agent

import (
	"testing"

	"flame/nexus/shared/models"
)

// 历史注入的字节预算：最近的上下文必须留下，更旧的按预算从最旧开始裁。
func TestTrimHistoryByBudget(t *testing.T) {
	mk := func(in, out string) models.SessionRecord {
		return models.SessionRecord{Input: []byte(in), Output: out}
	}
	cases := []struct {
		name    string
		records []models.SessionRecord
		budget  int
		want    int // 期望保留条数
	}{
		{"都在预算内", []models.SessionRecord{mk("a", "bb"), mk("ccc", "dddd")}, 100, 2},
		{
			"超预算时从最旧的开始丢",
			[]models.SessionRecord{mk("aaaaaaaaaa", ""), mk("bbbbbbbbbb", ""), mk("cccccccccc", "")},
			25, 2,
		},
		{
			// 最新一轮就算自己就超了预算也要留：否则模型接不上刚才那轮
			"最新一条超预算仍保留",
			[]models.SessionRecord{mk("old", ""), mk("", string(make([]byte, 50)))},
			10, 1,
		},
		{"空列表", nil, 10, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := trimHistoryByBudget(c.records, c.budget)
			if len(got) != c.want {
				t.Fatalf("保留 %d 条，期望 %d", len(got), c.want)
			}
			// 保留的必须是「最新的那几条」，不能反过来
			if c.want > 0 && len(got) > 0 {
				last := c.records[len(c.records)-1]
				if string(got[len(got)-1].Input) != string(last.Input) || got[len(got)-1].Output != last.Output {
					t.Fatalf("保留的末条不是最新一条")
				}
			}
		})
	}
}
