package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"

	"github.com/redis/go-redis/v9"
	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/config"
	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/gateway"
	"github.com/siddhant-rajhans/rate-limiter-gateway/internal/ratelimit"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	limiter := ratelimit.NewLimiter(client)
	gw := gateway.New(cfg, limiter)

	slog.Info("starting gateway", "addr", cfg.ListenAddr, "routes", len(cfg.Routes))
	if err := http.ListenAndServe(cfg.ListenAddr, gw); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
