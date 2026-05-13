package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/LightAwesome/portcullis/internal/ratelimit/luascript"
)

// RateLimitResult is the outcome of a single CheckRateLimit call.
//
// Allowed indicates whether the request fits in the current window.
// Remaining is the number of slots left after this call (0 on deny).
// ResetMS is the unix-ms timestamp when the next slot will free up,
// useful for Retry-After and X-RateLimit-Reset headers.
type RateLimitResult struct {
	Allowed   bool
	Remaining int64
	ResetMS   int64
}

// slidingWindowScript is the embedded Lua compiled into a redis.Script.
// Using *redis.Script gives us the EVALSHA-then-EVAL fallback automatically:
// the first call EVALs and caches; subsequent calls EVALSHA against the
// cached SHA; if Redis ever evicts the script (cluster failover, FLUSHALL),
// the next call falls back to EVAL transparently.
//
// Constructed once at package init time. Safe for concurrent use.
var slidingWindowScript = redis.NewScript(luascript.SlidingWindow)

// CheckRateLimit performs an atomic check-and-increment against the
// sliding-window log stored in Redis.
//
// Parameters:
//   - clientID: the *store.Client.ID as a string (we accept a string here
//     to keep this layer pure; the caller is responsible for stringifying
//     pgtype.UUID).
//   - routePrefix: the route's URL prefix.
//   - maxRequests: the allowed count within the window.
//   - windowSeconds: the window duration.
//   - nowMS: caller-provided current time in ms. Pass time.Now().UnixMilli()
//     in production; tests pass a fixed value for determinism.
//
// Returns the result of the check. Errors here indicate Redis-layer
// failures, not rate-limit denial.
func (s *Store) CheckRateLimit(
	ctx context.Context,
	clientID, routePrefix string,
	maxRequests, windowSeconds, nowMS int64,
) (*RateLimitResult, error) {
	key := rateLimitKey(clientID, routePrefix)
	windowMS := windowSeconds * 1000

	raw, err := slidingWindowScript.Run(
		ctx,
		s.redis,
		[]string{key},
		nowMS, windowMS, maxRequests,
	).Result()
	if err != nil {
		return nil, fmt.Errorf("rate limit script: %w", err)
	}

	arr, ok := raw.([]any)
	if !ok || len(arr) != 3 {
		return nil, fmt.Errorf("rate limit script: unexpected return shape %T %v", raw, raw)
	}

	allowed, ok1 := arr[0].(int64)
	remaining, ok2 := arr[1].(int64)
	resetMS, ok3 := arr[2].(int64)
	if !ok1 || !ok2 || !ok3 {
		return nil, fmt.Errorf("rate limit script: non-numeric return values")
	}

	return &RateLimitResult{
		Allowed:   allowed == 1,
		Remaining: remaining,
		ResetMS:   resetMS,
	}, nil
}

// preloadRateLimitScript registers the script with Redis on startup so
// the first request doesn't pay the NOSCRIPT round trip. Idempotent: if
// the script is already loaded, the SHA is returned but nothing changes.
//
// Failure to preload is non-fatal; the redis.Script.Run path handles
// uncached scripts transparently. We log (when we have logging in P3.1)
// and continue.
func (s *Store) preloadRateLimitScript(ctx context.Context) error {
	return slidingWindowScript.Load(ctx, s.redis).Err()
}

// rateLimitKey builds the sorted-set key used for one (client, route) pair.
//
// Format: "rl:<client_id>:<route_prefix>"
// Exported as a const-shaped function so middleware in another package
// can construct the same key for debugging or admin tooling.
func rateLimitKey(clientID, routePrefix string) string {
	return "rl:" + clientID + ":" + routePrefix
}
