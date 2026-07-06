# A2A Protocol Support — Technical Intelligence Report
**Author:** Aman Kumar  
**Branch:** feat/a2a-design-doc  
**Date:** June 2026  
**Scope:** Cross-reference of all gathered external intelligence (Red Hat product roadmap, A2A specification
versions, David Martin's tracking issue, Kuadrant YouTube research, Linux Foundation ecosystem) against
the actual mcp-gateway codebase. Identifies alignment, gaps, risks, and actionable judgements for the
LFX mentorship implementation.

---

## 1. Executive Summary

The mcp-gateway codebase is architecturally well-suited to A2A integration. The four processing phases
of the ext_proc loop, the existing `sseRewriter` pattern, the `session.Cache` in-memory/Redis duality,
and the `config.Observer` hot-reload system provide the exact primitives the A2A design requires —
without needing to be invented from scratch.

However, five concrete risks emerged from cross-referencing external intelligence against the code:

1. **A2A spec version target is undecided.** The design doc targets v0.3.0 but the current published
   spec is v1.0.1. The method names and well-known URI differ between versions. This is the highest
   risk item and requires explicit mentor confirmation before any router code is written.

2. **CONNLINK-1057 (MCP stateless, July 28) creates a Q4 session coupling risk.** The A2A design
   reuses `mcp-session-id` JWTs for session validation. CONNLINK-1057 is removing the MCP session
   protocol, but the gateway JWT infrastructure may survive independently. This must be clarified
   before Week 8 — which is exactly when CONNLINK-1057 merges.

3. **The `skillPrefix` field name is inconsistent with path-per-agent routing.** After the routing
   approach changed from skill-dispatch to `/a2a/{prefix}`, the CRD field should be renamed `agentPrefix`
   or `prefix`. Keeping `skillPrefix` is misleading and will create friction when platform engineers
   read the API reference.

4. **CONNLINK-1025 repo split timing is unknown.** If the operator repo split happens during the
   LFX term, the A2A controller (which belongs in the operator repo) may need to target a different
   repository than the broker/router changes.

5. **A2A v1.0.1 introduces `A2A-Version` header and `/.well-known/agent-card.json`.** If we
   eventually target v1.0.1, the router's method-matching constants and the broker's well-known
   endpoint need updating. The design doc does not currently account for this.

The design doc, implementation plan, and e2e test cases are internally consistent and correctly reflect
the path-per-agent routing approach. The codebase already has all the integration points the design
requires. The primary work is additive.

---

## 2. Red Hat Product Context — Codebase Impact Analysis

### 2.1 OCPSTRAT-2799 — MCP Gateway GA (RHCL 1.5)

**What it says:** MCP Gateway is on the Red Hat Connectivity Link (RHCL) 1.5 roadmap for GA. A2A
support is explicitly OUT OF SCOPE for the current Tech Preview phase. The LFX prototype shapes what
GA can commit to.

**Codebase impact:** None directly. This is a planning signal, not a code constraint.

**Judgement:** The design doc's framing — "entirely additive, all MCP behaviour unchanged" — is exactly
correct for a Tech Preview → GA bridge. A2A cannot break any existing code path or the Red Hat GA
commitment is jeopardized. The branch strategy (separate `A2AReconciler`, `internal/a2a/` package,
new router branch at `:path` prefix) enforces this additive property at the code level.

**What this means for implementation:** Every PR in the A2A implementation must include a regression
test (the `[A2A] MCP tools/list and tools/call are unaffected by A2A changes` test case in
`e2e_test_cases.md`). This is non-negotiable from a Red Hat product standpoint.

---

### 2.2 CONNLINK-1109 — CRD Graduation to `mcp.kuadrant.io`

**What it says:** The existing CRDs (`MCPServerRegistration`, `MCPVirtualServer`, `MCPGatewayExtension`)
are planned to graduate from `v1alpha1` to `v1` within the `mcp.kuadrant.io` group. The group name
itself (`mcp.kuadrant.io`) is staying fixed — only the version changes.

**Codebase verification:**  
`api/v1alpha1/groupversion_info.go`:
```go
GroupVersion = schema.GroupVersion{Group: "mcp.kuadrant.io", Version: "v1alpha1"}
```
The group is already `mcp.kuadrant.io`. Our A2A CRD design uses `apiVersion: mcp.kuadrant.io/v1alpha1`
which is aligned.

**Codebase impact on A2A:**
- The new `A2AAgentRegistration` CRD should use `mcp.kuadrant.io/v1alpha1` — already correct in the
  design doc.
- If graduation to `v1` happens before the A2A controller lands in main, we need to use `v1` from
  the start. Currently the design assumes `v1alpha1`. This is worth raising with David.
- Controller registration in `cmd/main.go` uses:
  ```go
  mcpv1alpha1.AddToScheme(scheme)
  ```
  Adding `A2AAgentRegistration` to the same scheme package is the right move.

**Judgement:** LOW RISK. The group name alignment is already correct. The version graduation is a
rename that affects all CRDs equally. Our A2A work should not block on this.

---

### 2.3 CONNLINK-1057 — MCP Stateless Protocol (July 28, 2026)

**What it says:** MCP protocol is moving stateless. The `initialize` / `initialized` handshake is
removed. `Mcp-Session-Id` sticky routing header is removed. `server/discover` replaces capability
negotiation. `_meta` field added to every request for tool tracing. New `Mcp-Method` and `Mcp-Name`
headers for routing without body inspection.

**Codebase verification of current session dependency:**

The following call sites in `internal/mcp-router/request_handlers.go` are directly affected:

| Function | Session dependency |
|---|---|
| `HandleToolCall()` | `s.validateSession(sessionID)` — will change or be removed |
| `HandleNoneToolCall()` | passes `initialize` to broker — `initialize` is being removed |
| `initializeMCPSeverSession()` | entire function: the hairpin initialize flow is removed |
| `HandleElicitationResponse()` | `GetSession()` call to retrieve server session |

`internal/session/cache.go` methods:
- `AddSession()`, `GetSession()`, `RemoveServerSession()` — all keyed on `Mcp-Session-Id`
- These survive only if the gateway JWT infrastructure is kept separately from the MCP session protocol

`internal/session/jwt.go`:
- `JWTManager.Generate()` / `Validate()` — the JWT is a gateway-level token, not an MCP protocol artifact
- The JWT system is the gateway's own invention; it can survive MCP going stateless if the gateway
  issues JWTs independently of the MCP `initialize` handshake

**Critical Q4 analysis (A2A session coupling):**

The A2A design doc states in Prerequisites:
> "The client has completed MCP initialize with the gateway and holds a valid mcp-session-id JWT.
> A2A requests reuse this session."

If MCP goes stateless on July 28 (Week 8 of the LFX term), and the `initialize` handshake disappears:

- **Scenario A:** Gateway continues issuing JWTs on a new endpoint (e.g., `POST /auth` or
  `POST /mcp` with `server/discover`). A2A requests still need a JWT. Coupling is acceptable.
- **Scenario B:** Gateway drops the JWT system entirely for stateless MCP. A2A would need its own
  session mechanism.
- **Scenario C:** The JWT system moves to an explicit `/session` endpoint unrelated to MCP. A2A
  references this directly.

**Judgement:** MEDIUM-HIGH RISK. Q4 is open for exactly this reason. The design doc correctly defers
this to Week 8. However, the implementation plan (Tasks 9 and 10) assumes the JWT validation path
works. If CONNLINK-1057 lands before Tasks 9/10 do, the `validateSession()` call in
`HandleA2ATaskSend()` may call into a removed or changed API.

**Recommendation:** Before writing Task 10 code, confirm from David whether the JWT gateway session
survives CONNLINK-1057. If not, the A2A design needs its own session token mechanism — potentially
a lightweight `/a2a/session` endpoint that the controller adds to the HTTPRoute config. This is the
concrete Q4 resolution needed by Week 8.

---

### 2.4 CONNLINK-1025 — Repository Split (MCP Gateway vs Operator)

**What it says:** The gateway project will be split into two repositories: one for the MCP Gateway
data-plane (broker + router), one for the Operator (controller + CRDs + Helm chart).

**Codebase impact on A2A:**

The A2A implementation spans both future repos:
- `internal/a2a/` (broker), `internal/mcp-router/` (router) → data-plane repo
- `internal/controller/a2aagentregistration_controller.go`, `api/v1alpha1/a2aagentregistration_types.go`
  → operator repo

If the split happens mid-LFX-term, PRs will need to target two repositories. The `config.Observer`
interface is the boundary: the operator writes to a config Secret; the broker reads it. As long as
A2A config is in the same Secret schema, the split does not require protocol changes.

**Concrete files that cross the split boundary:**

| File | Post-split location |
|---|---|
| `api/v1alpha1/a2aagentregistration_types.go` | operator repo |
| `internal/controller/a2aagentregistration_controller.go` | operator repo |
| `internal/a2a/broker.go` | data-plane repo |
| `internal/mcp-router/server.go` (A2A additions) | data-plane repo |
| `internal/config/types.go` (A2AAgents field) | shared / data-plane repo |
| `internal/session/cache.go` (TaskStore methods) | data-plane repo |

**Judgement:** LOW-MEDIUM RISK for this term. The split is a planned future event; it does not
block the LFX prototype. The key mitigation is keeping the coupling at the config Secret boundary
clean — which the design already does. The `SecretReaderWriter` abstraction (`internal/config/config_writer.go`)
is the correct decoupling point. If the split happens before the operator controller PR lands, that
PR targets the new operator repo; no re-architecture needed.

---

### 2.5 CONNLINK-1061 — MCP Gateway GA Roadmap (Jira accessible)

**What it says:** CONNLINK-1061 is the externally-accessible Jira issue containing the MCP Gateway GA
roadmap items. The internal Red Hat planning document (Google Doc, internal-only) covers the same
content. David Martin confirmed (June 15) that CONNLINK-1061 is the right entry point — most items
there have corresponding upstream GitHub issues. Some items are OpenShift-specific or downstream
processes and are deliberately absent from GitHub to avoid noise in the public repo.

**Codebase impact:** Reviewing CONNLINK-1061 gives visibility into what capabilities Red Hat needs
for GA. Any GA-required feature that intersects with A2A (e.g., observability, auth integration,
agent card schema) should be reflected in the design doc's Future Considerations section.

**Judgement:** Read CONNLINK-1061 before finalising the implementation plan tasks. Cross-reference
each GA roadmap item against the A2A design — specifically look for observability/auditing items
(David explicitly called out "observability/auditing at the gateway" as where Kuadrant adds value
regardless of how the A2A spec settles). Any relevant items should be added to the Future
Considerations section of `docs/design/a2a/a2a-design.md`.

---

## 3. A2A Specification Version Analysis

### 3.1 v0.3.0 vs v1.0.1 — Detailed Diff

| Dimension | v0.3.0 (current design target) | v1.0.1 (current published spec) |
|---|---|---|
| JSON-RPC methods | `message/send`, `tasks/get`, `tasks/cancel`, `tasks/resubscribe` | `SendMessage`, `GetTask`, `CancelTask`, `ResubscribeToTask` (PascalCase) |
| Streaming method | `message/send` + `Accept: text/event-stream` | `SendMessageStream` as a separate method |
| Agent card well-known URI | `/.well-known/agent.json` | `/.well-known/agent-card.json` |
| A2A-Version header | Not present | Required: `A2A-Version: 1.0` |
| AgentCard `supportedInterfaces` | Not present | Required array: `["tasks"]`, `["streaming"]`, etc. |
| MessageSendParams `skill` field | Not present (confirmed v0.3.0) | Not present (still absent) |
| Task state machine | `submitted`, `working`, `input-required`, `completed`, `failed`, `canceled` | Same states |
| Authentication in AgentCard | `authentication.schemes: []string` | `authentication.securitySchemes` (OpenAPI-style) |

**Codebase impact if targeting v1.0.1:**

1. **Router method constants** (`internal/mcp-router/request_handlers.go` and new A2A files):
   ```go
   // v0.3.0 (current design)
   a2aMethodMessageSend  = "message/send"
   a2aMethodTasksGet     = "tasks/get"
   a2aMethodTasksCancel  = "tasks/cancel"

   // v1.0.1 (would need to become)
   a2aMethodSendMessage   = "SendMessage"
   a2aMethodGetTask       = "GetTask"
   a2aMethodCancelTask    = "CancelTask"
   ```
   All method comparisons in `RouteA2ARequest()` switch statements need updating.

2. **Broker well-known endpoint** (`cmd/mcp-broker-router/main.go` and `internal/a2a/broker.go`):
   ```go
   // v0.3.0
   mux.HandleFunc("/a2a/{prefix}/.well-known/agent.json", a2aBroker.ServeAgentCard)

   // v1.0.1
   mux.HandleFunc("/a2a/{prefix}/.well-known/agent-card.json", a2aBroker.ServeAgentCard)
   ```
   Also affects the upstream agent card fetch URL in `A2AAgentManager.FetchAgentCard()`.

3. **Router — `A2A-Version` header injection:**
   v1.0.1 requires the `A2A-Version: 1.0` header on all A2A requests. The `HeadersBuilder` in
   `internal/mcp-router/headers.go` would need a `WithA2AVersion()` method, and `HandleRequestHeaders()`
   would need to inject it.

4. **AgentCard `supportedInterfaces` field:**
   The A2A broker's federated card builder (`buildFederatedAgentCard()`) would need to populate
   `supportedInterfaces` from the upstream agent's card. The Go struct in `internal/a2a/types.go`
   would gain a `SupportedInterfaces []string` field.

5. **e2e test cases (`tasks/e2e_test_cases.md`):**
   All curl commands and assertions use `method: "message/send"` etc. These need replacing with
   v1.0.1 method names.

6. **Design doc (`a2a-design.md`):**
   All sequence diagrams, JSON examples, and component responsibility table use v0.3.0 methods.

**Scope of change:** Targeting v1.0.1 instead of v0.3.0 is a medium-sized change — it affects string
constants, the broker endpoint URL, and the test assertions, but not the fundamental architecture.
The routing logic, ext_proc integration, task store, and CRD design are version-agnostic.

**Judgement:** This is the most consequential open question. The design doc's Non-Goals section
says "Supporting A2A spec versions other than v0.3.0" — but if the upstream A2A test server and
real A2A agents implement v1.0.1, the gateway will be interoperating with a different spec from day
one. Recommend asking David explicitly: "Should we target v0.3.0 for the PoC and upgrade later, or
build against v1.0.1 from the start given it's the current published spec?"

---

### 3.2 MessageSendParams Skill Field (Q3 Resolution Justification)

This is documented explicitly because Q3 ("What routing discriminator?") was resolved through spec
research, not a mentor conversation — and the commit history should reflect that.

In A2A v0.3.0, `MessageSendParams` is:
```json
{
  "message": { "role": "user", "parts": [...] },
  "configuration": { ... },
  "metadata": { ... }
}
```

There is no `skill` field. The skill-dispatch routing approach — reading `params.skill` from the
JSON body at `ProcessingRequest_RequestBody` to select the upstream agent — would have required
injecting a non-spec field that standard A2A clients would never send. This is not a design preference;
it is a spec compliance issue. Path-per-agent routing (`/a2a/{prefix}`) is the only spec-compliant
option when the path is the routing discriminator.

In A2A v1.0.1, `MessageSendParams` is similarly structured — `message`, `configuration`, `metadata`,
and `context` — still no `skill` field. So Q3 resolution holds for both spec versions.

---

## 4. David Martin's Tracking Issue — Design Coverage Analysis

Based on the tracking issue for A2A support in mcp-gateway, the following goals and considerations
were identified. This section maps each against the current design doc and codebase.

### 4.1 Goal: Route A2A traffic through the gateway policy layer

**Tracking issue intent:** A2A agent-to-agent calls should be subject to Kuadrant AuthPolicy and
RateLimitPolicy, identical to MCP traffic.

**Design doc coverage:** Fully covered. The design routes all A2A traffic through the `/a2a` HTTPRoute,
which can carry AuthPolicy and RateLimitPolicy. Section "Policy Enforcement Design" in
`lfx_implementation_plan.md` documents the attachment mechanism.

**Codebase alignment:** `config/e2e/auth/mcps-auth-policy.yaml` demonstrates the existing AuthPolicy
pattern for MCP. An identical resource targeting the `/a2a` HTTPRoute achieves A2A policy enforcement.
No gateway code changes needed — pure Kubernetes resource configuration.

---

### 4.2 Goal: Federated agent card discovery

**Tracking issue intent:** Operators should be able to register multiple upstream A2A agents and have
clients discover all of them through a single gateway endpoint.

**Design doc coverage:** Fully covered via RFC 9264 API Catalog at `/.well-known/api-catalog` and
per-agent cards at `/a2a/{prefix}/.well-known/agent.json`.

**Codebase alignment:**  
The existing `oauth_protected_resource_handler.go` serves `/.well-known/oauth-protected-resource` as
a well-known document from the broker HTTP mux. `ServeAPICatalog()` follows the identical registration
pattern:
```go
// existing
mux.HandleFunc("/.well-known/oauth-protected-resource", oauthHandler.Handle)

// new (Task 8)
mux.HandleFunc("/.well-known/api-catalog", a2aBroker.ServeAPICatalog)
mux.Handle("/a2a/{prefix}/.well-known/agent.json", a2aBroker.ServeAgentCard)
```
The Go 1.22+ `net/http` mux supports path parameter syntax (`{prefix}`). The module uses Go 1.25.9
(`go.mod`), so this is available.

---

### 4.3 Goal: Task ID isolation across upstream agents

**Tracking issue intent:** Clients should not need to know about upstream agents directly. Gateway
generates its own task IDs; clients use gateway task IDs for all `tasks/get` and `tasks/cancel`
operations.

**Design doc coverage:** Fully covered. `TaskRoute` struct, `StoreTaskRoute()` / `ResolveTaskRoute()`,
gateway task ID generation at RequestBody phase.

**Codebase alignment:**  
`internal/session/cache.go` has `inmemory *sync.Map` and `extClient *redis.Client`. Adding a new
`taskRoutes sync.Map` field to `Cache` is a clean addition — no existing fields need changing.

The `idmap.Map` interface in `internal/idmap/map.go` provides a structural template: `Store()`,
`Lookup()`, `Remove()`. The `TaskStore` is analogous, with a richer value type (`TaskRoute` struct
vs `idmap.Entry`).

Redis key pattern `a2atask:{gatewayTaskID}` avoids collision with:
- Session cache keys (bare UUID)
- Elicitation map keys (`elicit:{id}`)
- User token keys (`token:{serverName}`)
- Client elicitation flags (`clientelicitation:{sessionID}`)

---

### 4.4 Goal: SSE streaming support

**Tracking issue intent:** Long-running tasks should be streamable; clients should receive real-time
updates without polling.

**Design doc coverage:** Fully covered. `message/send` + `Accept: text/event-stream` triggers SSE
mode. `a2aSSEPassthrough` handles the response body streaming.

**Codebase alignment:**  
The existing `sseRewriter` in `internal/mcp-router/elicitation.go` is the direct template for
`a2aSSEPassthrough`. The `ModeOverride` mechanism at `ProcessingRequest_ResponseHeaders` is already
implemented and tested. The EnvoyFilter YAML does not need changes — `allow_mode_override: true` is
already set.

The SSE streaming path in the current codebase:
```
ResponseHeaders → HandleResponseHeaders() → sets ModeOverride: STREAMED
ResponseBody (per chunk) → sseRewriter.Process() → rewritten chunk
ResponseBody (EndOfStream) → sseRewriter.Flush() → final chunk
```
A2A streaming uses the same path with `a2aSSEPassthrough.Process()` instead of `sseRewriter.Process()`.
The A2A passthrough is simpler — it rewrites upstream task IDs to gateway task IDs in the `"id"` JSON
field of each `data:` event. The elicitation rewriter rewrites elicitation IDs, which involves more
complex state tracking.

---

### 4.5 Goal: CRD-driven configuration (no restart required)

**Tracking issue intent:** Creating or deleting `A2AAgentRegistration` resources should cause the
gateway to pick up the change without restarting.

**Design doc coverage:** Covered via `config.Observer` hot-reload. Controller writes to config Secret;
broker watches Secret via `fsnotify` and calls `OnConfigChange()`.

**Codebase alignment:**  
`internal/config/types.go`: `RegisterObserver(Observer)` and `Notify(ctx, *MCPServersConfig)` are
the hot-reload mechanism. The A2A broker registers as an observer:
```go
cfg.RegisterObserver(a2aBroker)  // a2aBroker.OnConfigChange() is called on every config update
```
`cmd/mcp-broker-router/main.go:396` shows the existing `LoadConfig()` and `viper.OnConfigChange()`
wiring. Adding `A2AAgents` to the config struct and reading `a2aAgents:` from the YAML is a
two-line change.

---

### 4.6 Consideration: Session coupling with Q4

**Tracking issue intent:** The design should not hard-couple A2A to the MCP session protocol in a
way that breaks when MCP goes stateless.

**Design doc coverage:** Q4 is explicitly open. The design doc says: "A2A requests reuse this session.
[OPEN: Q4 — reuse mcp-session-id or separate A2A session? Needed before Week 8.]"

**Codebase alignment:**  
The `JWTManager` in `internal/session/jwt.go` is gateway infrastructure, not an MCP protocol artifact.
It can continue to issue JWTs even if the MCP `initialize` handshake is removed. The question is
whether the client has a way to obtain a JWT without going through MCP `initialize`.

The cleanest resolution: keep `JWTManager.Generate()` as-is and add a new HTTP endpoint
`POST /a2a/session` that issues a JWT independently of MCP initialization. This endpoint would be
registered in `setUpHTTPServer()` alongside `/.well-known/api-catalog`. The endpoint validates the
`Authorization: Bearer` token via an AuthPolicy and then returns a signed JWT. No changes to
`JWTManager` itself. The client uses this JWT in `mcp-session-id` for all subsequent A2A requests.

This is a concrete Q4 resolution option that:
- Keeps the existing JWT validation path in `HandleA2ATaskSend()` unchanged
- Does not depend on MCP initialize surviving CONNLINK-1057
- Can be retrofitted to `MCPBroker` → `validateSession()` once confirmed

---

## 5. Kuadrant YouTube Research — Codebase Validation

This section cross-references findings from Kuadrant's YouTube channel (year-old videos documenting
architectural decisions) against the current codebase state.

