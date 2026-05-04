# Portcullis — Product Requirements Document

**Version:** 2.0 · **Status:** Active Development · **Language:** Go 1.22+

> A self-hosted API gateway with Redis-backed sliding-window rate limiting, atomic Lua-scripted counters, async request logging, Prometheus metrics, and a themed CLI — built in Go and deployed as a single static binary.

---

## 1. Overview

### 1.1 Problem Statement

Every personal project that touches an external API repeats the same setup: credentials, rate-limit handling, request logging, failure behaviour. This is duplicated across every project, creating fragile integrations, zero visibility, and no shared policy enforcement.

Portcullis acts as a single trusted intermediary between your applications and every external API they depend on. Once an app is connected, it inherits authentication, rate limiting, logging, and observability for free.

### 1.2 What This Is Not

To stay finishable, Portcullis explicitly is not:

- A general-purpose API management platform (Kong, Apigee, AWS API Gateway)
- A secrets vault (HashiCorp Vault)
- A service mesh (Istio, Linkerd)
- A multi-tenant SaaS product
- A Kubernetes-native control plane

### 1.3 Success Criteria

The project is successful if, three months after shipping, you naturally reach for it when starting new projects — not because you're forcing yourself, but because it genuinely reduces setup time and makes API behaviour easier to observe.

A secondary success criterion: at least one of your existing projects has been migrated onto Portcullis, with documented before/after metrics. See Phase 7.

---

## 2. Theming & Voice

Portcullis leans into its medieval namesake. This is not pure decoration — it makes the system memorable in interviews, gives error messages personality, and signals craft. The rule: **theme the human-facing surface, leave the technical surface clean.**

### 2.1 Vocabulary

| **Theme term** | **Means** | **Used in** |
| --- | --- | --- |
| **Garrison** | A registered client (an app stationed behind the gate) | CLI, errors, dashboard labels |
| **Banner** | A gateway key (the credential a garrison flies) | CLI output, docs |
| **Chronicle** | The request log / audit trail | CLI, admin UI |
| **Muster** | List/roll-call of garrisons | CLI |
| **Siege** | A load test | CLI |
| **The Gate / Portcullis** | The gateway itself | Errors, status, prose |
| **Keep** | An upstream API | Errors |

### 2.2 Stays technical (do **not** rename)

HTTP status codes, Postgres column names, Go package names, Prometheus metric names, environment variables, log field keys. Anything a tool parses or a future-you greps. Theming is for prose, not protocol.

### 2.3 Error voice

| **Condition** | **Body** |
| --- | --- |
| 401 missing key | `"halt — no banner, no entry"` |
| 401 invalid key | `"that banner is not recognised at this gate"` |
| 429 rate limited | `"the portcullis falls — try again in {N}s"` |
| 502 upstream | `"the keep beyond the gate has not answered"` |
| 503 shutting down | `"the gate is closed for repairs"` |
| 200 health | `{"status": "the gate stands", "redis": "ok", "postgres": "ok"}` |

All error bodies are JSON: `{"error": "<theme line>", "code": "<machine-readable>", ...}`. The theme line is for humans; the `code` field is what scripts switch on.

### 2.4 CLI grammar

`portcullis <noun> <verb>` for resources, `portcullis <verb>` for daemon-level actions. Examples in §7.

---

## 3. Technology Stack

