package gw

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestRedisSticky: an integration test of Lookup/Bind/RemoveBackend against a real redis.
// It is skipped when there is no redis locally (it only runs where redis is configured, in production / CI).
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

	// Not bound → miss
	if _, ok, _ := s.Lookup(c, "itest-s1", time.Hour); ok {
		t.Fatalf("an unbound session must not hit")
	}

	// First grab → won
	grabbed, err := s.Bind(c, "itest-s1", "http://a", time.Hour)
	if err != nil || !grabbed {
		t.Fatalf("the first Bind should win, got grabbed=%v err=%v", grabbed, err)
	}

	// Repeated grab (different backend) → not won, the original backend is kept
	grabbed, err = s.Bind(c, "itest-s1", "http://b", time.Hour)
	if err != nil || grabbed {
		t.Fatalf("a repeated Bind must not win, got grabbed=%v err=%v", grabbed, err)
	}

	// Lookup → hits a
	if key, ok, _ := s.Lookup(c, "itest-s1", time.Hour); !ok || key != "http://a" {
		t.Fatalf("should hit http://a, got %q ok=%v", key, ok)
	}

	// Unbind is a conditional delete: pointing at another backend leaves it alone (otherwise it would delete a
	// binding that was just re-created)
	if err := s.Unbind(c, "itest-s1", "http://other"); err != nil {
		t.Fatalf("Unbind(other): %v", err)
	}
	if _, ok, _ := s.Lookup(c, "itest-s1", time.Hour); !ok {
		t.Fatalf("Unbind against another backend must not delete the binding")
	}

	// Pointing at this backend → deleted, and the reverse index member is dropped along with it
	if err := s.Unbind(c, "itest-s1", "http://a"); err != nil {
		t.Fatalf("Unbind(a): %v", err)
	}
	if _, ok, _ := s.Lookup(c, "itest-s1", time.Hour); ok {
		t.Fatalf("should be gone after Unbind against this backend")
	}
	if member, _ := client.SIsMember(c, s.backendKey("http://a"), "itest-s1").Result(); member {
		t.Errorf("the reverse index should have dropped this session")
	}

	// Bind again: the RemoveBackend below still needs it
	if _, err := s.Bind(c, "itest-s1", "http://a", time.Hour); err != nil {
		t.Fatalf("re-bind: %v", err)
	}

	// RemoveBackend(a) → s1 is gone
	if err := s.RemoveBackend(c, "http://a"); err != nil {
		t.Fatalf("RemoveBackend: %v", err)
	}
	if _, ok, _ := s.Lookup(c, "itest-s1", time.Hour); ok {
		t.Fatalf("s1 should be gone after RemoveBackend(a)")
	}
}
