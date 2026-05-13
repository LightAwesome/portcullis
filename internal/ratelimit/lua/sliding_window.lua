-- sliding_window.lua — atomic check-and-increment for the gateway's rate limiter.
--
-- Implements the sliding window log algorithm. State per (client, route)
-- is a Redis sorted set whose members are request timestamps (ms). On
-- each call:
--
--   1. Drop entries older than now - window_ms (they're outside the window).
--   2. Count remaining entries — these are the in-window requests so far.
--   3. If count < limit, append now and allow. Otherwise, deny.
--   4. Set TTL on the key so it auto-cleans when no requests are in flight.
--
-- Atomicity: Lua scripts run as a single Redis operation. While this script
-- executes, no other Redis command (from any client) runs. This is what
-- makes the check-then-act sequence race-free.
--
-- KEYS[1] = the sorted-set key, e.g. "rl:<client_id>:<route_prefix>"
-- ARGV[1] = now_ms          (number)
-- ARGV[2] = window_ms       (number)  e.g. 60000 for a one-minute window
-- ARGV[3] = max_requests    (number)  e.g. 100
--
-- Returns: { allowed, remaining, reset_ms }
--   allowed   — 1 if the request was admitted, 0 if denied
--   remaining — slots remaining in the current window after this call
--   reset_ms  — timestamp (ms) when the next slot frees up; useful for
--               Retry-After and X-RateLimit-Reset headers

local key = KEYS[1]
local now_ms = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local max_reqs = tonumber(ARGV[3])

local cutoff_ms = now_ms - window_ms

-- Step 1: drop entries before the window's start.
-- Range is inclusive on both ends; using cutoff_ms (not cutoff_ms - 1)
-- means an entry exactly at cutoff_ms is removed. Correct: an entry at
-- exactly the cutoff is now_ms - window_ms old, which is outside the
-- "last window_ms" interval.
redis.call("ZREMRANGEBYSCORE", key, 0, cutoff_ms)

-- Step 2: count what's left.
local count = redis.call("ZCARD", key)

if count < max_reqs then
	-- Allow. Record this request.
	-- Score and member are both now_ms. Members must be unique within a
	-- ZSET, so two requests at the exact same millisecond would collide.
	-- We append a small random suffix... no, actually, we don't. ZADD
	-- with the same score+member is idempotent: the second call doesn't
	-- add a new entry. To make each call distinct, we append a counter
	-- generated from the current count, ensuring uniqueness within
	-- this script invocation.
	--
	-- For correctness under millisecond collisions: each script call
	-- adds exactly one entry, with member = "now_ms:count". Two calls
	-- at the same now_ms produce different members because the count
	-- changed in between (we just incremented it conceptually).
	local member = tostring(now_ms) .. ":" .. tostring(count)
	redis.call("ZADD", key, now_ms, member)

	-- Step 4: TTL. Keep the key alive at least window_ms past now,
	-- so it's available for the next request but auto-cleans when idle.
	-- PEXPIRE takes ms.
	redis.call("PEXPIRE", key, window_ms + 1000)

	-- reset_ms: when the oldest entry will fall out of the window.
	-- If this was the only entry, that's now_ms + window_ms.
	-- If there were earlier entries, it's their score + window_ms.
	local oldest = redis.call("ZRANGE", key, 0, 0, "WITHSCORES")
	local reset_ms
	if #oldest >= 2 then
		reset_ms = tonumber(oldest[2]) + window_ms
	else
		-- Defensive fallback; should not be reachable since we just added one.
		reset_ms = now_ms + window_ms
	end

	local remaining = max_reqs - count - 1
	return { 1, remaining, reset_ms }
end

-- Deny. Compute reset_ms from the oldest entry.
local oldest = redis.call("ZRANGE", key, 0, 0, "WITHSCORES")
local reset_ms
if #oldest >= 2 then
	reset_ms = tonumber(oldest[2]) + window_ms
else
	reset_ms = now_ms + window_ms
end

-- Refresh TTL even on deny — keeps the window state alive while we're
-- under load. Without this, an idle period exactly at the deny moment
-- could let the key expire and reset the window.
redis.call("PEXPIRE", key, window_ms + 1000)

return { 0, 0, reset_ms }
