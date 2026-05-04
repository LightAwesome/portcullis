# Portcullis Backlog

The actionable companion to `PRD.md`. Each ticket has an ID, a size, and acceptance criteria. Check them off as you go.

**Sizes** (rough complexity, not time):
- **S** — single sitting, one focused chunk (a function, an endpoint, a config wire-up)
- **M** — half-day to a day (a feature, a middleware, an integration)
- **L** — multi-day; usually a candidate for further breakdown

**Conventions:** Ticket IDs are `P<phase>.<n>`. Phase 0 uses `F0.x`. A ticket marked **(blocks → P2.3)** means P2.3 cannot start until this lands. A ticket without that tag has no hard downstream blocker.

---

## Phase 0 — Go Foundations

> Stop being a Python developer in a Go costume. No project code until this is done.

- [ ] **F0.1 (M)** — Complete the Go Tour, full run through concurrency
  - *Done when:* you've finished tour.golang.org including the goroutines/channels exercises
- [ ] **F0.2 (S)** — Read Go by Example: goroutines, channels, select, mutexes, defer, context, embed
  - *Done when:* you can explain each in 2 sentences without looking
- [ ] **F0.3 (S)** — Watch "How I Write HTTP Services in Go" by Mat Ryer
  - *Done when:* you've watched it; your mental model for `main.go` structure is influenced by it
- [ ] **F0.4 (M)** — Read 100 Go Mistakes, chapters 1–4
  - *Done when:* finished; nearby for reference during the build
- [ ] **F0.5 (M)** — Build a throwaway HTTP server
  - *Done when:* a server in `~/scratch/` accepts a request, logs it with `slog`, returns JSON. You used a goroutine and a channel somewhere even if it wasn't strictly needed. You understand `http.Handler`, `http.HandlerFunc`, and `http.ServeMux`

**Phase 0 DoD:** You can answer "what is a goroutine, and how is it different from a thread?" without thinking about it.

---

## Phase 1 — Proxy Core

> A working authenticated reverse proxy.

### Setup
- [ ] **P1.1 (S)** — Initialise repo, `go mod init github.com/<you>/portcullis`, `.gitignore`, MIT license
- [ ] **P1.2 (M)** — Project skeleton matching PRD §9 (`cmd/`, `internal/`, `migrations/`, etc.). Empty packages, doc comments at the top of each
- [ ] **P1.3 (M)** — `docker-compose.yml` with services: `gateway` (placeholder), `postgres:16`, `redis:7`. Named volumes. `.env.example` committed
  - *Done when:* `docker compose up postgres redis` brings both up healthy
- [ ] **P1.4 (S)** — `internal/config`: load env vars (`PORTCULLIS_*`) via godotenv. Required: DB URL, Redis URL, admin key, key pepper, master encryption key. Fail-fast on missing.

### Database
- [ ] **P1.5 (S)** — Set up golang-migrate; add Makefile targets `migrate-up`, `migrate-down`, `migrate-create`
- [ ] **P1.6 (M)** — Migration `0001_init.sql`: create `clients`, `upstream_routes`, `rate_limit_policies`, `request_logs` per PRD §5.1. Indexes included
  - *Done when:* `make migrate-up` succeeds; `\d clients` in psql shows the schema
- [ ] **P1.7 (M)** — `internal/store`: pgx pool init, basic query helpers for `clients` and `upstream_routes` (Get by id, Get by key_id, Get by prefix, Insert)

### Redis
- [ ] **P1.8 (S)** — `internal/store` (or `internal/cache`): go-redis client init, ping helper

### Server skeleton
- [ ] **P1.9 (S)** — `cmd/portcullis/main.go`: Cobra root command with `raise` subcommand placeholder
- [ ] **P1.10 (S)** — `internal/server`: Chi router, `GET /health` returning `{"status":"the gate stands","redis":"ok","postgres":"ok"}` with real pings
- [ ] **P1.11 (S)** — Wire it up: `portcullis raise` starts the server on configured port; graceful Ctrl-C exit (basic, not full Phase 4 graceful shutdown yet)

