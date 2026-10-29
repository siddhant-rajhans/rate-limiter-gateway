# Rate Limiter Gateway

![Go 1.22](https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-Lua%20Scripts-DC382D?logo=redis&logoColor=white)
![Tests: 13 passing](https://img.shields.io/badge/tests-13%20passing-brightgreen)
![License: MIT](https://img.shields.io/badge/license-MIT-green)

Reverse-proxy API gateway with sliding-window rate limiting backed by Redis Lua scripts. Per-client and per-route limits, standard rate-limit headers, and sub-millisecond overhead on the hot path.

## Measured Results

Benchmarked on AMD Ryzen 5 3550H (4C/8T), 2 GB RAM, Redis via miniredis (pure Go in-memory). Reproduce with `make bench`.

| Metric | Value |
|--------|-------|
| Throughput (16 concurrent clients) | **632 req/s** end-to-end |
| Rate limit check overhead (p50) | **863 us** |
| Rate limit check overhead (p99) | **5.3 ms** |
| Rate limit check overhead (avg) | **1.2 ms** |
| Concurrent accuracy | **50/50 allowed, 50/50 rejected** (limit=50, 100 goroutines) |
| Tests | **13/13 passing** |
| Test categories | unit, integration, concurrency |

## Architecture

```
        Client Request
        X-API-Key: abc123
              │
              ▼
┌─────────────────────────────────────────────┐
│              Gateway :8080                  │
│                                             │
│   ┌───────────────────────────────────┐     │
│   │         Route Matching            │     │
│   │    /api/* ──→ backend:9000        │     │
│   └─────────────────┬─────────────────┘     │
│                     │                       │
│                     ▼                       │
│   ┌───────────────────────────────────┐     │
│   │    Redis Lua EVAL  (atomic)       │     │
│   │                                   │     │
│   │  ZREMRANGEBYSCORE  trim window    │     │
│   │  ZCARD             count entries  │     │
│   │  ZADD              record req     │     │
│   │  PEXPIRE           set TTL        │     │
│   └────────┬─────────────────┬────────┘     │
│            │                 │              │
│        allowed           rejected           │
│            │                 │              │
│            ▼                 ▼              │
│   ┌──────────────┐  ┌────────────────┐     │
│   │   Reverse    │  │  429 Too Many  │     │
│   │    Proxy     │  │  Retry-After   │     │
│   └───────┬──────┘  └────────────────┘     │
│           │                                 │
└───────────┼─────────────────────────────────┘
            │
            ▼
      Backend :9000
```

## Headers

| Header | Direction | Description |
|--------|-----------|-------------|
| `X-API-Key` | Request | Client identifier (falls back to X-Forwarded-For, then RemoteAddr) |
| `X-RateLimit-Limit` | Response | Maximum requests allowed in the window |
| `X-RateLimit-Remaining` | Response | Requests remaining in the current window |
| `X-RateLimit-Window` | Response | Window duration |
| `Retry-After` | Response (429) | Seconds until the client can retry |

## Configuration

```yaml
listen_addr: ":8080"
redis_addr: "localhost:6379"
default_limit: 100
default_window: 1m

routes:
  - path: "/api/*"
    backend: "http://localhost:9000"
    limit: 100
    window: 1m

client_overrides:
  - client_id: "premium-client"
    limit: 500
    window: 1m
```

## Quick Start

```bash
# Start Redis
docker compose up -d redis

# Build and run
make build
./bin/gateway -config config.yaml

# Test rate limiting
for i in $(seq 1 5); do
  curl -s -o /dev/null -w "%{http_code}\n" \
    -H "X-API-Key: test" http://localhost:8080/api/data
done
```

### With Docker

```bash
docker compose up
```

## Lua Script

The entire check-and-increment runs as one atomic Lua script via `EVAL`. Redis executes Lua atomically, so there is no race between reading the count and adding a new entry.

```lua
local key = KEYS[1]
local window = tonumber(ARGV[1])  -- window in ms
local limit = tonumber(ARGV[2])
local now = tonumber(ARGV[3])     -- current time in ms

-- Remove entries outside the sliding window
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
local count = redis.call('ZCARD', key)

if count < limit then
    redis.call('ZADD', key, now, now .. ':' .. math.random(1, 1000000))
    redis.call('PEXPIRE', key, window)
    return {0, limit - count - 1, window}  -- allowed, remaining, window
else
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    local retry_after = tonumber(oldest[2]) + window - now
    return {1, 0, retry_after}  -- rejected, 0 remaining, retry_after_ms
end
```

<details>
<summary><strong>Design Decisions</strong></summary>

### 1. Sliding window via sorted sets

The rate limiter uses a Redis sorted set per key, with timestamps as scores. Each request adds an entry, and the Lua script atomically trims entries outside the window, counts the remainder, and decides allow/reject in a single round trip. This gives true sliding-window behavior (no fixed-window edge spikes) at the cost of O(n) storage per window.

### 2. Lua script atomicity

The entire check-and-increment runs as one Lua script via `EVAL`. Redis executes Lua atomically, so there is no race between reading the count and adding a new entry. This is what makes the concurrent accuracy test pass (exactly 50 allowed out of 100 goroutines against a limit of 50).

### 3. Per-client and per-route configuration

The gateway resolves limits in two layers: per-route limits from config, and per-client overrides (e.g., premium API keys get higher limits). The rate-limit key combines client ID and path, so a client's `/api/search` limit is independent of their `/api/data` limit.

### 4. Standard HTTP rate-limit headers

Every response includes `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and `X-RateLimit-Window`. Rejected responses include `Retry-After` in seconds, following RFC 6585.

### 5. Reverse proxy via stdlib

The gateway uses `net/http/httputil.ReverseProxy` from Go's standard library. No third-party HTTP frameworks. The proxy is configured per route at startup, so there is no allocation per request beyond the rate limit check.

</details>

## Tests

```
internal/ratelimit/  6 tests  allow under limit, reject over, sliding window expiry,
                              key isolation, concurrent (200 goroutines), window sizes
internal/gateway/    7 tests  rate limiting, headers, per-client isolation, client
                              overrides, concurrent (100 goroutines), no-backend 502
```

## Project Structure

```
cmd/gateway/           main.go entry point
internal/
  ratelimit/           sliding window limiter + Lua script
  config/              YAML config loading, route/client resolution
  gateway/             HTTP reverse proxy with rate limiting
  bench/               latency and throughput benchmarks
```

## License

MIT