### 5.1 x-track Filter / Body-Before-Headers Problem

**YouTube finding:** The team previously built a separate "x-track" filter to extract tool names from
JSON request bodies for policy enforcement. Root cause: Envoy sends headers to upstream before the
body is fully processed at the ext_proc level, making body-derived routing decisions at the header
phase technically complex.

**Codebase verification:**  
`internal/mcp-router/server.go` shows the processing order:
```
ProcessingRequest_RequestHeaders → headers extracted (path, method, host)
ProcessingRequest_RequestBody   → body parsed, JSON-RPC method extracted
```
The router cannot know the A2A JSON-RPC method at the RequestHeaders phase — only at RequestBody.
This was the exact problem with the skill-dispatch approach: reading `params.skill` from the body
at RequestBody phase to make a routing decision that should have been made at RequestHeaders.

**A2A design alignment:**  
Path-per-agent routing (`/a2a/{prefix}`) extracts the agent prefix at `ProcessingRequest_RequestHeaders`
via `:path` — no body read required. The agent is known before the body arrives. This avoids the
x-track problem entirely and is the direct application of the lesson the team already learned.

This is worth stating explicitly in any mentor conversation: "path-per-agent routing avoids recreating
the x-track filter problem your team hit with tool-name-based routing — the agent is known at header
phase from `:path`, not from the body."