### Auth
- [ ] **P1.12 (M)** — `internal/auth`: key generation helper (`pck_<keyId>_<secret>`), HMAC-SHA256 with pepper, constant-time compare. Unit tests for parse, generate, verify
- [ ] **P1.13 (M)** — Auth middleware: parse `X-Gateway-Key`, lookup by `key_id` (Redis cache miss → Postgres → cache), HMAC-verify, set `client_id` in request context. 401 with themed body on failure
  - *Done when:* unit tests cover: missing header, malformed, unknown keyId, valid keyId / wrong secret, valid+inactive, valid+active

### Minimum admin (needed to test the proxy)
- [ ] **P1.14 (S)** — Admin middleware: `X-Admin-Key` constant-time compare against env var
- [ ] **P1.15 (M)** — `POST /admin/clients` and `POST /admin/routes`: the bare minimum to bootstrap test data. Plaintext upstream secret accepted for now (encryption comes in Phase 4). `clients` POST returns the banner once
- [ ] **P1.16 (S)** — `seed.sql` or `make seed`: convenience to insert a test client + route pointing at httpbin.org

### Reverse proxy
- [ ] **P1.17 (M)** — `internal/proxy`: `httputil.ReverseProxy` handler. Strip `X-Gateway-Key`, rewrite path (`/proxy/{prefix}/x/y/z` → `/x/y/z`), inject upstream secret as `Authorization: Bearer …` (or upstream-configured header — start with `Authorization`). Forward Host appropriately
- [ ] **P1.18 (S)** — Wire `/proxy/{prefix}/*` route through auth middleware → proxy handler

### Smoke
- [ ] **P1.19 (S)** — Manual test: register a client, register a route pointing at `https://httpbin.org`, `curl` through the gateway, see the upstream response. Without header → themed 401. Wrong key → themed 401

**Phase 1 DoD:** A request with a valid banner reaches httpbin via the gateway. A request without one is halted at the gate with a themed message.

---

## Phase 2 — Rate Limiting

> Atomic sliding-window rate limiting demonstrably correct under concurrent load.

- [ ] **P2.1 (M)** — Write `internal/ratelimit/lua/sliding_window.lua`. Operations: `ZREMRANGEBYSCORE` (drop expired), `ZCOUNT` (current), conditional `ZADD` + `EXPIRE`. Returns `{allowed, remaining, reset_ms}`. KEYS[1]=window key, ARGV=now_ms, max, window_ms, request_id
- [ ] **P2.2 (S)** — Embed via `//go:embed`; load script SHA at startup; `EVALSHA` with `EVAL` fallback
- [ ] **P2.3 (M)** — Policy lookup: `internal/store` query `(client_id, route_prefix) → max, window`. Cache in Redis under `route:{prefix}` (300s TTL) plus an in-memory map invalidated on admin write. Global default applied if no policy
- [ ] **P2.4 (M)** — Rate-limit middleware: call Lua script, set `X-RateLimit-*` headers on every response, 429 + `Retry-After` + themed body on deny. Sits *after* auth, *before* proxy
  - *Done when:* unit tests cover: under-limit allows, at-limit denies, header values match script return
- [ ] **P2.5 (S)** — Admin: `POST /admin/policies` and `DELETE /admin/policies/:id`. (Wider admin CRUD lives in P4 / P6)
- [ ] **P2.6 (M)** — `portcullis siege <prefix> --concurrent N --total M`: built-in load test command. Prints histogram of status codes and latency p50/p95/p99
- [ ] **P2.7 (M)** — Atomicity test: a Go test that fires 1000 concurrent requests against a 100/60s policy and asserts exactly 100 allowed, 900 denied. Uses real Redis via testcontainers or against the local compose stack

**Phase 2 DoD:** `portcullis siege openai --concurrent 50 --total 200` against a 100/60s policy shows 100 × 200, 100 × 429 (give or take depending on timing window), correct headers throughout, no over-counts.

---

## Phase 3 — Observability

> You can see what the gate did.

- [ ] **P3.1 (S)** — `slog` JSON handler at startup. Request-ID middleware: generate UUID per request, put on context, add to slog logger via context handler
- [ ] **P3.2 (M)** — `internal/logging`: `LogEntry` struct, channel `chan LogEntry` (cap 512), worker goroutine. Send is **`select`-`default`**, drops on overflow and increments `LogsDropped` counter
  - *Done when:* a unit test fills the channel and confirms send doesn't block; counter increments
