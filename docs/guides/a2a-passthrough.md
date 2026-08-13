# A2A Passthrough (Experimental)

The MCP Gateway can bring Agent-to-Agent (A2A) traffic into the same Kuadrant policy
plane as MCP. When enabled, the router lifts A2A protocol metadata off `/a2a` traffic
into request headers — the agent (from the path) and the JSON-RPC method (from the body)
— so that Istio Telemetry and Kuadrant AuthPolicy can audit, observe and authorize
inter-agent calls with no gateway code per policy.

> **Note:** This is an experimental, opt-in feature. It is off by default, and no A2A
> behaviour exists until you enable it. In this phase the gateway does **not** route A2A
> traffic or manage agent registration — you author the HTTPRoutes yourself. The flag and
> behaviour may change as later phases land.

## What the router does

With the feature enabled, for any request whose path begins with `/a2a/`:

- It strips any client-supplied `x-a2a-agent` / `x-a2a-method` headers, then sets
  `x-a2a-agent` to the first path segment after `/a2a/` (the agent identity).
- For POST requests it parses the JSON-RPC envelope and sets `x-a2a-method`, normalized to
  a bounded set — the known v1 methods (`SendMessage`, `SendStreamingMessage`, `GetTask`,
  `CancelTask`, `SubscribeToTask`) verbatim, and anything else as `other` — so the value is
  safe to use as a metric label. An unparseable body is rejected at the router with a
  JSON-RPC `-32700` error and never reaches the agent.
- Everything else passes through untouched. The request is carried to the agent by your own
  HTTPRoute, not by the gateway.

Because these headers are router-derived and stripped from client input, a client cannot
forge the values that policy and telemetry key on.

## Prerequisites

- The MCP Gateway is installed and a gateway is running.
- You have one or more A2A agents reachable in the cluster.

## Step 1: Enable the feature

Add the `--enable-a2a` flag to the broker-router deployment:

```bash
kubectl patch deployment mcp-gateway -n mcp-system --type='json' \
  -p='[{"op": "add", "path": "/spec/template/spec/containers/0/command/-", "value": "--enable-a2a"}]'
kubectl rollout status deployment/mcp-gateway -n mcp-system
```

Verify the flag is set:

```bash
kubectl get deployment mcp-gateway -n mcp-system \
  -o jsonpath='{.spec.template.spec.containers[0].command}' | tr ',' '\n' | grep enable-a2a
```

## Step 2: Route an agent through the gateway

Author an HTTPRoute that matches the agent's `/a2a/{agent}` path and forwards to its
backend. The first path segment is the agent identity that becomes `x-a2a-agent`. The
route must attach to the same gateway listener the MCP Gateway extension targets.

```bash
kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: weather-agent-route
  namespace: mcp-test
spec:
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: mcp-gateway
      namespace: gateway-system
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /a2a/weather
      backendRefs:
        - name: weather-agent
          port: 9090
EOF
```

A `SendMessage` to `/a2a/weather` now traverses the router — which sets
`x-a2a-agent: weather` and `x-a2a-method: SendMessage` — before your route forwards it to
the `weather-agent` backend.

## Step 3: Authorize per agent

Attach an AuthPolicy that authenticates the bearer and authorizes on the router-set
`x-a2a-agent` header, analogous to MCP's per-tool authorization. Scope authentication to
POST so that public agent-card `GET` discovery is not gated.

```bash
kubectl apply -f - <<'EOF'
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: a2a-auth-policy
  namespace: gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
  rules:
    authentication:
      'sso-server':
        when:
          - predicate: "request.method == 'POST'"
        jwt:
          issuerUrl: https://keycloak.example.com/realms/agents
    authorization:
      'agent-access-check':
        when:
          - predicate: "request.method == 'POST'"
          - predicate: "request.headers.exists(h, h == 'x-a2a-agent')"
        patternMatching:
          patterns:
            - predicate: |
                ('agent:' + request.headers['x-a2a-agent']) in
                (has(auth.identity.resource_access) ? auth.identity.resource_access['a2a'].roles : [])
EOF
```

A client whose token grants the `agent:weather` role reaches the agent; one without it gets
a 403 before the request leaves the gateway. You can also rate-limit A2A traffic with a
`RateLimitPolicy` keyed on `x-a2a-agent` the same way.

## Step 4: Audit and observe

Surface the agent and method in the Istio access log so A2A invocations appear keyed by
agent and method. This example adds them as tags on the gateway's access log:

```bash
kubectl apply -f - <<'EOF'
apiVersion: telemetry.istio.io/v1
kind: Telemetry
metadata:
  name: a2a-access-log
  namespace: gateway-system
spec:
  accessLogging:
    - providers:
        - name: envoy
      filter:
        expression: "request.url_path.startsWith('/a2a/')"
EOF
```

The router-set `x-a2a-agent` and `x-a2a-method` headers are available to any Telemetry or
policy that reads request headers; `x-a2a-method`'s bounded value set keeps it safe as a
metric dimension.

## Trust boundaries in this phase

- **Task isolation is the agent's responsibility.** The gateway forwards the client's bearer,
  so agents can authenticate callers (for example, the `a2a-go` task store exposes an
  `Authenticator` hook), but the gateway does not yet bind tasks to a principal. Gateway-side
  task ownership arrives in a later phase.
- **Agents should advertise their gateway URL.** An agent fronted by the gateway should
  advertise its gateway path in its Agent Card and not be independently reachable — otherwise
  a client that reads the card could follow it straight to the agent, bypassing the gateway
  and the policy this feature applies.

## Next steps

- [Authorization](./authorization.md) — the per-capability authorization pattern this builds on
- [Auditing MCP Tool Calls](./auditing.md) — the Istio Telemetry access-log approach in depth
