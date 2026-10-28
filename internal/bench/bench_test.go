package bench

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/config"
	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/gateway"
	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/ratelimit"
)

func BenchmarkRateLimitCheck(b *testing.B) {
	mr := miniredis.RunT(b)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	b.Cleanup(func() { client.Close() })

	limiter := ratelimit.NewLimiter(client)
	cfg := ratelimit.Config{Limit: 1000000, Window: time.Minute}
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("bench:%d", i%100)
			limiter.Allow(ctx, key, cfg)
			i++
		}
	})
}

func BenchmarkGatewayE2E(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	mr := miniredis.RunT(b)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	b.Cleanup(func() { client.Close() })

	cfg := &config.Config{
		DefaultLimit:  1000000,
		DefaultWindow: time.Minute,
		Routes: []config.RouteConfig{
			{Path: "/api/*", Backend: backend.URL, Limit: 1000000, Window: time.Minute},
		},
	}
	limiter := ratelimit.NewLimiter(client)
	gw := gateway.New(cfg, limiter)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			req := httptest.NewRequest("GET", "/api/test", nil)
			req.Header.Set("X-API-Key", fmt.Sprintf("client-%d", i%50))
			rec := httptest.NewRecorder()
			gw.ServeHTTP(rec, req)
			i++
		}
	})
}

func TestLatencyOverhead(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	cfg := &config.Config{
		DefaultLimit:  100000,
		DefaultWindow: time.Minute,
		Routes: []config.RouteConfig{
			{Path: "/api/*", Backend: backend.URL, Limit: 100000, Window: time.Minute},
		},
	}
	limiter := ratelimit.NewLimiter(client)
	gw := gateway.New(cfg, limiter)

	// Warm up
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("X-API-Key", fmt.Sprintf("warmup-%d", i%10))
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, req)
	}

	n := 1000
	latencies := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("X-API-Key", fmt.Sprintf("latency-%d", i%50))
		rec := httptest.NewRecorder()
		start := time.Now()
		gw.ServeHTTP(rec, req)
		latencies[i] = time.Since(start)
	}

	var total time.Duration
	for _, l := range latencies {
		total += l
	}
	avg := total / time.Duration(n)

	// Sort for percentiles
	for i := 0; i < len(latencies); i++ {
		for j := i + 1; j < len(latencies); j++ {
			if latencies[j] < latencies[i] {
				latencies[i], latencies[j] = latencies[j], latencies[i]
			}
		}
	}
	p50 := latencies[n/2]
	p99 := latencies[n*99/100]

	t.Logf("Latency overhead (1000 requests):")
	t.Logf("  avg: %v", avg)
	t.Logf("  p50: %v", p50)
	t.Logf("  p99: %v", p99)

	// Throughput test: concurrent
	var ops atomic.Int64
	var wg sync.WaitGroup
	done := make(chan struct{})
	workers := 16

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-done:
					return
				default:
				}
				req := httptest.NewRequest("GET", "/api/test", nil)
				req.Header.Set("X-API-Key", fmt.Sprintf("throughput-%d-%d", wid, i%10))
				rec := httptest.NewRecorder()
				gw.ServeHTTP(rec, req)
				ops.Add(1)
				i++
			}
		}(w)
	}

	time.Sleep(3 * time.Second)
	close(done)
	wg.Wait()

	throughput := float64(ops.Load()) / 3.0
	t.Logf("Throughput (16 workers, 3s): %.0f req/s", throughput)
}
