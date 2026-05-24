# Portcullis

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://golang.org)
[![CI](https://github.com/LightAwesome/portcullis/actions/workflows/ci.yml/badge.svg)](https://github.com/LightAwesome/portcullis/actions/workflows/ci.yml)
[![Live](https://img.shields.io/badge/live-portcullis.duckdns.org-success)](https://portcullis.duckdns.org/health)
[![Docker](https://img.shields.io/badge/Docker-multi--stage-2496ED?logo=docker&logoColor=white)](Dockerfile)
[![Postgres](https://img.shields.io/badge/Postgres-16-336791?logo=postgresql&logoColor=white)](https://www.postgresql.org/)
[![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white)](https://redis.io/)
[![Prometheus](https://img.shields.io/badge/Prometheus-metrics-E6522C?logo=prometheus&logoColor=white)](https://prometheus.io/)

A self-hosted API gateway written in Go. Sits in front of external APIs (OpenAI, Anthropic, Stripe, etc.) and provides authentication, rate limiting, request logging, and circuit breaking — without sending traffic through a third-party gateway service.

**Live demo:** [`https://portcullis.duckdns.org/health`](https://portcullis.duckdns.org/health) — returns `{"status":"the gate stands","postgres":"ok","redis":"ok"}` from a free-tier Oracle Cloud VM.

---

## What it does

Portcullis is the kind of thing you'd reach for when you have several side projects all hitting the same paid APIs and you're tired of:

- Copy-pasting API keys into every project's `.env`
- Eyeballing your OpenAI dashboard wondering which project burned through your credits
- Having no per-project rate limits, so one runaway script costs you real money
- Discovering an API outage by watching three projects fail at the same time

Instead, you point your projects at Portcullis with a single gateway key. Portcullis holds your real upstream secrets (encrypted at rest), enforces per-client rate limits, logs every request, and shields you when upstreams misbehave.

It's kinda what services like Kong or Apigee do, scoped down to one person's needs and self-hosted. Basically for hackathons, small projects and throwaways.

---

## Architecture


A request lifecycle:

1. Client sends `GET /proxy/openai/v1/chat/completions` with header `X-Gateway-Key: pck_<id>_<secret>`.
2. **Nginx** (port 443) terminates TLS and proxies plain HTTP to localhost:8080.
3. **Portcullis gateway** runs the request through a middleware chain:
   - `recover` — converts panics to 500s
   - `request-id` — assigns a correlation ID, surfaced via `X-Request-ID`
   - `logger` — structured access log
   - `metrics` — increments Prometheus counters
   - `auth` — verifies the gateway key against `clients` table (HMAC-SHA256, peppered)
   - `chronicle` — non-blocking async write to `request_logs`
   - `ratelimit` — sliding-window check in Redis (Lua script for atomicity)
   - `circuit-breaker` — short-circuits if the route's upstream is failing
4. **Proxy handler** looks up the route, decrypts the upstream secret (AES-256-GCM), and forwards via `httputil.ReverseProxy` with `Authorization: Bearer <decrypted secret>`.
5. **Upstream API** returns a response. The proxy's `ModifyResponse` callback feeds the status code back into the circuit breaker.
6. Response streams back to the client. The chronicle middleware writes the request_log entry asynchronously after the response is sent.

---

## Key features

- **Authentication** via gateway keys with format `pck_<key_id>_<secret>`. Secrets are stored as HMAC-SHA256(secret, pepper). Constant-time comparison.
- **At-rest encryption** of upstream API keys via AES-256-GCM. Master key lives in `PORTCULLIS_MASTER_KEY` (env var); plaintext upstream secrets never touch the database.
- **Sliding-window rate limiting** in Redis. Per (client, route) policies. Atomic via a Lua script — no race conditions under concurrent traffic.
- **Async request logging** with a bounded internal queue. Workers batch-insert into Postgres. The proxy never blocks on chronicle writes; if the queue is full, the log entry is dropped (logged as a warning) and the request proceeds.
- **Per-route circuit breaker.** Closed → open after 5 consecutive failures within 60s. Half-open probe after 30s cooldown. Exactly one probe allowed during half-open (concurrency-safe via `probeInFlight` flag, verified by a 100-goroutine race test).
- **Prometheus metrics** for everything: request counts, latencies, rate-limit hits, circuit-breaker state changes, short-circuits, upstream errors by category.
- **Themed observability.** The codebase uses a coherent castle metaphor — gateway is "the gate," upstreams are "keeps," clients are "garrisons," rate limiter is "the portcullis," request log is "the chronicle." Error responses are themed for humans but include machine-readable `code` fields for scripts. The theming is deliberate flavor, not affectation; everyone reading the code sees the same vocabulary.

---

## Tech stack

| Layer | Choice | Why |
|---|---|---|
| Language | Go 1.26 | Single static binary, fast cold-start, mature net/http |
| HTTP router | [Chi](https://github.com/go-chi/chi) | Minimal, idiomatic, no global state |
| Database | Postgres 16 | Boring, reliable, JSON columns for flexible policy storage |
| Database driver | [pgx/v5](https://github.com/jackc/pgx) | Native Postgres protocol, connection pooling |
| Cache | Redis 7 | Lua scripting for atomic rate-limit ops |
| Migrations | [golang-migrate](https://github.com/golang-migrate/migrate) | SQL files, version-controlled, runs as a one-shot |
| Metrics | Prometheus client | Standard, scrape-based, integrates with Grafana |
| TLS edge | Nginx + Certbot | Let's Encrypt with auto-renewal via systemd timer |
| Container | Multi-stage Docker (`FROM scratch`) | 21 MB static binary, no shell, minimal attack surface |
| CI | GitHub Actions | gofmt → vet → golangci-lint → test (-race) → docker build |
| Deployment | Oracle Cloud Always Free (1 OCPU, 1 GB RAM) | Permanent free tier, real public IP, real TLS |

---

## How it works under the hood

### Rate limiting

The sliding-window-log algorithm, implemented as a Lua script that runs atomically in Redis:

```lua
-- Pseudocode of the actual script
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])

redis.call('ZREMRANGEBYSCORE', KEYS[1], 0, now - window)
local count = redis.call('ZCARD', KEYS[1])
if count < limit then
    redis.call('ZADD', KEYS[1], now, now)
    redis.call('EXPIRE', KEYS[1], window)
    return 1  -- allowed
end
return 0  -- denied
```

A sorted set per (client, route) holds timestamps of recent requests. On each request: trim entries older than `now - window`, count, and either add or reject. The entire sequence runs as one Redis command, so two concurrent requests can't both squeak past the limit.

Alternatives considered: token bucket (smoother but inaccurate under bursts), fixed-window counter (cheap but allows ~2x burst at window boundaries), GCRA (better accuracy than sliding-window-log but more complex). Sliding-window-log is the right balance for a personal gateway — accurate, simple, and traffic is low enough that the per-request memory cost is irrelevant.

### Circuit breaker

A three-state machine per route:

```
        failures ≥ 5 in 60s
   ┌──────────────────────┐
   ▼                      │
[ CLOSED ] ────────────► [ OPEN ]
   ▲                      │
   │ probe succeeds       │ 30s elapsed
   │                      ▼
   └──────────────── [ HALF-OPEN ]
            probe fails ↻ back to OPEN
```

The half-open state is the interesting part. Naively, you'd just go open → closed after cooldown and let all queued traffic resume. But if the upstream is still down, that "thundering herd" hits the upstream simultaneously and worsens the outage. So half-open allows *exactly one* request through to probe whether recovery has happened. If yes, close. If no, reopen.

Implementation detail: a `probeInFlight` boolean, protected by the breaker's mutex. When `Allow()` is called in half-open state and `probeInFlight` is false, set it to true and return true. All other concurrent callers see `probeInFlight=true` and get false back. When the proxy reports success or failure, `probeInFlight` clears.

Verified by a unit test that fires 100 concurrent `Allow()` calls in half-open state under the `-race` detector and asserts exactly one returns true. That's the kind of test that's worth more than a thousand lines of "happy path" coverage.

### At-rest encryption

Upstream API keys are sensitive enough that a Postgres backup leak shouldn't expose them. So they're encrypted with AES-256-GCM before insert:

```go
nonce := make([]byte, 12)
io.ReadFull(rand.Reader, nonce)
ciphertext := aead.Seal(nil, nonce, plaintext, nil)
// Stored: nonce || ciphertext (concatenated)
```

Master key is 32 bytes, base64-encoded in `PORTCULLIS_MASTER_KEY`. The gateway decrypts per-request — no plaintext caching. If you rotate the master key, you re-encrypt; if you lose it, the existing routes become un-routable (intentional — the data is meaningless without the key).

### Async request logging

The chronicle middleware can't block the request path — every API call would pay a Postgres write latency. So:

```go
// Inside the middleware
select {
case worker.queue <- entry:
    // Enqueued. Worker will batch-insert.
default:
    // Queue full. Log a warning and drop this entry.
    log.Warn("chronicle queue full, dropping entry")
}
```

A dedicated goroutine drains the queue, batches inserts (up to 100 entries or 1s of latency), and writes to Postgres. The drop-on-full pattern means request latency stays bounded even if Postgres is slow. The trade-off: under sustained overload, some request_log entries are lost. That's acceptable for a non-critical observability table.

---

## Repository structure

```
portcullis/
├── cmd/portcullis/         # CLI entry point (cobra)
├── internal/
│   ├── breaker/            # Circuit breaker package
│   ├── chronicle/          # Async request logging
│   ├── config/             # Env-var configuration
│   ├── crypto/             # AES-256-GCM encryption helpers
│   ├── httpx/              # Shared HTTP utilities (error responses, request IDs)
│   ├── logging/            # Structured logger + async worker
│   ├── metrics/            # Prometheus collectors
│   ├── proxy/              # Reverse proxy handler
│   ├── ratelimit/          # Sliding-window rate limit middleware + Lua script
│   ├── server/             # Routes wiring, middleware chain
│   ├── store/              # Postgres queries
│   └── testutil/           # Shared test infrastructure (testcontainers)
├── migrations/             # SQL schema migrations
├── scripts/                # Seed scripts, dev helpers
├── docker-compose.yml      # Local dev stack
├── Dockerfile              # Multi-stage build, FROM scratch
└── Makefile                # Common dev tasks
```

---

## Running locally

```bash
# 1. Clone and enter
git clone https://github.com/LightAwesome/portcullis.git
cd portcullis

# 2. Copy env template
cp .env.example .env
# Edit .env: set PORTCULLIS_ADMIN_KEY and PORTCULLIS_MASTER_KEY to real values.
# Generate fresh ones with:
#   openssl rand -hex 32                     # admin key, key pepper
#   openssl rand -base64 32                  # master key (32 bytes for AES-256)

# 3. Start the stack
make up                                       # postgres + redis
make migrate-up                               # apply schema
make dev                                      # gateway on :8080 (foreground)

# 4. Seed a test client and route (in another terminal)
make seed
# Prints a gateway key — save it.

# 5. Send a request through the gateway
curl -H "X-Gateway-Key: pck_..." http://localhost:8080/proxy/httpbin/get
```

### Common operations

```bash
# Tests
go test -race ./...

# Lint (golangci-lint v2.12.x required)
golangci-lint run ./...

# Build the production binary
go build -o bin/portcullis ./cmd/portcullis

# Wipe and restart
make nuke && make up && make migrate-up && make seed
```

---

## Deployment

The live demo at `portcullis.duckdns.org` runs on Oracle Cloud Always Free (E2.1.Micro, 1 OCPU, 1 GB RAM, x86_64, San Jose region). The deploy stack:

- **Docker Compose** for the Portcullis + Postgres + Redis trio
- **Nginx** as the TLS-terminating reverse proxy on 443
- **Certbot** + **Let's Encrypt** for the certificate, with auto-renewal via systemd timer
- **DuckDNS** for the public hostname
- **UFW** + **Oracle Security Lists** as layered firewalls (only 22/80/443 open)
- **fail2ban** for SSH brute-force protection
- **2 GB swap** because 1 GB of RAM is uncomfortably tight during the in-VM Docker build

Total monthly cost: **$0**. The deploy survives reboots; the certificate renews itself; the gateway restarts automatically if anything crashes (via Docker's `restart: unless-stopped`).

A few things I learned the hard way during the deploy session:

- **Layered firewalls are a footgun.** Opening port 80 in UFW does nothing if Oracle's cloud-level Security List still blocks it. The symptom is "connection timed out" with no indication of which layer dropped the packet.
- **`/bin/sh` on Ubuntu is `dash`, not `bash`.** Makefile targets using `source .env` work on macOS but fail on the VM. The portable form is `. ./.env` (POSIX dot, with explicit `./` because dash searches PATH, not cwd).
- **The 1 GB RAM Oracle micro shape OOM-kills the Go compiler** without swap. Add a 2 GB swap file before any in-VM `docker build`.
- **fail2ban's defaults are aggressive.** Install it *with* a `jail.local` whitelisting your home IP, or you'll lock yourself out within minutes of enabling it. Recovering requires Oracle's web serial console, which is the kind of thing you only want to use once.

---

## What's deferred

Honest list of work that's intentionally not done:

- **Per-route circuit breaker configuration.** Thresholds are hardcoded (5 failures / 60s / 30s cooldown). A real production deploy would expose these per-route in the `upstream_routes` table.
- **Streaming response support.** Server-Sent Events / NDJSON from LLM APIs work in the basic case (Nginx `proxy_buffering off`, Go `httputil.ReverseProxy` flushes on its own), but there's no explicit streaming-aware testing or chronicle handling.
- **Multi-instance state.** Circuit breaker state is in-memory per process. If scaled to multiple gateway pods behind a load balancer, each pod would have independent breaker state. Redis-backed state would be the next evolution.
- **A frontend dashboard.** Grafana already provides observability dashboards over the gateway's `/metrics` endpoint, so a custom React UI would mostly duplicate that.
- **CLI verbs beyond `raise` and `siege`.** Things like `portcullis garrison list` are not implemented; you query Postgres directly or hit the admin API with curl. The admin API itself is complete; the CLI is a thin layer that hasn't been built out.
- **A real migration story.** Portcullis hasn't replaced an existing API integration in a personal project — it's a portfolio piece designed and built independently. Benchmarks below are synthetic.

---

## Benchmarks

> _TODO: load test results from `siege` on the deployed VM. Will include p50/p95/p99 latency, max sustained RPS, and behaviour under failure injection._

---

## Design rationale

Some choices that aren't obvious from reading the code:

**HMAC-SHA256 over bcrypt for gateway keys.** Gateway keys are checked on every request — a bcrypt verification is ~100ms by design, and the gateway would spend more time hashing than proxying. HMAC-SHA256 with a pepper gives essentially the same security guarantee (you can't reverse it without the pepper) at ~microseconds per check. The pepper is in environment, not in the database, so a DB leak alone doesn't enable offline cracking.

**Async chronicle, not synchronous.** Every API call writes one row to `request_logs`. A synchronous insert would tie request latency to Postgres latency. A bounded async queue keeps request latency unaffected by chronicle load, with the explicit trade-off that some log entries can be dropped under sustained overload.

**Sliding-window-log via Lua, not via application code.** Sending three round-trips to Redis (read count, check limit, increment) leaves a window where two concurrent requests both pass the check. A single Lua script runs atomically in Redis — no race condition possible.

**Per-route circuit breaker, not global.** OpenAI being down shouldn't open the breaker for Anthropic. Each route prefix gets its own independent breaker state, stored in a `sync.Map[prefix]*Breaker` with lazy initialization via `LoadOrStore` (handles the construction race when two goroutines first hit a new route simultaneously).

**`FROM scratch` Docker image.** No shell, no package manager, no `/etc/passwd`. The container has exactly one file (the binary) plus the certs bundle. Attack surface is whatever's in your binary, nothing else. Image size: ~21 MB.

---

## License

MIT — see [LICENSE](LICENSE) for the full text.

---

**Author:** Mohammed Touseef Ansari
**GitHub:** [@LightAwesome](https://github.com/LightAwesome)