| **Concern** | **Decision + Rationale** |
| --- | --- |
| **Language** | Go 1.22 — compiled binary, native concurrency, excellent stdlib HTTP tooling. Chosen over Python to diversify portfolio while matching the problem shape (I/O-heavy, concurrent network proxy). |
| **HTTP Router** | Chi — thin wrapper over net/http, idiomatic, no magic. Closer to stdlib than Gin. Middleware composition is clean and explicit. |
| **Reverse Proxy** | `net/http/httputil.ReverseProxy` (stdlib) — purpose-built. Handles header forwarding, hop-by-hop stripping, and response streaming. |
| **Rate-Limit Store** | Redis 7 via go-redis/v9 — in-memory speed on the hot path, atomic Lua execution prevents race conditions, shared state for future horizontal scaling. |
| **Rate-Limit Algorithm** | Sliding window log — sorted set of request timestamps in Redis, evaluated atomically via Lua. More correct than fixed window (no boundary burst), more auditable than token bucket. |
| **Persistent Store** | PostgreSQL 16 via pgx/v5 — relational fit, native Postgres types, better pooling than database/sql. |
| **Migrations** | golang-migrate — CLI-driven, plain SQL up/down files, runnable in CI. |
| **Async Logging** | Buffered channel (cap 512) + worker goroutine, **non-blocking send via `select`-`default`**. Channel provides backpressure; overflow drops with a counter increment rather than blocking the request path. |
| **Structured Logging** | `log/slog` (stdlib since Go 1.21) — JSON to stdout, no extra dep. Each request gets a request ID, threaded through context. |
| **Metrics** | Prometheus client_golang + promhttp — `/metrics` endpoint scraped by Prometheus, surfaced in Grafana. |
| **Authentication** | `pck_<keyId>_<secret>` keys; Postgres stores keyId (indexed, plaintext) and HMAC-SHA256 of the secret with a server-side pepper. **Not bcrypt** — see §4.2. |
| **Encryption at Rest** | AES-256-GCM for upstream API secrets. Master key from env var, documented as non-rotatable at MVP. |
| **Containerisation** | Docker Compose — gateway + postgres + redis + prometheus + grafana. Same Compose file in dev and on the VPS. |
| **Deployment** | Single VPS (Hetzner CX22, ~$6/mo) — Nginx for TLS termination, Certbot for certs, Docker Compose for runtime. |
| **CLI** | Cobra — wraps the admin HTTP API with themed verbs (§7). |
| **Testing** | `testing` stdlib + testcontainers-go — real Postgres and Redis containers, no mocks for infrastructure. Unit tests for rate-limit logic and middleware. |
| **Dashboard (stretch)** | React 18 + Vite + Recharts, embedded via `embed.FS`. Phase 8 only. |

---

## 4. Architecture

### 4.1 Request Flow

```
Client request with X-Gateway-Key header
  → Chi router matches /proxy/{prefix}/*
  → Auth middleware: parse key, lookup by keyId (Redis cache → Postgres),
                     HMAC-verify secret with pepper (constant-time compare)
  → Rate-limit middleware: Lua script eval in Redis
       ALLOW: continue  |  DENY: 429 with Retry-After + themed body
  → ReverseProxy: strip gateway key, decrypt + inject upstream secret, forward
  → Upstream response forwarded to client (with X-RateLimit-* headers)
  → select { case logCh <- entry: default: droppedLogs.Inc() }   ← non-blocking
```

### 4.2 Authentication — keyId + HMAC + pepper

Gateway keys have the form `pck_<keyId>_<secret>`:

- `pck_` — constant prefix (so a leaked key is grep-able / scannable)
- `keyId` — 16 hex chars (8 random bytes), stored plaintext in Postgres, indexed for O(log n) lookup
- `secret` — 64 hex chars (32 random bytes); never stored, only its HMAC is

On every request: parse the header, look up by `keyId` (Redis cache, falling back to Postgres), compute `HMAC-SHA256(secret, pepper)`, constant-time compare to the stored hash.

