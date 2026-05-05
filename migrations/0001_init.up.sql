-- Initial schema for Portcullis.
--
-- Four tables:
--   clients              — registered apps (garrisons) authenticated via gateway key
--   upstream_routes      — configured external APIs (keeps) the gateway proxies to
--   rate_limit_policies  — per-client per-route rate limit rules
--   request_logs         — append-only audit trail (the chronicle)
--
-- Design decisions worth noting:
--   - UUIDs (gen_random_uuid()) for all primary keys; available in Postgres 13+
--     without an extension.
--   - timestamptz everywhere (never plain timestamp) — stores UTC, converts on read.
--   - rate_limit_policies.route_prefix and request_logs.route_prefix are deliberately
--     NOT foreign keys to upstream_routes. Policies and logs survive route deletion.
--   - bytea for binary fields (HMAC hashes, AES ciphertext) — saves space, avoids
--     encoding round-trips.

-- ============================================================
-- clients (garrisons)
-- ============================================================

CREATE TABLE clients (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL CHECK (length(name) > 0),
    key_id          TEXT NOT NULL UNIQUE CHECK (length(key_id) = 16),
    key_hash        BYTEA NOT NULL CHECK (length(key_hash) = 32),
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE clients IS 'Registered apps that authenticate to the gateway.';
COMMENT ON COLUMN clients.key_id IS '16 hex chars from the gateway key (pck_<keyId>_<secret>); plaintext, indexed for O(log n) lookup.';
COMMENT ON COLUMN clients.key_hash IS 'HMAC-SHA256 of the secret with server-side pepper. Never bcrypt — see PRD §4.2.';

-- ============================================================
-- upstream_routes (keeps)
-- ============================================================

CREATE TABLE upstream_routes (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prefix                      TEXT NOT NULL UNIQUE CHECK (length(prefix) > 0),
    target_base_url             TEXT NOT NULL CHECK (target_base_url LIKE 'http%'),
    upstream_secret_ciphertext  BYTEA NOT NULL,
    is_active                   BOOLEAN NOT NULL DEFAULT TRUE,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE upstream_routes IS 'External APIs the gateway proxies to.';
COMMENT ON COLUMN upstream_routes.prefix IS 'URL prefix; gateway exposes /proxy/{prefix}/* and forwards to target_base_url.';
COMMENT ON COLUMN upstream_routes.upstream_secret_ciphertext IS 'Real upstream API key, AES-256-GCM encrypted. Plaintext for Phase 1, encrypted in Phase 4.';

-- ============================================================
-- rate_limit_policies
-- ============================================================

CREATE TABLE rate_limit_policies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id       UUID NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    route_prefix    TEXT NOT NULL,
    max_requests    INTEGER NOT NULL CHECK (max_requests > 0),
    window_seconds  INTEGER NOT NULL CHECK (window_seconds > 0),
    UNIQUE (client_id, route_prefix)
);

COMMENT ON TABLE rate_limit_policies IS 'Per-client per-route rate limit rules. No policy = global default applies.';
COMMENT ON COLUMN rate_limit_policies.route_prefix IS 'Matches upstream_routes.prefix by string. NOT a foreign key — policies survive route deletion.';

-- ============================================================
-- request_logs (the chronicle)
-- ============================================================

CREATE TABLE request_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id       UUID NOT NULL,
    route_prefix    TEXT NOT NULL,
    method          TEXT NOT NULL,
    path            TEXT NOT NULL,
    status_code     INTEGER,
    latency_ms      INTEGER NOT NULL CHECK (latency_ms >= 0),
    rate_limited    BOOLEAN NOT NULL DEFAULT FALSE,
    error_detail    TEXT,
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE request_logs IS 'Append-only audit trail. Denormalised on purpose — survives route/client deletion.';
COMMENT ON COLUMN request_logs.client_id IS 'NOT a foreign key — log entries survive client deletion.';
COMMENT ON COLUMN request_logs.status_code IS 'NULL when gateway rejected the request (auth/rate-limit) before proxying.';

-- Indexes for the chronicle queries (admin /chronicle endpoint).
-- The composite index serves both filtered (WHERE client_id) and unfiltered uses,
-- but only the leftmost-prefix part — so we add a separate index on requested_at
-- for the unfiltered reverse-chrono case.
CREATE INDEX idx_request_logs_requested_at ON request_logs (requested_at DESC);
CREATE INDEX idx_request_logs_client_requested ON request_logs (client_id, requested_at DESC);
