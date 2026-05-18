#!/usr/bin/env bash
# Seed the gateway with a known-good test client and test route.
#
# Reads the admin key from .env (matches what the running gateway uses).
# Hits the admin API to register a "seed-test" garrison and an "httpbin"
# route pointing at https://httpbin.org.
#
# On success, prints the generated gateway key for use in subsequent curl
# commands. The key is shown ONCE — capture it now.
#
# To reset: `make nuke && make up && make migrate-up && make seed`.

set -euo pipefail

# Resolve the script's location and treat the parent as the repo root,
# so this script works regardless of where it's invoked from.
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
REPO_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"

# Load .env. It's git-ignored and contains the admin key.
if [[ ! -f "$REPO_ROOT/.env" ]]; then
    echo "error: $REPO_ROOT/.env not found. Copy .env.example and fill in values." >&2
    exit 1
fi

set -a
# shellcheck disable=SC1091
source "$REPO_ROOT/.env"
set +a

ADDR="${PORTCULLIS_ADDR:-:8080}"
HOST="http://127.0.0.1${ADDR}"

if [[ -z "${PORTCULLIS_ADMIN_KEY:-}" ]]; then
    echo "error: PORTCULLIS_ADMIN_KEY is not set in .env" >&2
    exit 1
fi

# Verify the gateway is reachable. The seed needs the server running.
if ! curl -sf "$HOST/health" >/dev/null; then
    echo "error: gateway not reachable at $HOST" >&2
    echo "       run 'make dev' in another terminal first" >&2
    exit 1
fi

echo "seeding $HOST..."

# Create the test client. The response includes the once-only key.
CLIENT_RESPONSE=$(curl -sS -X POST "$HOST/admin/clients" \
    -H "X-Admin-Key: $PORTCULLIS_ADMIN_KEY" \
    -H "Content-Type: application/json" \
    -d '{"name":"seed-test"}' \
    -w "\n%{http_code}")

CLIENT_BODY=$(printf '%s\n' "$CLIENT_RESPONSE" | sed '$d')
CLIENT_STATUS=$(echo "$CLIENT_RESPONSE" | tail -n 1)

if [[ "$CLIENT_STATUS" == "409" ]]; then
    echo "error: client 'seed-test' already exists. To reset, run:" >&2
    echo "       make nuke && make up && make migrate-up && make seed" >&2
    exit 1
elif [[ "$CLIENT_STATUS" != "201" ]]; then
    echo "error: failed to create client (HTTP $CLIENT_STATUS):" >&2
    echo "$CLIENT_BODY" >&2
    exit 1
fi

GATEWAY_KEY=$(echo "$CLIENT_BODY" | jq -r '.key')
KEY_ID=$(echo "$CLIENT_BODY" | jq -r '.key_id')

# Create the test route pointing at httpbin.
ROUTE_RESPONSE=$(curl -sS -X POST "$HOST/admin/routes" \
    -H "X-Admin-Key: $PORTCULLIS_ADMIN_KEY" \
    -H "Content-Type: application/json" \
    -d '{"prefix":"httpbin","target_base_url":"https://httpbin.org","upstream_secret":"unused-by-httpbin"}' \
    -w "\n%{http_code}")

ROUTE_BODY=$(printf '%s\n' "$ROUTE_RESPONSE" | sed '$d')
ROUTE_STATUS=$(echo "$ROUTE_RESPONSE" | tail -n 1)

if [[ "$ROUTE_STATUS" == "409" ]]; then
    echo "error: route 'httpbin' already exists. To reset, run:" >&2
    echo "       make nuke && make up && make migrate-up && make seed" >&2
    exit 1
elif [[ "$ROUTE_STATUS" != "201" ]]; then
    echo "error: failed to create route (HTTP $ROUTE_STATUS):" >&2
    echo "$ROUTE_BODY" >&2
    exit 1
fi

# Create a permissive test policy: 1000 req/min so manual testing doesn't
# hit the limit during ordinary exploration. Tighten via the API when
# rate-limiting behaviour itself is what's being tested.
POLICY_RESPONSE=$(curl -sS -X POST "$HOST/admin/policies" \
    -H "X-Admin-Key: $PORTCULLIS_ADMIN_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"client_id\":\"$(echo "$CLIENT_BODY" | jq -r '.id')\",\"route_prefix\":\"httpbin\",\"max_requests\":1000,\"window_seconds\":60}" \
    -w "\n%{http_code}")

POLICY_STATUS=$(echo "$POLICY_RESPONSE" | tail -n 1)
if [[ "$POLICY_STATUS" != "201" ]]; then
    echo "warning: policy create returned $POLICY_STATUS (continuing — seed is usable without explicit policy)" >&2
fi

cat <<EOF

seeded successfully.

  garrison:    seed-test
  key_id:      $KEY_ID
  gateway key: $GATEWAY_KEY

  route:       httpbin -> https://httpbin.org

try it (after P1.17 lands the real proxy):
  curl -H 'X-Gateway-Key: $GATEWAY_KEY' $HOST/proxy/httpbin/get

save the gateway key now — it cannot be recovered.
EOF
