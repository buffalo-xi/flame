package agents

import (
	"strconv"
	"testing"

	"flame/nexus/shared/models"
)

// makeRecords 造 n 条记录，ID 依次 r0..r(n-1)，顺序与 ListBySessionPage 的返回一致：旧 → 新。
func makeRecords(n int) []models.SessionRecord {
	out := make([]models.SessionRecord, n)
	for i := range out {
		out[i] = models.SessionRecord{ID: "r" + strconv.Itoa(i)}
	}
	return out
}

// pageRecords 的回归用例。
//
// 历史 bug：写成 records[:limit]（从尾部丢）。因为 records 是「旧 → 新」，
// 从尾部丢砍掉的恰好是**最新一条** —— 表现为会话超过 10 条记录后，
// 点击侧边栏打开会话时看不到最后一个记录。
func TestPageRecords(t *testing.T) {
	t.Run("多取一条：从头部丢掉最旧的，最新一条必须保留", func(t *testing.T) {
		got, hasMore := pageRecords(makeRecords(11), 10)
		if !hasMore {
			t.Fatal("多取到一条 → 还有更早的记录，hasMore 应为 true")
		}
		if len(got) != 10 {
			t.Fatalf("len = %d, want 10", len(got))
		}
		if got[0].ID != "r1" {
			t.Errorf("头部应为 r1（最旧的 r0 被丢掉）, got %s", got[0].ID)
		}
		if got[len(got)-1].ID != "r10" {
			t.Errorf("最新一条 r10 必须保留, got %s", got[len(got)-1].ID)
		}
	})

	t.Run("正好一页：不截断，且没有更早的记录", func(t *testing.T) {
		got, hasMore := pageRecords(makeRecords(10), 10)
		if hasMore {
			t.Error("长度正好等于 limit → hasMore 应为 false")
		}
		if len(got) != 10 || got[9].ID != "r9" {
			t.Errorf("不应改动分页结果: len=%d last=%s", len(got), got[len(got)-1].ID)
		}
	})

	t.Run("不足一页", func(t *testing.T) {
		got, hasMore := pageRecords(makeRecords(3), 10)
		if hasMore || len(got) != 3 {
			t.Errorf("len=%d hasMore=%v, want len=3 hasMore=false", len(got), hasMore)
		}
	})

	t.Run("空列表", func(t *testing.T) {
		got, hasMore := pageRecords(nil, 10)
		if hasMore || len(got) != 0 {
			t.Errorf("len=%d hasMore=%v, want len=0 hasMore=false", len(got), hasMore)
		}
	})
}
