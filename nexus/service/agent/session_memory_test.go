package agent

import (
	"testing"

	"flame/nexus/shared/models"
)

// The byte budget for injected history: the most recent context must survive, older records are cut from the oldest end by the budget.
func TestTrimHistoryByBudget(t *testing.T) {
	mk := func(in, out string) models.SessionRecord {
		return models.SessionRecord{Input: []byte(in), Output: out}
	}
	cases := []struct {
		name    string
		records []models.SessionRecord
		budget  int
		want    int // expected number of retained records
	}{
		{"all within budget", []models.SessionRecord{mk("a", "bb"), mk("ccc", "dddd")}, 100, 2},
		{
			"drop from the oldest once over budget",
			[]models.SessionRecord{mk("aaaaaaaaaa", ""), mk("bbbbbbbbbb", ""), mk("cccccccccc", "")},
			25, 2,
		},
		{
			// the newest round is kept even when it alone exceeds the budget: otherwise the model cannot follow the round just finished
			"newest kept even over budget",
			[]models.SessionRecord{mk("old", ""), mk("", string(make([]byte, 50)))},
			10, 1,
		},
		{"empty list", nil, 10, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := trimHistoryByBudget(c.records, c.budget)
			if len(got) != c.want {
				t.Fatalf("kept %d records, want %d", len(got), c.want)
			}
			// what is kept must be "the newest few", never the reverse
			if c.want > 0 && len(got) > 0 {
				last := c.records[len(c.records)-1]
				if string(got[len(got)-1].Input) != string(last.Input) || got[len(got)-1].Output != last.Output {
					t.Fatalf("the last retained record is not the newest one")
				}
			}
		})
	}
}