- [ ] **P3.3 (M)** — Worker batch insert: drain channel, flush every 500ms or 50 entries (whichever first), single multi-row `INSERT` via pgx `Batch` or `COPY`. Log on flush failure; do not retry indefinitely
- [ ] **P3.4 (S)** — Wire logging middleware: capture method, path, status, latency, rate_limited, client_id, error_detail. Push to channel after response written
- [ ] **P3.5 (M)** — `internal/metrics`: define and register
  - `portcullis_requests_total` (counter, labels: client, route, status)
  - `portcullis_request_duration_seconds` (histogram, labels: route)
  - `portcullis_rate_limited_total` (counter, labels: client, route)
  - `portcullis_logs_dropped_total` (counter)
  - `portcullis_upstream_errors_total` (counter, labels: route, code_class)
- [ ] **P3.6 (S)** — Metrics middleware: instrument requests; `GET /metrics` via `promhttp.Handler()`
- [ ] **P3.7 (S)** — Add `prometheus` and `grafana` services to `docker-compose.yml`. Provision Prometheus scrape config for the gateway. Mount Grafana datasource config
- [ ] **P3.8 (M)** — Build a basic Grafana dashboard: requests/min, p95 latency, 429 rate, dropped logs. Export the JSON to `grafana/dashboards/portcullis.json` and provision via Grafana config so it auto-loads on `compose up`

**Phase 3 DoD:** Compose up the full stack, run a siege, see the chronicle filling Postgres, see the dashboard light up in Grafana.

---

## Phase 4 — Production Hardening

> Ship it on a real domain, with confidence it won't lose data on shutdown or leak secrets.

- [ ] **P4.1 (M)** — Graceful shutdown: `os.Signal` (`SIGTERM`, `SIGINT`) → `http.Server.Shutdown` with 30s ctx → close log channel → wait for worker to drain via `sync.WaitGroup`. Test by sending a request, then `kill -TERM` mid-flight; request completes, log lands in DB
- [ ] **P4.2 (M)** — `internal/crypto`: AES-256-GCM encrypt/decrypt with random per-record nonce prepended to ciphertext. Key from `PORTCULLIS_MASTER_KEY` (base64-decoded at startup). Unit tests for round-trip + tamper detection
- [ ] **P4.3 (S)** — Migrate `upstream_routes.upstream_secret` storage to ciphertext. Update admin POST to encrypt on write. Update proxy handler to decrypt on read (cache decrypted in-memory with TTL to avoid re-decrypting per request)
- [ ] **P4.4 (M)** — Multi-stage Dockerfile: `golang:1.22-alpine` build stage, `FROM scratch` final, copy binary + CA certs + migrations. Image should be ~15 MB. `.dockerignore` set up
  - *Done when:* `docker build .` succeeds; `docker run` boots and serves `/health`
- [ ] **P4.5 (M)** — Integration test suite with testcontainers-go: spin up real Postgres + Redis, run end-to-end auth + rate-limit + proxy tests against a fake upstream (httptest.Server)
  - *Done when:* `go test ./...` passes locally and would pass in CI
- [ ] **P4.6 (S)** — GitHub Actions CI: lint (golangci-lint), unit tests, integration tests with services. Cache modules
- [ ] **P4.7 (L)** — VPS deployment
  - Provision Hetzner CX22 (or equivalent)
  - Install Docker + Docker Compose
  - Configure Nginx as TLS-terminating reverse proxy → gateway container
  - Certbot for Let's Encrypt cert
  - Systemd unit (or Docker restart policy) for resilience
  - DNS A record from a domain you own
  - *Done when:* `https://portcullis.<yourdomain>/health` returns "the gate stands"
- [ ] **P4.8 (S)** — Document the deploy in `docs/DEPLOY.md`. Include exact commands. Future-you will thank you

**Phase 4 DoD:** Live on a real HTTPS domain. CI green. AES-encrypted upstream secrets. Graceful shutdown verified.

---

## Phase 5 — Depth Feature (PICK ONE)

> One feature with enough depth to spend a 30-minute interview discussing.

Pick **one**. Don't do all four. The interview value is depth, not breadth.

