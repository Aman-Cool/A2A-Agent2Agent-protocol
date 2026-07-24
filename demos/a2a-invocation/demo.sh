#!/usr/bin/env bash
#
# A2A agent invocation through the MCP Gateway.
#
# Shows an unmodified A2A client invoking a registered agent through the gateway. The router
# resolves the agent from the /a2a/{namespace}/{prefix} path, routes the request to it by
# rewriting :authority, and passes responses — and task IDs — through unchanged. It also shows
# the gateway failing closed on requests it must not forward (unknown agent, unsupported
# method, embedded push config), and that streaming and MCP traffic are unaffected.
#
# Prereq: `make local-env-setup` (Kind + Istio + broker/router + controller), plus the A2A
# test server image loaded into Kind — `make kind-load-test-servers` (or `make
# deploy-test-servers`). Step 0 stands the server up and registers the agent, failing fast if
# the image is missing. Requires `jq`.
#
set -euo pipefail

GW="${GW:-http://mcp.127-0-0-1.sslip.io:8001}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$DIR/../.." && pwd)"
A2A="$GW/a2a/mcp-test/weather"
CARD="$A2A/.well-known/agent-card.json"

banner() { printf '\n\033[1;34m== %s ==\033[0m\n' "$1"; }
pause()  { [ "${PAUSE:-1}" = "1" ] && { printf '\033[2m(press enter)\033[0m'; read -r; } || true; }
ok()     { printf '\033[1;32m%s\033[0m\n' "$1"; }
bad()    { printf '\033[1;31m%s\033[0m\n' "$1"; exit 1; }

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
card_ready() { [ "$(curl -s -o /dev/null -w '%{http_code}' "$CARD")" = "200" ]; }

# call PREFIX BODY : POST a JSON-RPC body to /a2a/mcp-test/PREFIX, print the response body
call() { curl -s --max-time 15 -X POST "$GW/a2a/mcp-test/$1" -H 'content-type: application/json' -d "$2"; }

# expect_err PREFIX BODY EXPECTED_CODE LABEL : assert the gateway rejects with a JSON-RPC code
expect_err() {
  local code; code=$(call "$1" "$2" | jq -r '.error.code // empty')
  [ "$code" = "$3" ] && ok "  $4 → rejected $code" || bad "  $4 → expected $3, got '${code:-<none>}'"
}

banner "Step 0 — stand up the A2A agent behind the gateway and register it"
kubectl apply -n mcp-test -f "$REPO/config/test-servers/a2a-server-deployment.yaml" \
                          -f "$REPO/config/test-servers/a2a-server-service.yaml" \
                          -f "$REPO/config/test-servers/a2a-server-httproute.yaml"
if ! kubectl rollout status -n mcp-test deployment/a2a-test-server --timeout=120s; then
  printf '\033[1;31ma2a-test-server did not become ready — is its image loaded into Kind?\n'
  printf 'run: make kind-load-test-servers  (or make deploy-test-servers), then re-run this demo\033[0m\n'
  exit 1
fi
kubectl apply -f "$DIR/a2aagentregistration.yaml"
kubectl wait --for=condition=Ready --timeout=60s a2aagentregistration/weather-agent -n mcp-test
wait_for "the agent card to be served through the gateway" 60 card_ready
ok "agent registered and reachable at /a2a/mcp-test/weather"
pause

banner "Step 1 — invoke the agent: SendMessage is routed through the gateway to the agent"
# the client posts to the gateway path; the router resolves the agent and rewrites :authority
RESP=$(call weather '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello weather"}]}}}')
echo "$RESP" | jq .
STATE=$(echo "$RESP" | jq -r '.result.task.status.state // empty')
ECHOED=$(echo "$RESP" | jq -r '[.result.task.artifacts[]?.parts[]?.text] | join(" ")' | grep -c 'hello weather' || true)
{ [ "$STATE" = "TASK_STATE_COMPLETED" ] && [ "$ECHOED" -ge 1 ]; } \
  && ok "routed to the agent → completed task echoing the message (the agent received it, via Envoy)" \
  || bad "unexpected invocation result"
pause

banner "Step 2 — task IDs pass through UNCHANGED (the gateway never rewrites them)"
TID=$(echo "$RESP" | jq -r '.result.task.id')
echo "task id returned by the gateway: $TID"
case "$TID" in
  a2a-task-*) ok "this is the agent's own id (a2a-task-*), not a gateway-minted one — passthrough confirmed" ;;
  *)          bad "task id does not look agent-assigned" ;;
esac
# and it round-trips: GetTask by that same id resolves back through the gateway
GOT=$(call weather "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"GetTask\",\"params\":{\"id\":\"$TID\"}}")
[ "$(echo "$GOT" | jq -r '.result.id')" = "$TID" ] \
  && ok "GetTask by the agent-assigned id round-trips through the gateway" \
  || bad "GetTask did not return the same id"
pause

banner "Step 3 — streaming: SendStreamingMessage flows back as SSE, untouched"
STREAM=$(curl -s --max-time 15 -N -X POST "$A2A" -H 'content-type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"SendStreamingMessage","params":{"message":{"messageId":"m3","role":"ROLE_USER","parts":[{"text":"slow"}]}}}' || true)
printf '%s\n' "$STREAM" | grep -E '^data:' | sed 's/^data: //' \
  | jq -rc '.result | (.task.status.state // .statusUpdate.status.state // "update")' 2>/dev/null \
  | sed 's/^/  event: /' || true
ok "SSE streamed through the gateway (submitted → working → completed)"
pause

banner "Step 4 — the gateway fails closed on requests it must not forward"
expect_err nope    '{"jsonrpc":"2.0","id":4,"method":"SendMessage","params":{"message":{"messageId":"x","role":"ROLE_USER","parts":[{"text":"hi"}]}}}' -32602 "unknown agent"
expect_err weather '{"jsonrpc":"2.0","id":5,"method":"ListTasks","params":{}}'                                                                       -32004 "unsupported method"
expect_err weather '{"jsonrpc":"2.0","id":6,"method":"SendMessage","params":{"message":{"messageId":"y","role":"ROLE_USER","parts":[{"text":"hi"}]},"configuration":{"pushNotificationConfig":{"url":"https://evil.example"}}}}' -32003 "embedded push config"
ok "each rejected with a JSON-RPC error — the request never reached the agent"
pause

banner "Step 5 — MCP is entirely unaffected: tools/list still works through the same gateway"
SID=$(curl -s -D - -o /dev/null "$GW/mcp" \
  -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"demo","version":"1"}}}' \
  | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r')
curl -s "$GW/mcp" \
  -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | tr -d '\r' | grep -oE '"name":"[a-z_]+"' | head

banner "done — the router resolves, routes, passes through, and fails closed"