---

### 5.2 Wasm vs ext_proc Decision

**YouTube finding:** Team evaluated Wasm filters for tool filtering but chose ext_proc. The reasoning
was deployment complexity and debugging difficulty of Wasm modules.

**Codebase verification:**  
`config/istio/envoyfilter.yaml` confirms ext_proc is the current integration:
```yaml
name: envoy.filters.http.ext_proc
typed_config:
  "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
  grpc_service:
    envoy_config_ref:
      name: outbound|50051||mcp-broker.mcp-system.svc.cluster.local
```

**A2A design alignment:**  
A2A routing is implemented inside the existing ext_proc server (`ExtProcServer.Process()`), not as a
new Wasm filter. The design correctly extends the existing ext_proc rather than introducing a new
filter — consistent with the team's decision.

---

### 5.3 Steel Thread Approach / Living Design RFC

**YouTube finding:** Mentors value early "steel thread" demonstrations over polished finished work.
The RFC (design doc) process is used as a living document to guide implementation.

**Codebase alignment:**  
PR #1114 (the current design doc PR) is playing exactly this role. The design doc has open questions
explicitly marked (`[OPEN: Q4...]`), which is correct — it is a living RFC, not a finished spec. The
mentor posting pattern (asking for confirmation, not presenting decisions) aligns with this.