### Option A — Circuit Breaker *(recommended for the resume)*
- [ ] **P5A.1 (M)** — State machine type: `Closed | Open | HalfOpen`. Per-route. Stored in-memory in a `sync.Map[prefix]*BreakerState`. State transitions guarded by `sync.RWMutex` per breaker
- [ ] **P5A.2 (M)** — Trip logic: rolling window of last N upstream responses (use a small ring buffer). If failure rate > threshold over window, transition Closed → Open. After cooldown, transition to HalfOpen and allow one probe request
- [ ] **P5A.3 (S)** — Config per route: failure threshold, window size, cooldown duration. Stored in `upstream_routes` (new columns) or a separate `circuit_configs` table
- [ ] **P5A.4 (S)** — Open-state response: themed 503 immediately without hitting upstream. `Retry-After` header set to cooldown remaining
- [ ] **P5A.5 (M)** — Tests: simulate failures via a flaky `httptest.Server`, verify state transitions in correct order. Concurrency test: many goroutines hitting the breaker do not produce inconsistent state
- [ ] **P5A.6 (S)** — Metric: `portcullis_circuit_state` gauge per route. Add to Grafana dashboard

### Option B — Retry with backoff
- [ ] **P5B.1 (M)** — Retry middleware *outside* the proxy: on 5xx or network error, retry with exponential backoff + jitter
- [ ] **P5B.2 (S)** — Per-route config: max attempts, base delay, max delay
- [ ] **P5B.3 (S)** — `context.WithTimeout` for the total budget so retries don't extend response time unboundedly
- [ ] **P5B.4 (S)** — Idempotency awareness: retry GET freely, retry POST only with explicit opt-in or `Idempotency-Key` header
- [ ] **P5B.5 (M)** — Tests with flaky upstream

### Option C — Response caching
- [ ] **P5C.1 (M)** — GET responses cached in Redis under `cache:{prefix}:{path}:{query_hash}`
- [ ] **P5C.2 (S)** — Per-route TTL config; honour upstream `Cache-Control: max-age` if present and lower
- [ ] **P5C.3 (S)** — `X-Portcullis-Cache: HIT|MISS` header on responses
- [ ] **P5C.4 (S)** — `DELETE /admin/cache/:prefix` for manual invalidation
- [ ] **P5C.5 (M)** — Tests including TTL expiry and Vary handling

### Option D — Per-client cost tracking
- [ ] **P5D.1 (M)** — `usage_records` table; cost extraction strategy per route (e.g., parse OpenAI-style `usage` block from JSON response body)
- [ ] **P5D.2 (M)** — Response body inspection in proxy without breaking streaming (`io.TeeReader` into a parser goroutine)
- [ ] **P5D.3 (S)** — Admin endpoint: `GET /admin/usage?client_id=…&from=…&to=…`
- [ ] **P5D.4 (S)** — Add to Grafana dashboard

**Phase 5 DoD:** The chosen feature ships with tests, has a paragraph in the README explaining "why this and not that," and has at least one Grafana panel.

---

## Phase 6 — CLI + Theming Polish

> The project has personality and is fully operable from a terminal.

- [ ] **P6.1 (S)** — Cobra command tree set up under `internal/cli`. Persistent flags for `--url` and admin key (also from env)
- [ ] **P6.2 (M)** — `portcullis muster` (list clients, table output)
- [ ] **P6.3 (M)** — `portcullis garrison add <name>` (create + print banner once with a clear "save this — it will not be shown again" warning)
- [ ] **P6.4 (S)** — `portcullis garrison dismiss <id>` (PATCH active=false)
- [ ] **P6.5 (M)** — `portcullis route add <prefix> <url> --secret <key>` and `portcullis route list`
- [ ] **P6.6 (S)** — `portcullis policy set <client> <route> <max>/<window>` (also `policy list`, `policy unset`)
- [ ] **P6.7 (M)** — `portcullis chronicle [--tail] [--garrison <id>] [--limit N]` — paginated log viewer; `--tail` polls for new entries
- [ ] **P6.8 (S)** — `portcullis status` — hits `/health`, prints one-line summary with green/red colour
- [ ] **P6.9 (M)** — Themed error responses across the API. Audit every handler. Add a helper `respondError(w, code, theme, machineCode)` so it's centralised
- [ ] **P6.10 (S)** — Optional: ASCII art on `portcullis raise` banner at server startup. Don't go overboard — small portcullis sketch above the listening line is plenty

