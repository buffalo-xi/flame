package sessions

import "testing"

// evictCount computes how many sessions must go when a protocol endpoint opens a new one: enough to get from used below
// limit.
// The boundaries are pinned here, above all limit<=0 (0 = new sessions forbidden, -1 = unlimited), which must never be
// computed as "delete every session".
func TestEvictCount(t *testing.T) {
	cases := []struct {
		name      string
		used, lim int
		want      int
	}{
		{"at the limit: free one slot", 10, 10, 1},
		{"two over: free three slots", 12, 10, 3},
		{"not over: free nothing", 9, 10, 0},
		{"empty table: free nothing", 0, 10, 0},
		{"limit 0 (creating forbidden): free nothing, leave it to the 507 path", 5, 0, 0},
		{"unlimited (-1): free nothing", 5, -1, 0},
	}
	for _, c := range cases {
		if got := evictCount(c.used, c.lim); got != c.want {
			t.Errorf("%s: evictCount(%d,%d) = %d, want %d", c.name, c.used, c.lim, got, c.want)
		}
	}
}
