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

A client cannot inject these headers directly — the router strips any client-supplied copy
and sets its own — so a policy or access log that keys on them sees the router's value, not
one the client planted. The client still *chooses* the path segment and JSON-RPC method the
router derives them from, so it is route matching and the AuthPolicy below that constrain
which agent and method a given client is allowed to reach; the headers describe the request,
they do not by themselves authorize it.

## Prerequisites

- The MCP Gateway is installed and a gateway is running.
- You have one or more A2A agents reachable in the cluster.

## Step 1: Enable the feature

`--enable-a2a` is a command-line flag on the broker-router, not an environment variable, so
`kubectl set env` does not apply — add it to the deployment's container command. The
controller treats it as a user-managed flag and preserves it across reconciles:

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

Verify the route was accepted by the gateway:

```bash
kubectl get httproute weather-agent-route -n mcp-test \
  -o jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'
# expect: True
```

A `SendMessage` to `/a2a/weather` now traverses the router — which sets
`x-a2a-agent: weather` and `x-a2a-method: SendMessage` — before your route forwards it to
the `weather-agent` backend. You can confirm the headers reach the agent by inspecting what
the agent received, or the access log configured in Step 4.

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

Verify the policy is enforced:

```bash
kubectl get authpolicy a2a-auth-policy -n gateway-system \
  -o jsonpath='{.status.conditions[?(@.type=="Enforced")].status}'
# expect: True
```

A client whose token grants the `agent:weather` role reaches the agent; one without it gets
a 403 before the request leaves the gateway (a `POST` with no bearer, or an unauthorized
one, returns 401/403 rather than reaching the agent). You can also rate-limit A2A traffic
with a `RateLimitPolicy` keyed on `x-a2a-agent` the same way.

## Step 4: Audit and observe

The router-set `x-a2a-agent` and `x-a2a-method` headers are ordinary request headers, so any
access-log format or Telemetry that reads request headers can surface them.

Surfacing the header *values* takes two pieces. The built-in `envoy` access-log provider logs
Envoy's default fields — not arbitrary request headers — so first define a provider whose
format includes the A2A headers, exactly as the [Auditing guide](./auditing.md) describes for
the MCP headers, adding `%REQ(X-A2A-AGENT)%` and `%REQ(X-A2A-METHOD)%` to its format string.
Then attach a Telemetry that scopes access logging to the gateway and to A2A traffic, and
references that provider:

```bash
kubectl apply -f - <<'EOF'
apiVersion: telemetry.istio.io/v1
kind: Telemetry
metadata:
  name: a2a-access-log
  namespace: gateway-system
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: mcp-gateway
  accessLogging:
    - providers:
        - name: a2a-access-log-provider   # the MeshConfig provider defined above
      filter:
        expression: "request.url_path.startsWith('/a2a/')"
EOF
```

Without `targetRefs` the Telemetry would apply to every workload in `gateway-system`; scoping
it to the gateway keeps it to A2A traffic on the gateway. Because `x-a2a-method` is normalized
to a bounded value set (known v1 methods, else `other`), it is also safe to use as a metric
dimension, not just a log field.

Verify the Telemetry was accepted:

```bash
kubectl get telemetry a2a-access-log -n gateway-system -o name
# then send an A2A request and confirm the agent and method appear in the gateway's access log:
kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --tail=20 | grep a2a
```

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
