package gw

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestRedisSticky：真实 redis 上的 Lookup/Bind/RemoveBackend 集成测试。
// 本机无 redis 时跳过（生产/CI 配了 redis 才跑）。
func TestRedisSticky(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("no redis at localhost:6379: %v", err)
	}
	defer client.Close()

	s := NewRedisSticky(client)
	c := context.Background()

	// 未绑定 → miss
	if _, ok, _ := s.Lookup(c, "itest-s1", time.Hour); ok {
		t.Fatalf("未绑定 session 不应命中")
	}

	// 首次抢占 → 抢到
	grabbed, err := s.Bind(c, "itest-s1", "http://a", time.Hour)
	if err != nil || !grabbed {
		t.Fatalf("首次 Bind 应抢到，got grabbed=%v err=%v", grabbed, err)
	}

	// 重复抢占（不同后端）→ 没抢到，保持原后端
	grabbed, err = s.Bind(c, "itest-s1", "http://b", time.Hour)
	if err != nil || grabbed {
		t.Fatalf("重复 Bind 不应抢到，got grabbed=%v err=%v", grabbed, err)
	}

	// Lookup → 命中 a
	if key, ok, _ := s.Lookup(c, "itest-s1", time.Hour); !ok || key != "http://a" {
		t.Fatalf("应命中 http://a，got %q ok=%v", key, ok)
	}

	// Unbind 是条件删除：指向别的后端时不动（否则会误删刚重建的绑定）
	if err := s.Unbind(c, "itest-s1", "http://other"); err != nil {
		t.Fatalf("Unbind(other): %v", err)
	}
	if _, ok, _ := s.Lookup(c, "itest-s1", time.Hour); !ok {
		t.Fatalf("Unbind 到别的后端不应删掉绑定")
	}

	// 指向本后端 → 删掉，并同步摘掉反向索引里的成员
	if err := s.Unbind(c, "itest-s1", "http://a"); err != nil {
		t.Fatalf("Unbind(a): %v", err)
	}
	if _, ok, _ := s.Lookup(c, "itest-s1", time.Hour); ok {
		t.Fatalf("Unbind 到本后端后应失效")
	}
	if member, _ := client.SIsMember(c, s.backendKey("http://a"), "itest-s1").Result(); member {
		t.Errorf("反向索引应已摘掉该 session")
	}

	// 重新绑定，后半段的 RemoveBackend 还要用
	if _, err := s.Bind(c, "itest-s1", "http://a", time.Hour); err != nil {
		t.Fatalf("re-bind: %v", err)
	}

	// RemoveBackend(a) → s1 失效
	if err := s.RemoveBackend(c, "http://a"); err != nil {
		t.Fatalf("RemoveBackend: %v", err)
	}
	if _, ok, _ := s.Lookup(c, "itest-s1", time.Hour); ok {
		t.Fatalf("RemoveBackend(a) 后 s1 应失效")
	}
}
