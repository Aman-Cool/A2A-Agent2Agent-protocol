#!/usr/bin/env bash
#
# A2A federated agent discovery through the MCP Gateway.
#
# Shows an unmodified A2A client discovering and fetching agent cards through the gateway,
# with no A2A knowledge baked into the client — the gateway serves an RFC 9727 API Catalog
# and per-agent AgentCards from a broker-side cache, verbatim so signatures survive.
#
# Prereq: `make local-env-setup` (Kind + Istio + broker/router + controller), plus the A2A
# test server image loaded into Kind — `make kind-load-test-servers` (or `make
# deploy-test-servers`). Step 0 stands the server up and fails fast if the image is missing.
#
set -euo pipefail

GW="${GW:-http://mcp.127-0-0-1.sslip.io:8001}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$DIR/../.." && pwd)"
CATALOG="$GW/.well-known/api-catalog"
CARD="$GW/a2a/mcp-test/weather/.well-known/agent-card.json"

banner() { printf '\n\033[1;34m== %s ==\033[0m\n' "$1"; }
pause()  { [ "${PAUSE:-1}" = "1" ] && { printf '\033[2m(press enter)\033[0m'; read -r; } || true; }

# wait_for DESC TIMEOUT CMD... : poll CMD until it succeeds, or fail the demo after TIMEOUT.
wait_for() {
  local desc="$1" timeout="$2"; shift 2
  local deadline=$(( $(date +%s) + timeout ))
  until "$@"; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      printf '\033[1;31mtimed out after %ss waiting for %s\033[0m\n' "$timeout" "$desc"; exit 1
    fi
    sleep 2
  done
}
card_ready()    { [ "$(curl -s -o /dev/null -w '%{http_code}' "$CARD")" = "200" ]; }
catalog_lists() { curl -fsS "$CATALOG" 2>/dev/null | grep -q weather; }
catalog_clear() { ! curl -fsS "$CATALOG" 2>/dev/null | grep -q weather; }

PF=""
cleanup() { [ -n "$PF" ] && kill "$PF" 2>/dev/null || true; }
trap cleanup EXIT

banner "Step 0 — the A2A test server is running behind the gateway"
kubectl apply -n mcp-test -f "$REPO/config/test-servers/a2a-server-deployment.yaml" \
                          -f "$REPO/config/test-servers/a2a-server-service.yaml" \
                          -f "$REPO/config/test-servers/a2a-server-httproute.yaml"
if ! kubectl rollout status -n mcp-test deployment/a2a-test-server --timeout=120s; then
  printf '\033[1;31ma2a-test-server did not become ready — is its image loaded into Kind?\n'
  printf 'run: make kind-load-test-servers  (or make deploy-test-servers), then re-run this demo\033[0m\n'
  exit 1
fi
pause

banner "Step 1 — baseline: the API Catalog is empty (no agents registered yet)"
curl -fsS "$CATALOG" | jq .
pause

banner "Step 2 — an operator registers an agent (one CRD, no gateway restart)"
cat "$DIR/a2aagentregistration.yaml"
kubectl apply -f "$DIR/a2aagentregistration.yaml"
kubectl wait --for=condition=Ready --timeout=60s \
  a2aagentregistration/weather-agent -n mcp-test
pause

banner "Step 3 — the catalog now lists the agent (hot config reload)"
# the agent enters the catalog once its card is fetched and validated, not merely on registration
wait_for "the agent to enter the catalog" 60 catalog_lists
curl -fsS "$CATALOG" | jq .
pause

banner "Step 4 — a client fetches the agent card through the gateway ..."
# the card is fetched from upstream on the broker's refresh cycle; wait for it
wait_for "the agent card to be served" 60 card_ready
curl -fsS "$CARD" | jq .

banner "     ... and it is byte-identical to the upstream card (served verbatim)"
kubectl -n mcp-test port-forward svc/a2a-test-server 19090:9090 >/dev/null 2>&1 &
PF=$!; sleep 3
if diff <(curl -fsS "$CARD") <(curl -fsS http://localhost:19090/.well-known/agent-card.json) >/dev/null; then
  printf '\033[1;32mBYTE-IDENTICAL — verbatim serving, JWS signature preserved\033[0m\n'
else
  printf '\033[1;31mcards differ!\033[0m\n'; exit 1
fi
kill "$PF" 2>/dev/null || true; PF=""
pause

banner "Step 5 — deregister the agent; the catalog empties"
kubectl delete a2aagentregistration/weather-agent -n mcp-test
wait_for "the catalog to empty" 30 catalog_clear
curl -fsS "$CATALOG" | jq .
pause

banner "Step 6 — MCP is entirely unaffected: tools/list still works through the same gateway"
SID=$(curl -s -D - -o /dev/null "$GW/mcp" \
  -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"demo","version":"1"}}}' \
  | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r')
curl -s "$GW/mcp" \
  -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | tr -d '\r' | grep -oE '"name":"[a-z_]+"'

banner "done"
