#!/usr/bin/env bash
# Compose e2e smoke for mcp-auth-proxy.
#
# Run from the repo root: ./e2e/smoke.sh
# Requires: docker with compose v2, go, curl, jq.
#
# Hard assertions: healthz, both protected-resource documents (#15), the Bearer
# gate, and tool-schema relay presence.
# Deferred assertions (warn, do not fail): non-root container user (until #19
# lands) and verbatim schema relay (until #18 / upstream PR #179 port lands).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "$REPO_ROOT/e2e/docker-compose.yml")
BASE="${E2E_BASE_URL:-http://localhost:8080}"
PROXY_IMAGE="mcp-auth-proxy:local-e2e"

cleanup() {
  "${COMPOSE[@]}" down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> building stdio fixture server"
go build -o "$REPO_ROOT/e2e/bin/testserver" "$REPO_ROOT/pkg/backend/testserver"

echo "==> building proxy image"
"${COMPOSE[@]}" build proxy

echo "==> starting stack"
"${COMPOSE[@]}" up -d

echo "==> waiting for /healthz"
healthy=""
for _ in $(seq 1 60); do
  if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 2
done
if [ -z "$healthy" ]; then
  echo "FAIL: proxy did not become healthy at $BASE/healthz"
  "${COMPOSE[@]}" logs proxy | tail -40
  exit 1
fi

fail() {
  echo "FAIL: $1"
  "${COMPOSE[@]}" logs proxy | tail -40
  exit 1
}

check() { # name expected actual
  if [ "$2" = "$3" ]; then
    echo "PASS: $1"
  else
    fail "$1 (expected '$2', got '$3')"
  fi
}

echo "==> protected-resource metadata (RFC 9728)"
resource="$(curl -fsS "$BASE/.well-known/oauth-protected-resource" | jq -r .resource)"
check "root document resource" "${BASE%/}/" "$resource"

resource="$(curl -fsS "$BASE/.well-known/oauth-protected-resource/mcp" | jq -r .resource)"
check "path-derived /mcp document resource (#15)" "$BASE/mcp" "$resource"

echo "==> Bearer gate"
code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/mcp")"
check "unauthenticated /mcp -> 401" "401" "$code"

code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/.well-known/oauth-protected-resource/other")"
check "unknown well-known path -> 401" "401" "$code"

echo "==> non-root container user (#19, deferred)"
uid="$("${COMPOSE[@]}" exec -T proxy id -u 2>/dev/null || true)"
case "$uid" in
  65532) echo "PASS: container runs as UID 65532 (#19)" ;;
  0) echo "WARN: image still runs as root — non-root assertion deferred until #19 (Dockerfile USER) lands" ;;
  *) fail "unexpected container UID '$uid'" ;;
esac

echo "==> tool-schema relay over /mcp (#178, deferred hard assert)"
# The proxy writes its key as root with 0600 perms (until #19 changes the
# container user), so extract it through exec into a runner-writable temp
# file rather than reading the container filesystem directly.
key="$(mktemp)"
"${COMPOSE[@]}" exec -T proxy cat /data/private_key.pem > "$key"
if [ ! -s "$key" ]; then
  fail "proxy did not generate /data/private_key.pem"
fi
# The proxy normalizes EXTERNAL_URL to a trailing slash (main.go), and
# validates iss/aud against that exact string — mint with "$BASE/".
token="$(go run "$REPO_ROOT/e2e/tokengen" -key "$key" -iss "$BASE/")"
rm -f "$key"

resp="$(curl -fsS -X POST "$BASE/mcp" \
  -H "Authorization: Bearer $token" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')"
echo "$resp" | jq -e '.result.tools[] | select(.name == "with_defs")' >/dev/null \
  || fail "with_defs fixture tool missing from tools/list"

schema="$(echo "$resp" | jq -c '.result.tools[] | select(.name == "with_defs") | .inputSchema')"
if echo "$schema" | jq -e 'has("definitions")' >/dev/null; then
  echo "PASS: with_defs schema relayed verbatim ('definitions' preserved)"
else
  echo "WARN: with_defs schema was rewritten (no 'definitions' key) — expected until #18 (upstream PR #179 port) lands"
fi

echo "==> e2e smoke OK"
