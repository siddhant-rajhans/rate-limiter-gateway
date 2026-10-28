package gateway

import (
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
	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/ratelimit"
)

func setupGateway(t *testing.T, cfg *config.Config) (*Gateway, *httptest.Server) {
	t.Helper()
	mr := miniredis.RunT(t)
	cfg.RedisAddr = mr.Addr()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	limiter := ratelimit.NewLimiter(client)
	gw := New(cfg, limiter)
	return gw, nil
}

func TestGatewayRateLimits(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()

	cfg := &config.Config{
		DefaultLimit:  3,
		DefaultWindow: time.Second,
		Routes: []config.RouteConfig{
			{Path: "/api/*", Backend: backend.URL, Limit: 3, Window: time.Second},
		},
	}
	gw, _ := setupGateway(t, cfg)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("X-API-Key", "client-1")
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-API-Key", "client-1")
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th request: expected 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
}

func TestRateLimitHeaders(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := &config.Config{
		DefaultLimit:  10,
		DefaultWindow: time.Minute,
		Routes: []config.RouteConfig{
			{Path: "/api/*", Backend: backend.URL, Limit: 10, Window: time.Minute},
		},
	}
	gw, _ := setupGateway(t, cfg)

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-API-Key", "headers-test")
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Header().Get("X-RateLimit-Limit") != "10" {
		t.Fatalf("expected X-RateLimit-Limit 10, got %s", rec.Header().Get("X-RateLimit-Limit"))
	}
	if rec.Header().Get("X-RateLimit-Remaining") != "9" {
		t.Fatalf("expected remaining 9, got %s", rec.Header().Get("X-RateLimit-Remaining"))
	}
}

func TestPerClientIsolation(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := &config.Config{
		DefaultLimit:  2,
		DefaultWindow: time.Second,
		Routes: []config.RouteConfig{
			{Path: "/api/*", Backend: backend.URL, Limit: 2, Window: time.Second},
		},
	}
	gw, _ := setupGateway(t, cfg)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("X-API-Key", "client-A")
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("client-A request %d should be allowed", i+1)
		}
	}
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-API-Key", "client-A")
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != 429 {
		t.Fatal("client-A 3rd request should be rate limited")
	}

	req = httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-API-Key", "client-B")
	rec = httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("client-B should not be affected by client-A's limit")
	}
}

func TestClientOverride(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := &config.Config{
		DefaultLimit:  2,
		DefaultWindow: time.Second,
		Routes: []config.RouteConfig{
			{Path: "/api/*", Backend: backend.URL},
		},
		ClientOverrides: []config.ClientOverride{
			{ClientID: "premium", Limit: 10, Window: time.Second},
		},
	}
	gw, _ := setupGateway(t, cfg)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/api/data", nil)
		req.Header.Set("X-API-Key", "premium")
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("premium client request %d should be allowed (has 10 limit)", i+1)
		}
	}
}

func TestConcurrentClientsGateway(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := &config.Config{
		DefaultLimit:  50,
		DefaultWindow: time.Second,
		Routes: []config.RouteConfig{
			{Path: "/api/*", Backend: backend.URL, Limit: 50, Window: time.Second},
		},
	}
	gw, _ := setupGateway(t, cfg)

	var allowed atomic.Int64
	var rejected atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/test", nil)
			req.Header.Set("X-API-Key", "concurrent-client")
			rec := httptest.NewRecorder()
			gw.ServeHTTP(rec, req)
			if rec.Code == 200 {
				allowed.Add(1)
			} else if rec.Code == 429 {
				rejected.Add(1)
			}
		}()
	}

	wg.Wait()

	if allowed.Load() != 50 {
		t.Fatalf("expected 50 allowed, got %d", allowed.Load())
	}
	if rejected.Load() != 50 {
		t.Fatalf("expected 50 rejected, got %d", rejected.Load())
	}
}

func TestNoBackendReturns502(t *testing.T) {
	cfg := &config.Config{
		DefaultLimit:  10,
		DefaultWindow: time.Second,
	}
	gw, _ := setupGateway(t, cfg)

	req := httptest.NewRequest("GET", "/unknown", nil)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
}