---

### 5.4 Three Personas

**YouTube finding:** Kuadrant uses three personas in documentation: Infrastructure Provider, Cluster
Operator, App Developer.

**A2A design alignment:**  
Task 15 (User Guide) should use these personas. The platform engineer in the Job Stories maps to
Cluster Operator. The A2A client application maps to App Developer. The guide should be structured
around these personas for consistency with the rest of `docs/guides/`.

---

## 6. Linux Foundation Ecosystem Validation

### 6.1 agentgateway (Linux Foundation, Rust)

**External finding:** The Linux Foundation's `agentgateway` project (Rust implementation) uses
per-route `a2a: {}` policy with one route per agent. There is no federated agent card; each agent
has its own routing entry. No task ID mapping layer.

**Codebase alignment:**  
The mcp-gateway approach adds two capabilities that agentgateway does not have:
1. **RFC 9264 API Catalog** — federated discovery of all registered agents. agentgateway requires
   the client to know each agent's endpoint directly.
2. **Task ID isolation** — the gateway generates its own task IDs and maps them to upstream IDs.
   agentgateway proxies task IDs directly.

Both additions are architecturally justified:
- RFC 9264 catalog is the spec-recommended multi-agent discovery mechanism.
- Task ID isolation is a security property (clients cannot probe upstream task IDs) and an operational
  property (upstream task IDs don't leak to clients, enabling backend agent replacement without
  client changes).

