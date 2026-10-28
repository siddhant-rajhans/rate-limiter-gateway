package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*Limiter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewLimiter(client), mr
}

func TestAllowUnderLimit(t *testing.T) {
	lim, _ := setup(t)
	cfg := Config{Limit: 5, Window: time.Second}

	for i := 0; i < 5; i++ {
		res, err := lim.Allow(context.Background(), "user:1", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
		if res.Remaining != 5-i-1 {
			t.Fatalf("expected remaining %d, got %d", 5-i-1, res.Remaining)
		}
	}
}

func TestRejectOverLimit(t *testing.T) {
	lim, _ := setup(t)
	cfg := Config{Limit: 3, Window: time.Second}

	for i := 0; i < 3; i++ {
		res, err := lim.Allow(context.Background(), "user:2", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	res, err := lim.Allow(context.Background(), "user:2", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed {
		t.Fatal("4th request should be rejected")
	}
	if res.RetryAfter <= 0 {
		t.Fatal("RetryAfter should be positive")
	}
}

func TestSlidingWindowExpiry(t *testing.T) {
	lim, mr := setup(t)
	cfg := Config{Limit: 2, Window: time.Second}

	for i := 0; i < 2; i++ {
		_, err := lim.Allow(context.Background(), "user:3", cfg)
		if err != nil {
			t.Fatal(err)
		}
	}

	res, _ := lim.Allow(context.Background(), "user:3", cfg)
	if res.Allowed {
		t.Fatal("should be rejected before window expires")
	}

	mr.FastForward(time.Second + time.Millisecond)

	res, err := lim.Allow(context.Background(), "user:3", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Allowed {
		t.Fatal("should be allowed after window expires")
	}
}

func TestIsolationBetweenKeys(t *testing.T) {
	lim, _ := setup(t)
	cfg := Config{Limit: 1, Window: time.Second}

	res, _ := lim.Allow(context.Background(), "user:a", cfg)
	if !res.Allowed {
		t.Fatal("user:a first request should be allowed")
	}
	res, _ = lim.Allow(context.Background(), "user:a", cfg)
	if res.Allowed {
		t.Fatal("user:a second request should be rejected")
	}

	res, _ = lim.Allow(context.Background(), "user:b", cfg)
	if !res.Allowed {
		t.Fatal("user:b first request should be allowed (isolated)")
	}
}

func TestConcurrentRequests(t *testing.T) {
	lim, _ := setup(t)
	cfg := Config{Limit: 100, Window: time.Second}

	var allowed atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := lim.Allow(context.Background(), "user:concurrent", cfg)
			if err != nil {
				t.Error(err)
				return
			}
			if res.Allowed {
				allowed.Add(1)
			}
		}()
	}

	wg.Wait()

	if allowed.Load() != 100 {
		t.Fatalf("expected exactly 100 allowed, got %d", allowed.Load())
	}
}

func TestDifferentWindowSizes(t *testing.T) {
	lim, mr := setup(t)

	shortCfg := Config{Limit: 5, Window: 500 * time.Millisecond}
	longCfg := Config{Limit: 10, Window: 5 * time.Second}

	for i := 0; i < 5; i++ {
		lim.Allow(context.Background(), "short", shortCfg)
	}
	res, _ := lim.Allow(context.Background(), "short", shortCfg)
	if res.Allowed {
		t.Fatal("short window should be exhausted")
	}

	mr.FastForward(600 * time.Millisecond)
	res, _ = lim.Allow(context.Background(), "short", shortCfg)
	if !res.Allowed {
		t.Fatal("short window should have reset")
	}

	for i := 0; i < 10; i++ {
		lim.Allow(context.Background(), "long", longCfg)
	}
	mr.FastForward(600 * time.Millisecond)
	res, _ = lim.Allow(context.Background(), "long", longCfg)
	if res.Allowed {
		t.Fatal("long window should still be active")
	}
}
