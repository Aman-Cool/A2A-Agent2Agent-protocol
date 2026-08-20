# A2A Passthrough Documentation Plan

Documentation for phase 1 of A2A support — the experimental `--enable-a2a` router
passthrough — organized by user goals. Each section maps to a guide or doc update.
Later phases (registration/discovery, task ownership) get their own plans when they
land; see [the design](../a2a-design.md#implementation-phases).

## User-Facing Guide (`docs/guides/a2a-passthrough.md`)

### When I want inter-agent A2A traffic to flow through the gateway's policy plane

When a platform engineer has A2A agents that talk to each other outside the gateway,
they want that traffic to pick up the same AuthPolicy, RateLimitPolicy and
observability that MCP traffic already has, so that inter-agent calls are no longer a
blind spot in the policy perimeter.

**Cover:**
- Enabling the experimental feature with `--enable-a2a` on the broker-router deployment
- That it is off by default, and that no A2A behaviour exists until it is set
- What the router does: on `/a2a` traffic it sets `x-a2a-agent` (from the path) and
  `x-a2a-method` (from the JSON-RPC body), and strips any client-supplied `x-a2a-*`
- That the gateway does not route A2A traffic in this phase — the operator wires their
  own HTTPRoute from the gateway to each agent

### When I want to route A2A requests to an agent through the gateway

When a platform engineer wants clients to reach an agent through the gateway rather
than directly, they want to author an HTTPRoute that matches the agent's `/a2a/{agent}`
path and forwards to the agent's backend, so that the request traverses the router and
picks up the A2A headers and any attached policy.

**Cover:**
- The `/a2a/{agent}` path convention and that the first path segment is the agent identity
- A minimal HTTPRoute example matching `/a2a/{agent}` with a backendRef to the agent
- That the route must attach to the same listener the gateway extension targets
- That in this phase there is no `A2AAgentRegistration` CRD — routes are authored by hand

### When I want per-agent authorization on A2A traffic

When a platform engineer wants to control which clients may call which agent, they want
to attach an AuthPolicy keyed on the router-set `x-a2a-agent` header, analogous to MCP's
per-tool authorization, so that authorization is enforced at the gateway with no gateway code.

**Cover:**
- An AuthPolicy example authenticating the bearer and authorizing on `x-a2a-agent`
- Scoping authentication to POST so public card GETs are not gated
- That `x-a2a-agent`/`x-a2a-method` are router-derived and stripped from client input, so
  they cannot be forged

### When I want logs and metrics for A2A traffic

When a platform engineer wants to audit and observe inter-agent traffic, they want to
surface the agent and method in Istio Telemetry, so that A2A invocations appear in access
logs and metrics keyed by agent and method.

**Cover:**
- A Telemetry example tagging access logs / metrics with `x-a2a-agent` and `x-a2a-method`
- That `x-a2a-method` is normalized to a bounded set (known v1 methods, else `other`) so it
  is safe as a metric label
- That an unparseable `/a2a` request is rejected at the router (`-32700`) and never reaches
  the agent unlabeled

### When I need to understand the trust boundaries of this phase

When a platform engineer deploys A2A passthrough, they want to know what the gateway does
and does not enforce, so that they configure their agents correctly and do not assume
guarantees the passthrough does not provide.

**Cover:**
- Task ownership is the upstream agent's responsibility in this phase — the gateway forwards
  the client's bearer, so agents can authenticate callers (e.g. the `a2a-go` task store's
  `Authenticator` hook); the gateway does not yet bind tasks to principals
- Agents fronted by the gateway should advertise their gateway URL in their card and not be
  independently reachable, or a client could follow the card straight to the agent, bypassing
  the gateway
- That this is experimental and the API/flag may change as later phases land

## Reference / other updates

- `docs/guides/README.md` — add the new guide to the index.
- No CRD reference doc in this phase (no new CRD). Command-line flag documentation for
  `--enable-a2a` lives in the guide rather than a separate reference.
