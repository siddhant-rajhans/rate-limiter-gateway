package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// sliding window rate limiter backed by Redis Lua scripts
var slidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local window = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local cutoff = now - window

redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)

if count < limit then
    redis.call('ZADD', key, now, now .. ':' .. math.random(1, 1000000))
    redis.call('PEXPIRE', key, window)
    return {0, limit - count - 1, window}
else
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    local retry_after = 0
    if #oldest >= 2 then
        retry_after = tonumber(oldest[2]) + window - now
    end
    return {1, 0, retry_after}
end
`)

type Result struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}

type Config struct {
	Limit  int
	Window time.Duration
}

type Limiter struct {
	client *redis.Client
}

func NewLimiter(client *redis.Client) *Limiter {
	return &Limiter{client: client}
}

func (l *Limiter) Allow(ctx context.Context, key string, cfg Config) (Result, error) {
	now := time.Now().UnixMilli()
	windowMs := cfg.Window.Milliseconds()

	vals, err := slidingWindowScript.Run(ctx, l.client, []string{fmt.Sprintf("rl:%s", key)},
		windowMs, cfg.Limit, now,
	).Int64Slice()
	if err != nil {
		return Result{}, fmt.Errorf("rate limit script: %w", err)
	}

	if vals[0] == 0 {
		return Result{
			Allowed:   true,
			Remaining: int(vals[1]),
		}, nil
	}

	return Result{
		Allowed:    false,
		Remaining:  0,
		RetryAfter: time.Duration(vals[2]) * time.Millisecond,
	}, nil
}
