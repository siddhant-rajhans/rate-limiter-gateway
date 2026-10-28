package gateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/config"
	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/ratelimit"
)

type Gateway struct {
	cfg     *config.Config
	limiter *ratelimit.Limiter
	proxies map[string]*httputil.ReverseProxy
}

func New(cfg *config.Config, limiter *ratelimit.Limiter) *Gateway {
	g := &Gateway{
		cfg:     cfg,
		limiter: limiter,
		proxies: make(map[string]*httputil.ReverseProxy),
	}
	for _, r := range cfg.Routes {
		target, err := url.Parse(r.Backend)
		if err != nil {
			slog.Error("invalid backend URL", "route", r.Path, "backend", r.Backend, "error", err)
			continue
		}
		g.proxies[r.Path] = httputil.NewSingleHostReverseProxy(target)
	}
	return g
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	clientID := extractClientID(r)
	routeCfg := g.cfg.RouteFor(r.URL.Path)

	limit, window := g.cfg.ClientLimit(clientID)
	if routeCfg != nil && routeCfg.Limit > 0 {
		limit = routeCfg.Limit
		window = routeCfg.Window
	}

	key := fmt.Sprintf("%s:%s", clientID, r.URL.Path)
	result, err := g.limiter.Allow(r.Context(), key, ratelimit.Config{
		Limit:  limit,
		Window: window,
	})
	if err != nil {
		slog.Error("rate limiter error", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
	w.Header().Set("X-RateLimit-Window", window.String())

	if !result.Allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(result.RetryAfter.Seconds())+1))
		w.Header().Set("X-RateLimit-Remaining", "0")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		slog.Info("rate limited",
			"client", clientID,
			"path", r.URL.Path,
			"retry_after", result.RetryAfter,
		)
		return
	}

	if routeCfg != nil {
		proxy, ok := g.proxies[routeCfg.Path]
		if ok {
			proxy.ServeHTTP(w, r)
			slog.Info("proxied",
				"client", clientID,
				"path", r.URL.Path,
				"backend", routeCfg.Backend,
				"latency", time.Since(start),
			)
			return
		}
	}

	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, "no backend for %s", r.URL.Path)
}

func extractClientID(r *http.Request) string {
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}