The path-per-agent routing approach (`/a2a/{prefix}`) is confirmed as the industry-standard approach
by agentgateway's per-route design. Our design validates against real-world implementation practice.

---

## 7. Codebase Integration Points — Precise File Map

This section provides the definitive map of every file the A2A implementation touches, with the
current state of each file and the specific changes needed.

### 7.1 Files Modified (existing files)

**`internal/mcp-router/server.go`**  
Current state: `ExtProcServer` struct, `Process()` loop with four cases.  
Changes needed:
- Add `A2ABroker a2a.Broker` field to `ExtProcServer`
- Add `isA2A bool` local in `Process()`
- Branch at `ProcessingRequest_RequestHeaders`: `isA2A = strings.HasPrefix(path, "/a2a")`
- Branch at `ProcessingRequest_RequestBody`: `if isA2A { RouteA2ARequest() }  else { RouteMCPRequest() }`
- Branch at `ProcessingRequest_ResponseHeaders`: A2A streaming mode override
- Branch at `ProcessingRequest_ResponseBody`: A2A SSE passthrough

**`internal/mcp-router/headers.go`**  
Current state: `HeadersBuilder` with MCP header methods.  
Changes needed:
- Add `A2AAgentHeader`, `A2ATaskIDHeader`, `A2AMethodHeader` constants
- Add `WithA2AAgent()`, `WithA2ATaskID()`, `WithA2AMethod()` methods
- Add A2A headers to `internalOnlyHeaders` map

