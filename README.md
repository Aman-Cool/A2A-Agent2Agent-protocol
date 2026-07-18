<div align="center">

<img src="https://readme-typing-svg.demolab.com?font=Fira+Code&weight=500&size=22&pause=1200&center=true&vCenter=true&width=640&lines=Teaching+an+MCP+gateway+to+speak+Agent2Agent;One+policy+plane+for+MCP+%2B+A2A+traffic;Discover+%2F+route+%2F+secure+inter-agent+tasks" alt="Teaching an MCP gateway to speak Agent2Agent" />

# A2A Protocol Support : Exploration Fork

[![Design Doc](https://img.shields.io/badge/design%20doc-Kuadrant%20%231114-8A2BE2?logo=github)](https://github.com/Kuadrant/mcp-gateway/pull/1114)
[![Tracking Issue](https://img.shields.io/badge/upstream%20issue-%23766-blue?logo=github)](https://github.com/Kuadrant/mcp-gateway/issues/766)
[![Test Server](https://img.shields.io/badge/test%20server-Kuadrant%20%231200-2ea44f?logo=github)](https://github.com/Kuadrant/mcp-gateway/pull/1200)
[![A2A Spec](https://img.shields.io/badge/A2A-v1.0%20target-orange)](https://a2a-protocol.org/latest/specification/)
[![LFX Mentorship](https://img.shields.io/badge/LFX%20Mentorship-2026%20Term%202-0094FF)](https://mentorship.lfx.linuxfoundation.org/)

</div>

> [!IMPORTANT]
> This is a **workshop fork** of [Kuadrant/mcp-gateway](https://github.com/Kuadrant/mcp-gateway) (the original project README lives [there](https://github.com/Kuadrant/mcp-gateway#readme)). The agreed home for this work is upstream.., the design lands via [#1114](https://github.com/Kuadrant/mcp-gateway/pull/1114), and code upstreams incrementally once proven here. The fork exists so A2A exploration can move fast without carrying MCP regression risk into the main repo ; workshop in the fork, home in-tree.

This fork is where I'm prototyping Agent2Agent (A2A) protocol support for Kuadrant's MCP Gateway, as part of the CNCF LFX mentorship *"Prototype A2A protocol support in the agentic gateway"* (2026 Term 2), mentored by the Kuadrant maintainers. Everything here traces back to an upstream artifact : the design doc, the test server, the review threads and this README is the map.

## The problem, in one paragraph

The MCP Gateway handles the **vertical axis** of agentic workloads.., a single client consuming federated tools from many upstream MCP servers, with Kuadrant's AuthPolicy, RateLimitPolicy, and observability wrapped around every call. But as agentic architectures grow, a **horizontal axis** emerges: agents delegating long-running work to *other agents*, discovering peer capabilities, coordinating over tasks that run for seconds or days. That's what the [A2A protocol](https://a2a-protocol.org) standardizes ; and today, that traffic bypasses the gateway entirely. No auth, no rate limits, no discovery, no audit trail. Every agent-to-agent delegation is a direct connection outside the policy perimeter. This project puts it back inside.

```mermaid
flowchart LR
    subgraph vertical["MCP — the vertical axis (today)"]
        C1[Client] -->|tools/call| G1[Gateway]
        G1 --> S1[MCP Server A]
        G1 --> S2[MCP Server B]
    end
    subgraph horizontal["A2A — the horizontal axis (this work)"]
        A1[Agent] -->|SendMessage| G2[Gateway]
        G2 --> A2[Weather Agent]
        G2 --> A3[Search Agent]
        A2 -.long-running task.-> A1
    end
```

## A2A in two minutes

A2A is an open protocol (originally Google, donated to the Linux Foundation, now at **v1.0**) for communication between *opaque* agents — agents that collaborate without sharing internal memory, tools, or logic. The pieces that matter for a gateway:

- **Agent Cards** .., a JSON manifest served at a well-known path describing what an agent can do (skills), how to reach it (`url` / `supportedInterfaces`), and how to authenticate (`securitySchemes`). Discovery is card-driven: a client reads the card and sends work to whatever URL it advertises.
- **Tasks** .., the unit of work. A `SendMessage` creates a task that moves through a lifecycle (`submitted → working → completed/failed/canceled`, with `input-required` and `auth-required` detours) and may outlive the request that created it by hours or days. Clients poll with `GetTask`, cancel with `CancelTask`.
- **Streaming** .., `SendStreamingMessage` subscribes the client to real-time task updates over SSE, carrying multi-modal artifacts (text, files, structured data) as they're produced ; `SubscribeToTask` reconnects a dropped stream.
- **It complements MCP, not competes** : MCP standardizes agent-to-*tool*, A2A standardizes agent-to-*agent*. A gateway that already routes one is halfway to routing both.

<details>
<summary><b>v0.3.0 -> v1.0: what changed (and why this fork targets v1.0)</b></summary>

<br>

The spec moved under us mid-project, and the maintainer review agreed there's little value building against an already-superseded line. The v1.0 deltas we've verified against the spec repo:

| Surface | v0.3.0 | v1.0 |
|---|---|---|
| Send | `message/send` | `SendMessage` (blocking by default) |
| Stream | `message/stream` | `SendStreamingMessage` |
| Task fetch / cancel | `tasks/get` / `tasks/cancel` | `GetTask` / `CancelTask` (+ `ListTasks`) |
| Resubscribe | `tasks/resubscribe` | `SubscribeToTask` |
| Well-known card path | `/.well-known/agent-card.json` | `/.well-known/agent-card.json` (unchanged) |
| Card endpoint field | top-level `url` | `supportedInterfaces[]` (+ `tenant` for multi-agent hosting) |
| Security fields | — | camelCase JSON: `securitySchemes` map, `securityRequirements` |
| Card integrity | unsigned | JWS signatures over the JCS-canonicalized card |
| Canonical definition | JSON schema | protobuf (`a2a.proto`) ; JSON-RPC binding uses PascalCase methods |

The architecture is version-agnostic — routing, task-ID mapping, CRD, policy attachment all survive the rename. The version-specific surface (method names, well-known path, card shape) is isolated behind one mapping so the version is never load-bearing. That's the design's answer to a fast-moving spec.

</details>

## Why a gateway should carry this traffic

The durable value isn't protocol plumbing.., it's that inter-agent traffic picks up the same **Kuadrant policy plane** MCP traffic already has, with zero gateway code per policy: AuthPolicy (OIDC/JWT via Authorino) for who may talk to which agent, RateLimitPolicy (Limitador) for how often, OpenTelemetry traces stitching a task's whole lifecycle across requests, and centralized discovery so clients never need upstream addresses. Kuadrant policies attach to Gateway API HTTPRoutes ; that one fact drives most of the design below.

## How it works

Two flows carry the whole story. Discovery, the broker serves each registered agent's signed card **verbatim** and advertises the gateway path through an RFC 9727 catalog, so *unmodified* A2A clients route through the gateway without the card's signature ever being touched:

```mermaid
sequenceDiagram
    participant Client
    participant Gateway as Gateway (Envoy)
    participant Broker
    participant Agent as Upstream Agent

    Client->>Gateway: GET /.well-known/api-catalog
    Gateway->>Broker: (RFC 9727 catalog)
    Broker-->>Client: links: [/a2a/mcp-test/weather, /a2a/mcp-test/search]
    Client->>Gateway: GET /a2a/mcp-test/weather/.well-known/agent-card.json
    Broker->>Agent: periodic card refresh (ticker, conditional GET)
    Note over Broker: serve the signed card verbatim<br/>(a rewrite would void the JWS signature)
    Broker-->>Client: AgentCard (signature intact) — catalog link routes to the gateway
```

And invocation.., the ext_proc router detects A2A by path prefix and routes to the right upstream. The path carries the routing, so the agent's own task IDs pass through unchanged — the router records `(agent, task ID) → principal` for ownership and tracing, but never rewrites what the client sees:

```mermaid
sequenceDiagram
    participant Client
    participant Envoy
    participant Router as ext_proc Router
    participant Agent as Upstream Agent

    Client->>Envoy: POST /a2a/mcp-test/weather {method: SendMessage}
    Envoy->>Router: RequestHeaders (:path = /a2a/mcp-test/weather)
    Note over Router: detect A2A by path prefix<br/>resolve agent, set :authority
    Envoy->>Router: RequestBody
    Note over Router: authenticate the principal (sub)
    Envoy->>Agent: forward
    Agent-->>Envoy: 200 {result: {id: task-abc}}
    Envoy->>Router: ResponseBody
    Note over Router: record (agent, task-abc) → principal<br/>id passed through, not rewritten
    Envoy-->>Client: {result: {id: task-abc}}
```

## How we got here

```mermaid
timeline
    title From PoC to workshop
    May 2026 : Working PoC — federated agent-card broker (upstream PR 986, since superseded)
    Early June : Design doc opened upstream (PR 1114) with the open questions flagged for mentors
    Mid June : Line-by-line v0.3.0 spec pass — message/send carries NO skill field : pivot to path-per-agent routing + card url rewriting
    Late June : A2A test server built (PR 1200) — SSE keepalives, heavy multi-modal artifacts, enforced auth modes, deterministic task states
    June 29 : Nine-point maintainer review — v1.0 target, tenant field, signed cards, fork workflow
    July 1 : Revised design up ; two OPEN decisions remain (v1.0 confirm, namespace-qualified paths)
    July 2 : This fork opens for business — spike 1, per-method response ModeOverride : Verified same day against real Envoy — BUFFERED + STREAMED honored mid-request, content-length constraint found + recorded
    July 5 : CRD + controller merged (PR 3) — 56/56 envtest specs, live-verified on Kind, full upstream CI green : Cross-namespace registration gated behind ReferenceGrant consent ; revoking a grant withdraws the config, not just the status
    July 7 : Both open design questions resolved by the maintainers — v1.0 is the target, and paths are namespace-qualified (/a2a/{namespace}/{prefix}) to kill cross-namespace collisions : design doc migrated to the v1.0 surface (method names, well-known path, JWS-signed cards served verbatim)
    July 8 : Design doc corrected to the exact v1.0 wire (well-known path, camelCase fields, streaming envelopes) : A2A test server migrated to the v1.0 ProtoJSON surface — PascalCase methods, TASK_STATE_* enums, flat parts, StreamResponse envelopes — every field verified against the canonical proto
    Mid July : Fork housekeeping merged — credential-rotation controller tests (#14) and the credential-label doc (#15) : upstream's api/v1 gateway promotion adopted via a sync PR (#20), A2A kept at v1alpha1 (the right lifecycle for a new surface)
    July 12 : Design doc sharpened for review — explicit signed-card discovery contract (verbatim + fail-closed), RFC 9264 linkset shape, session model decoupled from MCP's stateless cut ; review requested from David : discovery steel thread underway (#19) — pluggable card store + runtime a2aAgents config layer landed, broker + catalog next
    Mid July : Discovery steel thread completed — card manager (ticker + conditional GET + SHA-256, stale-on-error), verbatim card serving, RFC 9727 catalog, binary + gateway-route wiring : controller hardened — within-namespace agentPrefix collision (deterministic oldest-wins), multi-namespace fan-out coverage, external-agent via Hostname backendRef
    July 17 : Discovery runs end to end on Kind — register an agent, catalog lists it on a hot reload, the card is served byte-for-byte identical to upstream (verbatim/JWS-safe), MCP untouched throughout ; captured as a one-command demo : task-ID model settled with David — path-per-agent already carries the routing, so IDs pass through and the risky body rewrite is dropped
```

The pivot in the middle is the story worth telling: the original design routed by reading a `skill` out of the `message/send` body, and the spec pass revealed that field **doesn't exist** — `MessageSendParams` is `{message, configuration, metadata}`, skills live only in the card. So routing moved to a path per agent (`/a2a/{namespace}/{prefix}`), which is also what [agentgateway](https://agentgateway.dev) converged on, and which turns out to be Kuadrant-optimal anyway.., policies attach to HTTPRoutes, and a path per agent means an *operator can attach a distinct AuthPolicy and RateLimitPolicy per agent*. The protocol forced a change that made the design better.

## Where everything lives

| Workstream | Where | State |
|---|---|---|
| Design doc (routing, CRD, card serving, auth, task store) | [Kuadrant#1114](https://github.com/Kuadrant/mcp-gateway/pull/1114) | in review — every review point + the v1.0 migration reflected ; signed-card contract, catalog shape and session scope sharpened ; **review requested from David**, CI green ; one deferred item (discovery convention) |
| A2A test server (e2e target) | [Kuadrant#1200](https://github.com/Kuadrant/mcp-gateway/pull/1200) | **v1.0 migration done** — full ProtoJSON wire, proto-verified against `a2a.proto@v1.0.1` ; draft, held behind #1114 |
| Original PoC (federated card broker) | [Kuadrant#986](https://github.com/Kuadrant/mcp-gateway/pull/986) | closed... pre-pivot, superseded by the design |
| Spike 1 — per-method response ModeOverride | [this fork, PR #1](../../pull/1) | **merged** : verified against real Envoy, BUFFERED + STREAMED both honored mid-request ; surfaced the content-length constraint (recorded in the design doc) |
| CRD + controller (`A2AAgentRegistration`) | [this fork, #3 + hardening](../../pulls?q=is%3Apr+is%3Amerged) | **merged** : 56/56 envtest specs, live-verified on Kind, consent-gated cross-namespace with revocation withdrawal ; hardened — within-namespace prefix collision (#8), multi-namespace fan-out coverage (#5), external-agent via Hostname backendRef (#6) |
| Upstream sync + fork hygiene | [this fork, #20 / #14 / #15](../../pulls?q=is%3Apr+is%3Amerged) | **merged** : adopted upstream's api/v1 gateway promotion (A2A stays v1alpha1) ; credential-rotation controller tests + the credential-label doc |
| Discovery steel thread (card cache + catalog + wiring) | [this fork, #19/#21/#28/#22](../../pulls?q=is%3Apr+is%3Amerged) | **merged + live** : pluggable card store, runtime `a2aAgents` config, `A2AAgentManager` (ticker + conditional GET + SHA-256, stale-on-error, refresh-on-change), verbatim card serving, RFC 9727 catalog, binary + gateway-route wiring |
| End-to-end demo | [this fork, PR #31](../../pull/31) | **merged** : `demos/a2a-discovery/demo.sh` — register → catalog (hot reload) → card byte-identical to upstream → deregister → MCP regression ; verified live on Kind |
| Router (invocation + task store) | this fork, next up | **unblocked** : namespace-qualified routing, per-request auth, task-ID **passthrough** (settled with David — the path carries routing, no ID rewrite), ownership record for `GetTask`/`CancelTask` |
| Stretch + mentor-gated backlog | [issues](../../issues) | deferred scope, each with its why.., plus two follow-ups the live run surfaced (#27 fail-closed card check, #30 refresh-on-change ✓) |

## The plan

```mermaid
gantt
    title Twelve weeks, three phases
    dateFormat YYYY-MM-DD
    section Phase 1 — design
    Design doc + gap analysis (1114)      :done,   p1a, 2026-06-01, 2026-06-27
    A2A test server (1200)                :done,   p1b, 2026-06-20, 2026-06-28
    section Phase 2 — build (fork)
    Spike ModeOverride                    :done,   p2a, 2026-07-01, 2026-07-02
    CRD + controller                      :done,   p2b, 2026-07-02, 2026-07-05
    Upstream api/v1 sync                  :done,   p2s, 2026-07-12, 1d
    Broker card serving + catalog         :done,   p2c, 2026-07-12, 2026-07-17
    Discovery live + demo                 :done,   p2e, 2026-07-17, 1d
    Router routing + task-ID passthrough  :active, p2d, 2026-07-17, 2026-08-04
    section Phase 3 — prove
    SSE streaming passthrough             :        p3a, 2026-08-04, 2026-08-14
    E2E suite + upstreaming               :        p3b, 2026-07-18, 2026-08-24
```

- [x] Analysis of A2A vs MCP traffic patterns (request/response vs long-running tasks, push, multi-modal artifacts)
- [x] Design doc: ext_proc routing, federated card serving, session implications, CRD design
- [x] Deterministic A2A test server for e2e
- [x] Spike: mid-request response mode change (the one piece the review flagged as *"haven't seen it done before... good to derisk early"*) — verified, works ; one constraint found and recorded
- [x] `A2AAgentRegistration` CRD + controller (config fan-out per gateway namespace); merged ahead of plan ; immutable identity fields, ReferenceGrant-gated cross-namespace, revocation withdraws config
- [x] Broker: card cache behind a pluggable interface, RFC 9727 catalog endpoint — *merged and running end to end on Kind ([demo](demos/a2a-discovery/demo.sh))*
- [ ] Router: namespace-qualified path-per-agent routing, per-request auth, task-ID passthrough with a `(agent, id) → principal` ownership record
- [ ] E2E: discovery, task execution, streaming, auth, MCP regression — *discovery specs unblocked now (the demo is the skeleton)*

If the schedule slips, the must-have order is CRD/controller -> card serving -> routing -> e2e ; streaming passthrough and metrics defer first.

## Design decisions, and why

<details>
<summary><b>1 : Path-per-agent routing, not skill dispatch</b></summary>

<br>

The protocol never routes by skill... no `skill` field exists in the send request (both spec versions). The two honest options were a path per agent or a custom header ; a header only works for clients we've specifically taught about the gateway, while a path works for *any* stock A2A client, because clients already POST to whatever URL discovery advertises. The catalog the gateway serves points at `/a2a/{namespace}/{prefix}`; so the routing key lives in discovery, not in anything the client has to be told. The path is **namespace-qualified** (confirmed by the maintainers) so two agents sharing a prefix across different namespaces can never collide. It's also what agentgateway (Linux Foundation) does, and it gives each agent its own HTTPRoute for per-agent policy attachment.

</details>

<details>
<summary><b>2 : Serve signed cards verbatim, route by path — don't rewrite the card</b></summary>

<br>

The obvious move was to rewrite the served card's `url` to the gateway path, so an unmodified client reading the card routes back through the gateway rather than talking *directly* to the agent and silently bypassing the policy perimeter (no AuthPolicy, no rate limits, no logs). That works right up until v1.0: cards can carry JWS signatures over the JCS-canonicalized card, and the signature covers the URL — rewriting the card invalidates its signature.

So with v1.0 now the confirmed target, the design **serves signed cards verbatim** and moves the routing key out of the card entirely: the RFC 9727 catalog advertises the gateway endpoint (`/a2a/{namespace}/{prefix}`), and v1.0's `tenant` field — which exists precisely for multiple agents behind one endpoint — carries the per-agent selector. The one residual dependency, that clients discover via the catalog rather than the card's own interface URL, is stated in the design with its two clean resolutions rather than hand-waved.

</details>

<details>
<summary><b>3 : Task IDs pass through — the gateway doesn't own them</b></summary>

<br>

The first design had the gateway mint its own task ID and rewrite the upstream's out of every response — the same way it rewrites MCP session IDs. A maintainer's question unpicked it : that only earns its keep if the gateway routes *by* the task ID, and it doesn't — path-per-agent already carries the routing, so a `GetTask` is addressed to `/a2a/{namespace}/{prefix}` and the agent is resolved from the path. The ID only has to be unique *within* an agent, which the agent guarantees. And the MCP-session parallel doesn't transfer : the gateway owns session IDs because one client session fans out across several backends — a task lives on exactly one agent, no fan-out, nothing to multiplex.

So task IDs pass through unchanged. The gateway keeps an internal `(agent, id) → principal` record so `GetTask`/`CancelTask` can verify the caller owns the task, and so a task's whole lifecycle correlates in traces — but it never rewrites what the client sees. The payoff : the single riskiest piece of the build — the buffered/SSE task-ID body rewrite — simply disappears. A maintainer's question made the design smaller.

</details>

<details>
<summary><b>4 : The spike that let us cut the rewrite (mid-request response mode)</b></summary>

<br>

Before the task-ID model settled, rewriting IDs meant flipping Envoy's response body mode **mid-request** — `BUFFERED` to rewrite a whole non-streaming body, `STREAMED` to touch SSE chunks as they arrive — and since the method is only known at the request-body phase, the flip has to happen at response-headers via ext_proc `ModeOverride`. That was the review's flagged unknown (*"haven't seen it done before... good to derisk early"*), so it became spike 1 : verified against real Envoy (Istio 1.27), both directions honored mid-request, and it surfaced one constraint — a buffered rewrite changes the body length, so `content-length` must be stripped in the same response, or Envoy fails closed. Then decision #3 dropped the task-ID rewrite — but not the mode flip itself : the filter's default response body mode is `NONE`, so the router only sees a response body when it asks for one, and binding a task to its creator means reading the agent's task ID out of the `SendMessage` response. So the override stays on the critical path as a read-only tap — `BUFFERED` to observe, never to mutate — and since nothing changes the body, the content-length constraint the spike surfaced never applies ; that half sits in reserve for any future body rewrite (the push-notification relay, response filtering). De-risking early paid off twice : the confidence to choose passthrough knowing the harder path was viable, and the exact mechanism the ownership record now rides on. Transcripts in [PR #1](../../pull/1).

</details>

<details>
<summary><b>5 : Two auth paths that must never mix</b></summary>

<br>

Card fetching (broker → agent, no client involved) uses the registration's `credentialRef`; a static credential the router can never see. Task invocation (a real client behind every call) forwards the *client's* identity; bearer pass-through or, recommended, RFC 8693 token exchange re-audienced to the agent via Authorino. Injecting the gateway's static credential into client calls would be the classic confused-deputy: the agent loses the caller's identity and a low-privilege client rides the gateway's credential. Same split MCP already enforces, for the same reason.

</details>

<details>
<summary><b>6 : Cross-namespace registration needs the route namespace's consent</b></summary>

<br>

Being able to create a registration in namespace A is not permission to expose namespace B's agent through the gateway — that would let a tenant register another tenant's backend with no signal and no veto. So a cross-namespace `targetRef` requires a `ReferenceGrant` in the route's namespace (`from: A2AAgentRegistration`, `to: HTTPRoute`), the Gateway API's own consent primitive and the same model the extension controller already uses.., a boundary the maintainers held firm on for the sibling MCP fix, adopted here from day one. The controller watches grants, so consent takes effect within a reconcile in both directions ; and crucially, revoking a grant *withdraws the agent's config*, not just the status, everywhere else config is last-known-good on failure (a transient error must never rip a live agent out of the data plane), but consent is an explicit state, and consent withdrawn means exposure withdrawn. Identity fields (`agentPrefix`, `targetRef`) are immutable by CEL for the same reason: a retarget across gateways would require cleaning stale namespace fan-out config from the previous target, so replacing an agent means replacing the registration.., blue/green swaps happen at the HTTPRoute's `backendRef`, which the controller watches.

</details>

## Reading list

The primary sources this work leans on : the [A2A specification](https://a2a-protocol.org/latest/specification/) and [what's new in v1.0](https://a2a-protocol.org/latest/whats-new-v1/) ; [RFC 9727](https://www.rfc-editor.org/rfc/rfc9727) (api-catalog well-known URI) and [RFC 9264](https://www.rfc-editor.org/rfc/rfc9264) (Linkset) for discovery ; [agentgateway](https://agentgateway.dev) as prior art for route-per-agent (it rewrites cards ; we serve them verbatim) ; the upstream [design doc](https://github.com/Kuadrant/mcp-gateway/pull/1114) where the decisions above are argued in full ; and Kuadrant's own [MCP Gateway docs](https://docs.kuadrant.io) for the platform this extends.

---

<div align="center">

Everything here is headed upstream to [Kuadrant/mcp-gateway](https://github.com/Kuadrant/mcp-gateway) if any of it interests you, the design discussion on [#1114](https://github.com/Kuadrant/mcp-gateway/pull/1114) is the room where it's happening, and pushback is genuinely welcome 🙂

</div>