**Why HMAC-SHA256 and not bcrypt:** bcrypt is intentionally slow to defend weak human passwords against brute force. Gateway keys are 32 random bytes (~256 bits of entropy), which is brute-force-infeasible regardless of hash speed. Putting bcrypt on the hot path would cap throughput at ~100 req/s per CPU and would also make Redis caching *correct* (you can't look up a bcrypted-with-fresh-salt value). HMAC with a server-side pepper is what production API systems (Stripe, GitHub) use for this exact case. The pepper lives in `PORTCULLIS_KEY_PEPPER` and means a Postgres-only leak does not compromise keys.

### 4.3 Atomic rate limiting via Lua

Check-and-increment must be atomic to prevent races under concurrent load. A Lua script (`sliding_window.lua`) is loaded at startup via `//go:embed` and runs `ZREMRANGEBYSCORE` (expire old), `ZCOUNT` (current count), `ZADD` (record this one) as a single atomic Redis op. Lua in Redis is single-threaded; no other command runs between the steps. Returns `{allowed, remaining, reset_unix_ms}` for the response headers.

### 4.4 Async logging — actually non-blocking

```go
select {
case s.logCh <- entry:    // capacity 512
default:
    s.metrics.LogsDropped.Inc()
}
```

A bare `s.logCh <- entry` *blocks* when the channel is full — that is the whole point of buffered channels having capacity. The `select`-with-`default` makes the send genuinely non-blocking. Worker goroutine drains in batches: every 500ms or every 50 entries, whichever comes first.

Graceful shutdown drains the channel before the process exits (Phase 4).

### 4.5 Encryption at rest

Upstream API secrets (the real OpenAI key, etc.) are encrypted with AES-256-GCM before storage. Nonce is random per record, prepended to the ciphertext. Master key is 32 bytes from `PORTCULLIS_MASTER_KEY` (base64-encoded in env).

**Acknowledged limitation:** key rotation requires a re-encrypt migration pass and is not built in. Master key compromise compromises all stored secrets. KMS integration is a non-goal at MVP.

### 4.6 Single binary

The CLI and the gateway server are the same binary, dispatched by subcommand:
`portcullis raise` (the server), `portcullis muster` (the CLI), etc. Compiled React dashboard (Phase 8) is embedded via `embed.FS`. `FROM scratch` Docker image, ~15 MB.

### 4.7 Single point of failure

At MVP, the gateway is one instance; if it goes down, connected apps cannot reach configured APIs. This is acceptable for personal scale and is documented, not hidden. Mitigations: `/health` endpoint, structured logs, Docker restart policy, and stateless design (all state in Redis + Postgres) so horizontal scaling is straightforward when needed.

---

## 5. Data Models

### 5.1 PostgreSQL Schema

#### `clients`

| **Column** | **Type** | **Notes** |
| --- | --- | --- |
| **id** | uuid PK | `gen_random_uuid()` |
| **name** | text NOT NULL | Human-readable, e.g. `'hackathon-2025'` |
| **key_id** | text NOT NULL UNIQUE | 16 hex chars, indexed, plaintext lookup |
| **key_hash** | bytea NOT NULL | HMAC-SHA256 of secret with server pepper |
| **is_active** | boolean NOT NULL DEFAULT true | Soft disable |
| **created_at** | timestamptz NOT NULL DEFAULT now() | |

#### `upstream_routes`

| **Column** | **Type** | **Notes** |
| --- | --- | --- |
| **id** | uuid PK | |
| **prefix** | text NOT NULL UNIQUE | e.g. `'openai'` → `/proxy/openai/*` |
| **target_base_url** | text NOT NULL | e.g. `https://api.openai.com/v1` |
| **upstream_secret_ciphertext** | bytea NOT NULL | AES-256-GCM (nonce ‖ ciphertext) |
| **is_active** | boolean NOT NULL DEFAULT true | |
| **created_at** | timestamptz NOT NULL DEFAULT now() | |

#### `rate_limit_policies`

| **Column** | **Type** | **Notes** |
| --- | --- | --- |
| **id** | uuid PK | |
| **client_id** | uuid NOT NULL | FK → `clients(id)` ON DELETE CASCADE |
| **route_prefix** | text NOT NULL | Match against `upstream_routes.prefix` |
| **max_requests** | int NOT NULL | |
| **window_seconds** | int NOT NULL | |
| UNIQUE (client_id, route_prefix) | | |

If no policy exists for a client/route pair, a configurable global default applies.

#### `request_logs`

| **Column** | **Type** | **Notes** |
| --- | --- | --- |
| **id** | uuid PK | |
| **client_id** | uuid NOT NULL | FK → `clients(id)` |
| **route_prefix** | text NOT NULL | Denormalised — survives route deletion |
| **method** | text NOT NULL | |
| **path** | text NOT NULL | |
| **status_code** | int | Null if gateway rejected |
| **latency_ms** | int NOT NULL | End-to-end |
| **rate_limited** | boolean NOT NULL | |
| **error_detail** | text | Nullable |
| **requested_at** | timestamptz NOT NULL DEFAULT now() | Indexed |

Indexes: `(requested_at DESC)`, `(client_id, requested_at DESC)`.

### 5.2 Redis Key Structure

Redis is ephemeral. Losing it resets in-progress windows and forces cold Postgres reads — both acceptable.

| **Key** | **Type** | **Purpose** |
| --- | --- | --- |
| **`rl:{client_id}:{prefix}`** | sorted set | Sliding window log; score = unix ms; TTL = `window_seconds + 60` |
| **`client:{key_id}`** | string (JSON) | Cached client `{id, key_hash_hex, is_active}`, TTL 60s |
| **`route:{prefix}`** | string (JSON) | Cached upstream config, TTL 300s |

---

## 6. API Specification

### 6.1 Proxy Endpoints

| **Route** | **Behaviour** |
| --- | --- |
| **ALL /proxy/{prefix}/*** | Authenticated, rate-limited proxy. `X-Gateway-Key` required and stripped before forwarding. All HTTP methods, full path passthrough. |

### 6.2 Admin Endpoints

Mounted at `/admin`, gated by `X-Admin-Key`. Not exposed to client apps.

| **Route** | **Behaviour** |
| --- | --- |
| **POST /admin/clients** | Register a garrison. Returns the banner (`pck_…`) **once** — never stored in plaintext, never retrievable. |
| **GET /admin/clients** | Muster — list all garrisons. |
| **PATCH /admin/clients/:id** | Update name or active flag. |
| **DELETE /admin/clients/:id** | Hard delete; cascades policies. |
| **POST /admin/routes** | Register an upstream with encrypted secret. |
| **GET /admin/routes** | List configured routes. |
| **DELETE /admin/routes/:id** | Remove an upstream route. |
| **POST /admin/policies** | Create a rate-limit policy for a client/route pair. |
| **GET /admin/policies** | List policies. |
| **DELETE /admin/policies/:id** | Drop a policy (falls back to default). |
| **GET /admin/chronicle** | Paginated request log; filters: `client_id`, `route_prefix`, date range, `rate_limited`. |
| **GET /admin/stats** | Aggregated: requests/client, p50/p95/p99 latency, error rate, top routes. |

### 6.3 System Endpoints

| **Route** | **Behaviour** |
| --- | --- |
| **GET /health** | `{"status": "the gate stands", "redis": "ok", "postgres": "ok"}` — pings both deps. |
| **GET /metrics** | Prometheus scrape: `portcullis_requests_total`, `portcullis_request_duration_seconds`, `portcullis_rate_limited_total`, `portcullis_logs_dropped_total`. |
| **GET /dashboard/*** | Embedded React SPA. Phase 8 only. |

### 6.4 Rate-Limit Headers

Present on every proxied response:

| **Header** | **Meaning** |
| --- | --- |
| **X-RateLimit-Limit** | Max requests in window for this client/route |
| **X-RateLimit-Remaining** | Remaining in current window |
| **X-RateLimit-Reset** | Unix timestamp when window resets |
| **Retry-After** | Seconds until retry (429 only) |

---

## 7. CLI Specification

The CLI wraps the admin API. Same binary as the server; subcommand routing.

| **Command** | **Does** |
| --- | --- |
| **`portcullis raise`** | Start the gateway server (default action with no args). |
| **`portcullis muster`** | List garrisons (`GET /admin/clients`). |
| **`portcullis garrison add <name>`** | Register a client; prints the banner once. |
| **`portcullis garrison dismiss <id>`** | Set `is_active = false`. |
| **`portcullis route add <prefix> <url> --secret <key>`** | Register an upstream. |
| **`portcullis route list`** | List routes. |
| **`portcullis policy set <client> <route> <max>/<window>`** | e.g. `… set hackathon-2025 openai 60/60`. |
| **`portcullis chronicle [--tail] [--garrison <id>] [--limit N]`** | Tail or query the log. |
| **`portcullis siege <prefix> --concurrent N --total M`** | Built-in load test. Useful in Phase 2 and demoably impressive. |
| **`portcullis status`** | Hit `/health` and show a one-line summary. |

Configuration: CLI reads `PORTCULLIS_URL` and `PORTCULLIS_ADMIN_KEY` from env or `~/.portcullis/config`.

---

## 8. Implementation Plan

Phases are work units, not time units. Each phase has a Definition of Done. **See `BACKLOG.md` for the full ticket list with sizes and acceptance criteria.** This section describes intent.

### Phase 0 — Go Foundations
**Goal:** Stop being a Python developer in a Go costume.
**DoD:** You've built a throwaway HTTP server, you understand goroutines vs. channels vs. select, and you know what `context.Context` is for. Until then, do not start Phase 1.

### Phase 1 — Proxy Core
**Goal:** A working authenticated reverse proxy.
**DoD:** `curl -H 'X-Gateway-Key: pck_…' http://localhost:8080/proxy/httpbin/get` returns the upstream response. Without the header you get `401 — halt, no banner, no entry`. With a wrong key, same. Includes minimum admin endpoints (POST clients, POST routes) needed to set up test data.

### Phase 2 — Rate Limiting
**Goal:** Atomic sliding-window rate limiting demonstrably correct under concurrent load.
**DoD:** Running `portcullis siege` with 50 concurrent workers × 200 requests against a route limited to 100/60s shows: exactly the right number of 429s, X-RateLimit headers correct on every response, no over-counts, retry-after matches reset.

### Phase 3 — Observability
**Goal:** You can see what the gate did.
**DoD:** Every request produces a `request_logs` row (eventually — async). `/metrics` exports the four Prometheus metrics. Grafana dashboard shows requests/min, p95 latency, 429 rate, dropped-logs counter. `slog` JSON is structured and request-ID-correlated.

### Phase 4 — Production Hardening
**Goal:** Ship it on a real domain, with confidence it won't lose data on shutdown or leak secrets.
**DoD:** Live deployment at HTTPS on a real domain, AES-encrypted upstream secrets, graceful shutdown drains the log channel, integration tests with testcontainers pass in CI, `/health` pings real Postgres + Redis.

### Phase 5 — Depth Feature (pick one)
**Goal:** A feature impressive enough to spend an interview on.
**Options:**
- **Circuit breaker** — per-upstream state machine (closed/open/half-open), trips on 5xx threshold, `sync.RWMutex` or atomic-CAS. Most resume-effective IMO.
- **Retry with exponential backoff + jitter** — `context.WithTimeout`, configurable max attempts.
- **Response caching** — GETs cached in Redis with route-configurable TTL, `Cache-Control` aware.
- **Per-client cost tracking** — parse token usage from upstream responses, accumulate in Postgres.

Pick one. Ship it well. The interview value is depth, not breadth.

**DoD:** Feature works, has tests, has a documented `Why this and not that?` paragraph in the README.

### Phase 6 — CLI + Theming Polish
**Goal:** The project has personality and is operable from a terminal.
**DoD:** All admin actions doable via `portcullis …` commands. All error responses use the themed voice. Health endpoint says "the gate stands." `portcullis siege` exists and works.

### Phase 7 — Demonstration & Documentation
**Goal:** Make the project a *story*, not just a system.
**DoD:** At least one of your existing projects (a script, a hackathon thing, anything) has been migrated to use Portcullis. README has:
- Architecture diagram (one image, hand-drawn is fine, Excalidraw is better)
- Before/after section: latency, observability, "what broke before that doesn't now"
- A 30-second demo GIF or video
- A `Why this and not Kong/Apigee/etc.?` paragraph
- The themed CLI shown off

This phase is the difference between "I built an API gateway" and "I built an API gateway and here's what changed when I used it."

### Phase 8 (stretch) — React Dashboard
**Goal:** Optional, but a nice screenshot for portfolio sites.
**DoD:** SPA at `/dashboard` showing requests over time, latency distribution, 429 rate, garrison list, chronicle viewer. Embedded in the binary via `embed.FS`. Skip if Phase 5–7 took longer than expected; Grafana already covers most of this.

---

## 9. Project Structure

| **Path** | **Purpose** |
| --- | --- |
| **cmd/portcullis/main.go** | Entry point. Cobra dispatch: `raise` starts the server, others are CLI verbs. |
| **internal/server/** | HTTP server wiring: router, middleware chain, dependency injection. No business logic. |
| **internal/auth/** | Key parsing, keyId lookup, HMAC verify. Pepper config. |
| **internal/ratelimit/** | Middleware + Lua loader. |
| **internal/ratelimit/lua/sliding_window.lua** | Atomic Redis script. Lives next to the code that embeds it. |
| **internal/proxy/** | ReverseProxy handler: header strip, secret inject, forward. |
| **internal/logging/** | LogEntry, channel, worker, batch insert. |
| **internal/store/** | All pgx access. Nothing else touches pgx directly. |
| **internal/admin/** | Admin API handlers: CRUD + stats + chronicle queries. |
| **internal/cli/** | Cobra command tree for non-`raise` verbs. |
| **internal/crypto/** | AES-256-GCM encrypt/decrypt for upstream secrets. |
| **internal/metrics/** | Prometheus definitions. |
| **internal/config/** | Env-var loading via godotenv. |
| **migrations/** | golang-migrate SQL up/down. |
| **dashboard/** | React + Vite (Phase 8). |
| **docker-compose.yml** | Gateway + postgres + redis + prometheus + grafana. |
| **Dockerfile** | Multi-stage; FROM scratch final. |
| **README.md** | The story (Phase 7). |

---

## 10. Deliberate Non-Goals

Saying no is a design decision. State these in interviews — it's evidence of judgment.

| **Non-goal** | **Reasoning** |
| --- | --- |
| **JWT auth between client and gateway** | Pre-shared keys are simpler and sufficient when clients are mine. JWT earns its complexity when clients are third parties I don't control. |
| **Horizontal scaling / load balancing** | Single instance is honest at personal scale. Stateless design means a load balancer is straightforward when actually needed. |
| **Streaming responses (SSE)** | `httputil.ReverseProxy` buffers by default; streaming requires manual `Flusher` copy loops. Real complexity, defer to Phase 5 if chosen as the depth feature. |
| **Secret rotation UI** | Rotation = re-create route. Acceptable for personal use. |
| **Multi-user admin** | Single admin key. Adding per-user admin auth is straightforward later. |
| **Webhook ingestion** | Inbound is a different domain. Keep this gateway outbound-only. |
| **Provider abstraction / fallback routing** | Cross-provider fallback requires normalising request/response shapes, which is provider-specific. Future direction, not MVP. |
| **KMS-managed encryption keys** | Master key from env var is sufficient at this scale. KMS adds a real dependency for marginal gain. |

---

## 11. Resume & Interview Framing

### 11.1 Resume Bullets — Layered

**One-liner (top of resume, skim-friendly):**
> **Portcullis** — Self-hosted API gateway in Go. Redis-backed sliding-window rate limiting via atomic Lua scripts, async request logging on buffered channels, Prometheus + Grafana observability. Single 15 MB binary, deployed on a $6/mo VPS.

**Two-liner (project section):**
> Built **Portcullis**, a self-hosted API gateway in Go (Chi, pgx, go-redis). Atomic sliding-window rate limiting via embedded Lua scripts; async log pipeline with buffered channels and batched Postgres writes; AES-256-GCM-encrypted upstream secrets; Prometheus metrics surfaced in Grafana.
> Deployed as a single 15 MB static binary on a Hetzner VPS with Nginx + Certbot TLS and Docker Compose. Migrated *N* personal projects onto it; reduced setup time from *X* minutes to *Y*; surfaced *Z*% of failure modes that previously ailed silently.

The second bullet's numbers come out of Phase 7. **Do Phase 7.**

### 11.2 Expected Interview Questions

| **Question** | **What to say** |
| --- | --- |
| **Why sliding window not fixed?** | Fixed window has a boundary-burst problem — a client can get 2× the limit straddling the reset. Sliding-window log tracks individual timestamps, so it's exact. The cost is more Redis memory per client, irrelevant at this scale. |
| **How are race conditions prevented?** | Lua script via `EVAL`. Lua in Redis is single-threaded and atomic — no other command runs between `ZCOUNT` and `ZADD`. Same approach Stripe uses. |
| **Why HMAC and not bcrypt?** | bcrypt's slowness defends weak human passwords. Gateway keys are 32 random bytes — brute force is computationally infeasible. bcrypt would cap throughput at ~100 req/s/CPU and break Redis caching (per-call salt). HMAC-SHA256 with a server-side pepper is what Stripe and GitHub use for API keys. |
| **What if the gateway dies?** | Single point of failure by design at MVP, documented explicitly. All state lives in Redis + Postgres, so the path to HA is "add a load balancer and another instance." `/health` + Docker restart policy mean transient failures recover automatically. |
| **Why async logging?** | Synchronous Postgres writes on the hot path would add ~5-20ms to every request. The `select`-`default` send is genuinely non-blocking; if the worker falls behind, `LogsDropped` increments and the request still returns fast. The trade-off is at-most-once delivery for log entries during overload, which is the right call here. |
| **What happens on shutdown?** | `os.Signal` handler triggers `http.Server.Shutdown` with a 30s context, then closes the log channel and waits for the worker to drain. So in-flight requests complete and queued logs flush. |
| **Why Go?** | I/O-heavy, concurrent network proxy is exactly what Go's stdlib + goroutines were designed for. Also wanted to diversify my portfolio beyond Python. |
| **What would you do differently at scale?** | Sliding-window log gets memory-expensive past a few hundred clients per route — I'd switch to a sliding-window counter (a hybrid that approximates with two fixed buckets). I'd move encryption keys to a real KMS. I'd add a circuit breaker (or, if I picked it as my Phase 5 depth feature, point at it). |

---

## 12. Learning Resources

### Before Phase 1

| **Resource** | **Why** |
| --- | --- |
| **The Go Tour** (tour.golang.org) | Full run, especially concurrency. Don't skip. |
| **Go by Example** | Goroutines, channels, select, mutexes, defer, context, embed. Bookmark. |
| **"How I write HTTP services in Go" — Mat Ryer** | Influences how `main.go` and dependency wiring should look. |
| **100 Go Mistakes — Teiva Harsanyi, ch. 1–4** | Prevents Python-shaped Go habits. |

### During the build

| **Resource** | **When** |
| --- | --- |
| **Chi README + middleware examples** | Phase 1 router. |
| **pgx wiki — pools and context** | Phase 1 DB layer. |
| **Redis EVAL docs** | Phase 2 Lua script. |
| **Prometheus Go client README** | Phase 3 metrics. |
| **testcontainers-go docs** | Phase 4 integration tests. |
| **Cobra docs** | Phase 6 CLI. |

---

*Portcullis PRD v2.0 · Personal API Gateway · Go + Redis + PostgreSQL*