**`internal/session/cache.go`**  
Current state: `Cache` with `inmemory sync.Map`, `extClient *redis.Client`, session/token methods.  
Changes needed:
- Add `taskRoutes sync.Map` field (separate from `inmemory` to avoid type collision)
- Add `StoreTaskRoute()`, `ResolveTaskRoute()`, `DeleteTaskRoute()` with in-memory and Redis paths
- Redis key prefix: `a2atask:`; TTL: from `JWTManager.GetExpiresIn(sessionID)`

**`internal/config/types.go`**  
Current state: `MCPServersConfig` with `Servers`, `VirtualServers`, `observers`.  
Changes needed:
- Add `A2AAgents []*A2AAgent` field
- Add `SetA2AAgents()`, `ListA2AAgents()` methods under existing `lock sync.RWMutex`
- `Notify()` passes A2A agents to observers alongside MCP servers

**`internal/config/config_writer.go`**  
Current state: `UpsertServer()`, `RemoveServer()` with retry-on-conflict.  
Changes needed:
- Add `UpsertA2AAgent()`, `RemoveA2AAgent()` following identical retry-on-conflict pattern
- `BrokerConfig` struct gains `A2AAgents []A2AAgent` field with `yaml:"a2aAgents,omitempty"`

**`internal/controller/broker_router.go`**  
Current state: `buildGatewayHTTPRoute()` with `/mcp` and `/.well-known/oauth-protected-resource` rules.  
Changes needed:
- Add `/a2a` PathPrefix rule with `RequestHeaderModifier` filter stripping `x-a2a-agent`, `x-a2a-task-id`
- Add `/.well-known/api-catalog` ExactPath rule

**`cmd/mcp-broker-router/main.go`**  
Current state: `setUpHTTPServer()`, `setUpRouter()`, `LoadConfig()`.  
Changes needed:
- `setUpHTTPServer()`: register `/.well-known/api-catalog` and `/a2a/{prefix}/.well-known/agent.json`
- `setUpRouter()`: inject `A2ABroker` into `ExtProcServer`
- `LoadConfig()`: parse `a2aAgents` key from config YAML
- Register `a2aBroker` as config observer: `cfg.RegisterObserver(a2aBroker)`

**`cmd/main.go`**  
Current state: registers `MCPReconciler`, `MCPGatewayExtensionReconciler`.  
Changes needed:
- Add `A2AAgentRegistration` to scheme: `mcpv1alpha1.AddToScheme(scheme)` (already present for other CRDs)
- Register `A2AReconciler.SetupWithManager(mgr)`

### 7.2 Files Created (new files)

| File | Purpose |
|---|---|
| `api/v1alpha1/a2aagentregistration_types.go` | CRD type definitions |
| `internal/controller/a2aagentregistration_controller.go` | A2AReconciler |
| `internal/controller/a2aagentregistration_controller_test.go` | unit tests |
| `internal/controller/a2aagentregistration_controller_integration_test.go` | envtest tests |
| `internal/config/a2a_types.go` | A2AAgent config struct |
| `internal/a2a/broker.go` | A2ABroker interface + implementation |
| `internal/a2a/broker_test.go` | broker unit tests |
| `internal/a2a/types.go` | AgentCard, Skill, TaskRoute, TaskStore types |
| `internal/mcp-router/a2a_request.go` | A2ARequest struct + method constants |
| `internal/mcp-router/a2a_router.go` | RouteA2ARequest, HandleA2ATaskSend, HandleA2ATaskOperation |
| `internal/mcp-router/a2a_router_test.go` | router unit tests |
| `tests/servers/a2a-server/main.go` | A2A test server (minimal) |
| `tests/servers/a2a-server/Dockerfile` | container image |
| `tests/e2e/a2a_discovery_test.go` | discovery + deregistration E2E tests |
| `tests/e2e/a2a_task_test.go` | task routing + streaming + auth E2E tests |
| `docs/reference/a2aagentregistration.md` | CRD API reference |
| `docs/guides/a2a-agent.md` | operator how-to guide |