**Phase 6 DoD:** Every admin operation doable from CLI. Every error response is themed. `portcullis status` works against your live deployment.

---

## Phase 7 — Demonstration & Documentation

> Make the project a story, not just a system. **This phase is what makes the resume bullet land.**

- [ ] **P7.1 (S)** — Pick a target project to migrate. Criteria: it makes API calls, you can measure something about its behaviour. A hackathon project, a script, anything
- [ ] **P7.2 (M)** — Migrate it. Replace direct API calls with calls through Portcullis. Set up a banner, a route, a policy
- [ ] **P7.3 (M)** — Measure something. Examples:
  - "Setup time for next project's API integration: was 25 min of credential plumbing, now 2 min (`portcullis garrison add …`)"
  - "Failure visibility: previously OpenAI 429s caused silent retries with no log; now every 429 lands in the chronicle with retry-after"
  - "Latency overhead added by the gateway: p50 +Xms, p95 +Yms" (from your Grafana)
- [ ] **P7.4 (M)** — README.md
  - Hero section with the one-line description
  - Architecture diagram (Excalidraw → SVG, commit to `docs/`)
  - Feature list (with the depth feature highlighted)
  - Quickstart (compose up, register, curl)
  - Design decisions section: link to PRD, summarise the top 3 (HMAC vs bcrypt, sliding window log atomic via Lua, async logging with non-blocking send)
  - Before/after section from P7.3
- [ ] **P7.5 (S)** — Record a short demo: terminal recording (`asciinema` or a video). Show: `portcullis garrison add`, a curl that succeeds, a curl that gets rate-limited, `portcullis chronicle --tail`. Embed/link in README
- [ ] **P7.6 (S)** — Update LinkedIn / portfolio site / resume with the layered bullets from PRD §11.1, using actual numbers from P7.3

**Phase 7 DoD:** A stranger reading your README in 90 seconds understands what this is, why it exists, and what changed when you used it.

---

## Phase 8 (Stretch) — React Dashboard

> Skip if Phase 5–7 took longer than expected. Grafana already covers most of this.

- [ ] **P8.1 (S)** — `dashboard/` Vite + React 18 scaffold. Tailwind. Recharts
- [ ] **P8.2 (M)** — Overview page: requests/min, p95 latency, 429 rate, error rate cards, Recharts line for requests over time
- [ ] **P8.3 (M)** — Garrisons page: muster table, click into one for per-garrison stats
- [ ] **P8.4 (M)** — Chronicle page: filterable log viewer (client, route, date range, rate-limited toggle)
- [ ] **P8.5 (S)** — Admin auth: dashboard prompts for admin key, stores in memory only
- [ ] **P8.6 (S)** — Embed compiled dashboard via `//go:embed dashboard/dist/**`. Serve at `/dashboard` from the gateway. Update Dockerfile build stage to also build the SPA
- [ ] **P8.7 (S)** — Screenshot for the README

**Phase 8 DoD:** `https://portcullis.<yourdomain>/dashboard` shows a usable panel. Screenshot in README.

---

## Working Rules

A few habits that will save you grief.

- **Don't move to the next phase until DoD is met.** Half-finished phases compound.
- **Commit per ticket.** Branch names like `p2.4-rate-limit-middleware`. Future-you can see what each ticket actually was.
- **Write the test as part of the ticket.** Not "I'll add tests later" — that's how you end up with no tests.
- **Keep a `NOTES.md`** as you go: surprises, dead ends, things you'd do differently. This becomes interview material.
- **When stuck for >2h, write the question down and move to a different ticket.** Come back tomorrow. The blocking issue is usually a wrong mental model, and a night's sleep fixes it cheaper than four more hours.
- **At the end of each phase, write a 200-word reflection** in `NOTES.md`: what you learned, what surprised you, what you'd change. These are the actual answers to "tell me about a project you're proud of."

---

*Backlog v1 · Companion to PRD.md · Update as scope adjusts*