### 7.3 Files Regenerated (make generate-all)

- `api/v1alpha1/zz_generated.deepcopy.go`
- `config/crd/mcp.kuadrant.io_a2aagentregistrations.yaml`
- `charts/mcp-gateway/crds/mcp.kuadrant.io_a2aagentregistrations.yaml`

---

## 8. Design Doc Assessment

### 8.1 What Is Correct

1. **Path-per-agent routing** — spec-compliant, avoids body-before-headers problem, consistent with
   agentgateway industry practice.

2. **RFC 9264 API Catalog** — correct discovery mechanism for multi-agent federation. Individual
   agent cards at `/a2a/{prefix}/.well-known/agent.json` is correct per v0.3.0 spec.

3. **`A2AAgentRegistration` as a new CRD** — not extending `MCPServerRegistration`. Correct call;
   the field sets are divergent enough to warrant a separate type. Follows Gateway API naming convention.

4. **`mcp.kuadrant.io` API group** — aligned with CONNLINK-1109 and existing codebase.

5. **Task ID isolation** — gateway generates its own IDs, clients never see upstream IDs. Correct
   security design.

6. **`config.Observer` hot-reload** — correct reuse of existing infrastructure, no restart required
   for agent registration/deregistration.

7. **SSE streaming via `ModeOverride`** — reuses existing elicitation infrastructure. No EnvoyFilter
   change needed.

8. **Internal header stripping** — `x-a2a-agent` and `x-a2a-task-id` stripped at HTTPRoute level
   and in `internalOnlyHeaders`. Correct dual defence.

9. **`credentialRef` isolation** — broker-only, never injected into client tool/call requests.
   Follows existing `MCPServerRegistration.credentialRef` semantics exactly.

10. **Session validation at RequestBody** — `validateSession()` before `RouteA2ARequest()` matches
    the existing `HandleToolCall()` pattern. Consistent.

### 8.2 What Needs Attention

**HIGH: `skillPrefix` field naming**  
The CRD field is named `skillPrefix` but the routing is path-based (`/a2a/{skillPrefix}`). After the
routing approach changed from skill-dispatch to path-per-agent, the field name is a misnomer. A platform
engineer reading the CRD YAML will ask "what is a skillPrefix?" — the answer is "the URL path segment",
not "a skill namespace". Recommend renaming to `agentPrefix` or `prefix` before the CRD is applied
to a cluster (changing it post-application requires schema migration). The PR comment should flag this
to David explicitly.

**HIGH: A2A spec version needs explicit confirmation**  
The design doc's Non-Goals says "v0.3.0 only" but the published spec is v1.0.1. This needs a
deliberate decision from mentors, not an assumption. The method names in the router code are the
hardest thing to change after implementation begins.

**MEDIUM: `a2aMethodMessageStream` constant in `lfx_implementation_plan.md`**  
The implementation plan includes a `message/stream` constant (`a2aMethodMessageStream`). The design
doc (correctly) removed `message/stream` as a separate method — streaming is `message/send` +
`Accept: text/event-stream`. The implementation plan has not been fully updated to reflect this.
The streaming method detection in `isStreamingMethod()` should check for the `Accept` header, not
the JSON-RPC method name.

**MEDIUM: Task 11 note on `a2a-task-routing-infra` branch**  
The tasks.md notes: "The a2a-task-routing-infra branch has an existing partial implementation with a
simpler `StoreTaskRoute(ctx, taskID, serverName string)` signature." This branch needs to be rebased
onto current main and updated before Task 11. The `TaskRoute` struct in the design doc is richer than
the existing partial implementation. This rebase carries risk of conflict with any session cache
changes from CONNLINK-1057.

**LOW: `agentCardURL` field format validation**  
The CRD spec says `+kubebuilder:validation:Pattern=^https?://` but if `agentCardURL` is an override
for the well-known path (e.g., `/.well-known/agent.json`), a URL with a path component is also valid.
The regex may reject `http://agent.local:8080/.well-known/agent.json` vs `https://agent.example.com`.
The CEL rule should be reviewed before the CRD is merged.

### 8.3 What Is Open

| # | Question | Due | Risk if not resolved |
|---|---|---|---|
| Q2 | A2A spec version (v0.3.0 vs v1.0.1) | Before Task 9 | All router constants, broker endpoints, test cases wrong |
| Q4 | Session reuse vs separate A2A session | Before Week 8 | `validateSession()` call in Task 10 may break with CONNLINK-1057 |
| Q5 | Rename `skillPrefix` → `agentPrefix`? | Before Task 3 (CRD finalization) | Confusing API, hard to rename post-cluster-apply |

---

## 9. Implementation Risk Register

| ID | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | A2A spec version mismatch between design and real agents | HIGH | HIGH | Get explicit mentor confirmation before writing Task 9 code |
| R2 | CONNLINK-1057 removes `mcp-session-id` before A2A Tasks 9/10 merge | MEDIUM | HIGH | Design Q4 resolution before Week 8; keep JWT manager independent of MCP protocol |
| R3 | `a2a-task-routing-infra` branch creates merge conflicts | MEDIUM | MEDIUM | Rebase and update to full TaskRoute struct before Task 11 |
| R4 | CONNLINK-1025 repo split happens mid-term | LOW | MEDIUM | Keep controller/broker coupling at config Secret boundary only |
| R5 | `skillPrefix` field confuses operators and requires rename after apply | LOW | MEDIUM | Rename to `agentPrefix` before Task 3 CRD merge |
| R6 | A2A v1.0.1 `A2A-Version` header requirement breaks compliance | LOW | LOW | Confirm target spec version first; add header if targeting v1.0.1 |
| R7 | RFC 9264 API Catalog not widely implemented | LOW | LOW | The spec is correct choice for multi-agent discovery regardless |

---

## 10. Actionable Recommendations

**Immediate (before next PR review):**
1. Flag `skillPrefix` naming to mentors in PR #1114 comments. Ask if they prefer `agentPrefix` or
   `prefix`. Do not write the CRD Go types until this is answered.
2. Ask David: "Should we target A2A v0.3.0 for the PoC, or build against v1.0.1 given it's the
   current published spec?" Frame as: "v1.0.1 changes method names to PascalCase and the well-known
   URI to `agent-card.json` — not an architectural change, but commits us to different string constants."
3. Ask David: "Re: Q4 — after CONNLINK-1057 lands, how will an A2A-only client obtain a gateway
   JWT without going through MCP initialize? Are you planning a standalone `/session` endpoint, or
   should A2A have its own token mechanism?"

**Before Task 3 (CRD):**
4. Finalize CRD field name for the path prefix field.
5. Confirm API version: `v1alpha1` or `v1` (per CONNLINK-1109 graduation timeline).

**Before Task 9 (Router — A2A Traffic Detection):**
6. Finalize A2A spec version target. Lock in method name constants.
7. Audit the implementation plan for remaining `message/stream` references and replace with
   `message/send` + `Accept: text/event-stream` throughout.

**Before Task 11 (Task ID Mapping):**
8. Rebase `a2a-task-routing-infra` branch onto current main. Update `StoreTaskRoute` signature
   to use the full `TaskRoute` struct from the design doc.

**Ongoing:**
9. Every PR must include the MCP regression E2E test. No exceptions.
10. Keep the `internal/a2a/` package isolated — do not import from `internal/broker/` into it.
    Dependency direction: `broker → a2a`, not `a2a → broker`.

---

## Appendix A: Module-Level Dependency Graph (A2A additions)

```
cmd/main.go
  └── internal/controller/a2aagentregistration_controller.go
      ├── api/v1alpha1/a2aagentregistration_types.go
      ├── internal/config/config_writer.go
      └── internal/controller/httproute_wrapper.go (reused as-is)

cmd/mcp-broker-router/main.go
  ├── internal/a2a/broker.go
  │   ├── internal/config/types.go (A2AAgent, Observer)
  │   └── internal/a2a/types.go (AgentCard, TaskStore)
  └── internal/mcp-router/server.go (A2ABroker field)
      ├── internal/mcp-router/a2a_request.go
      ├── internal/mcp-router/a2a_router.go
      │   ├── internal/session/cache.go (TaskStore methods)
      │   └── internal/mcp-router/headers.go (A2A header methods)
      └── internal/session/jwt.go (validateSession, GetExpiresIn)
```

---

## Appendix B: A2A v0.3.0 vs v1.0.1 Spec Diff Summary

| Field | v0.3.0 | v1.0.1 |
|---|---|---|
| `message/send` | `method: "message/send"` | `method: "SendMessage"` |
| Streaming | `message/send` + `Accept: text/event-stream` | `method: "SendMessageStream"` |
| `tasks/get` | `method: "tasks/get"` | `method: "GetTask"` |
| `tasks/cancel` | `method: "tasks/cancel"` | `method: "CancelTask"` |
| Agent card URI | `/.well-known/agent.json` | `/.well-known/agent-card.json` |
| Version header | Not present | `A2A-Version: 1.0` required |
| AgentCard skills | `skills: [{ id, name, description }]` | Same, plus `inputModes`, `outputModes` |
| Capabilities | Not present | `supportedInterfaces: ["tasks", "streaming"]` |
| Auth in card | `authentication.schemes` | `authentication.securitySchemes` (OpenAPI 3) |

---

## Appendix C: Current Codebase State Snapshot (June 2026)

| Component | Location | A2A readiness |
|---|---|---|
| API group | `mcp.kuadrant.io/v1alpha1` | Aligned with A2A CRD design |
| ext_proc server | `internal/mcp-router/server.go` | Has all four phases; ready for A2A branch |
| SSE streaming | `internal/mcp-router/elicitation.go` | `sseRewriter` is template for A2ASSEPassthrough |
| Session cache | `internal/session/cache.go` | Ready for TaskStore extension |
| JWT manager | `internal/session/jwt.go` | Ready for A2A session validation reuse |
| Config observer | `internal/config/types.go` | Ready for A2ABroker to register |
| HTTP mux | `cmd/mcp-broker-router/main.go:317` | Ready for API Catalog + AgentCard handlers |
| HeadersBuilder | `internal/mcp-router/headers.go` | Ready for A2A header methods |
| Controller | `internal/controller/` | Pattern ready for A2AReconciler |
| Test servers | `tests/servers/` | No A2A server yet (Task 2) |
| Config types | `internal/config/types.go` | No A2AAgent type yet (Task 6) |
