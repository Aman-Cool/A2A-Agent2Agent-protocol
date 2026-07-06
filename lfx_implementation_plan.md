# LFX Mentorship 2026 Implementation Plan
## CNCF – Kuadrant: Prototype A2A Protocol Support in the Agentic Gateway
### Term 2: June–August 2026
**Author:** Aman Kumar  
**Repo:** github.com/Kuadrant/mcp-gateway  
**Mentors:** david-martin, maleck13

---

## 1. Project Overview & Motivation

### The Agentic Gateway Problem

Modern AI workloads are architecturally similar to microservice workloads a decade ago: dozens of specialized agents — each with distinct capabilities, trust boundaries, and authentication requirements — need to interoperate safely and at scale. Just as API gateways emerged to provide a unified, policy-enforced entry point for REST services, the ecosystem now needs agentic gateways that can route, secure, and federate traffic between agents and their tools. Without a gateway layer, every agent-to-agent interaction requires bespoke authentication wiring, direct network access, and per-pair policy configuration — exactly the pattern that collapsed under the weight of microservice proliferation.

### What MCP Solves

The Model Context Protocol (Anthropic, now Linux Foundation) standardizes the *vertical* relationship between a single agent and the tools or data sources it consumes. The MCP Gateway already handles this dimension: it aggregates tools from multiple upstream MCP servers, enforces authentication via Kuadrant AuthPolicy / Authorino, applies rate limits via Kuadrant RateLimitPolicy, and routes `tools/call` requests to the correct backend using Envoy's external processor (ext_proc) at `:50051`. Clients see a single `/mcp` endpoint and a federated tool list; the gateway handles all backend complexity.

### What A2A Solves

The Agent-to-Agent Protocol (Google, now Linux Foundation) standardizes the *horizontal* relationship: how two agents discover each other, negotiate capabilities, and delegate long-running work. A2A introduces concepts that have no equivalent in MCP:

- **Agent Cards** — a JSON discovery document at `/.well-known/agent.json` describing an agent's name, version, skills, endpoint URL, and authentication requirements. This is how an agent advertises itself to the ecosystem.
- **Tasks** — the primary unit of work. A Task has an explicit state machine (`submitted → working → input-required → completed / failed / canceled / rejected`) and is first-class: tasks can run for seconds or days, can be polled or streamed, and produce typed Artifacts.
- **Messages** — the exchange unit inside a Task. Multi-modal by design (text, file, structured data via typed Parts).
- **Artifacts** — structured output delivered when a Task completes.
- **Streaming and Push** — Server-Sent Events for real-time task updates; webhook-based push notifications for async task completion callbacks.
- **JSON-RPC 2.0 over HTTP** — wire protocol, parity with MCP at the transport layer.

### Why A2A Support is Needed in the Gateway

MCP and A2A are complementary, not competing. MCP governs the vertical axis (agent consumes tools); A2A governs the horizontal axis (agent delegates work to other agents). A complete agentic platform requires both. Without A2A support, every agent-to-agent delegation bypasses the gateway entirely: no AuthPolicy enforcement, no rate limiting, no centralized logging, no federated discovery. Adding A2A to the gateway extends its policy enforcement perimeter to cover the full interaction surface of modern agentic architectures.

### Why the MCP Gateway is the Right Platform

The MCP Gateway already possesses the primitives A2A requires:

- **Envoy ext_proc routing** — the `ExtProcServer.Process()` loop already parses JSON-RPC bodies, inspects the `method` field, and injects routing headers. Extending this to handle `message/send`, `tasks/get`, and related A2A methods is a targeted addition, not a rewrite.
- **Kubernetes CRD-driven configuration** — the `MCPServerRegistration` + controller pattern, which resolves HTTPRoutes to upstream endpoints and writes config Secrets, is directly reusable for an `A2AAgentRegistration` CRD.
- **Kuadrant policy attachment** — AuthPolicy and RateLimitPolicy already attach to HTTPRoutes that carry MCP traffic. A2A traffic flowing through the same gateway inherits the same policy mechanism.
- **Session store** — the `session.Cache` (in-memory or Redis via `WithRedisClient`) and the `idmap.Map` (the elicitation ID rewriter) both provide patterns for the stateful task-routing index A2A needs.
- **Broker federation** — the broker's pattern of aggregating upstream capabilities (tools) into a single endpoint is exactly what A2A agent card federation requires.

### What This Project Delivers

A working proof-of-concept demonstrating:
1. A design document covering routing strategy, CRD design, session handling, agent card federation, and policy enforcement — reviewed and approved by Kuadrant mentors.
2. An `A2AAgentRegistration` CRD and controller that discovers upstream A2A agents via HTTPRoutes and writes agent config for the broker and router.
3. A2A request routing through the ext_proc pipeline: `message/send`, `tasks/get`, `tasks/cancel`, and related methods dispatched to the correct upstream agent.
4. A federated agent card endpoint in the broker at `/.well-known/agent.json` that aggregates Agent Cards from all registered upstream A2A agents.
5. A Go-based A2A test server in `tests/servers/a2a-server/` used to validate the full pipeline end-to-end.
6. E2E tests covering agent discovery, task submission and completion, streaming task updates, and error handling.
7. Documentation: `docs/design/a2a/a2a-design.md` and `docs/guides/a2a-setup.md`.

---

## 2. Full Codebase Analysis

### 2.1 Build System & Project Structure

**`go.mod`**  
Module: `github.com/Kuadrant/mcp-gateway`, Go 1.25.9. Key direct dependencies relevant to A2A integration:
- `github.com/envoyproxy/go-control-plane/envoy v1.37.0` — ext_proc gRPC definitions (`envoy/service/ext_proc/v3`). The `ExternalProcessor_ProcessServer` stream interface that `ExtProcServer.Process()` implements is defined here. All A2A routing instructions to Envoy use these types.
- `github.com/mark3labs/mcp-go v0.52.0` — MCP server/client SDK. The broker's `server.MCPServer`, `server.StreamableHTTPServer`, and `server.Hooks` are from here. A2A will not use this SDK but must coexist with it in the same binary.
- `google.golang.org/grpc v1.80.0` — gRPC server for the ext_proc listener at `:50051`.
- `sigs.k8s.io/controller-runtime v0.23.1` — Kubernetes controller framework. The `A2AAgentRegistration` controller will use `ctrl.NewControllerManagedBy`, `builder.WithPredicates`, and `reconcile.Request` — exactly as `MCPReconciler.SetupWithManager()` does in `internal/controller/mcpserverregistration_controller.go:638`.
- `sigs.k8s.io/gateway-api v1.4.1` — `gatewayv1.HTTPRoute` and `gatewayv1.Gateway` types used in controller reconcilers to resolve upstream endpoints.
- `istio.io/client-go v1.29.0` — Istio `EnvoyFilter` types used by `MCPGatewayExtensionReconciler` to generate the ext_proc filter config.
- `github.com/golang-jwt/jwt/v5 v5.3.1` — JWT session management in `session.JWTManager`.
- `github.com/redis/go-redis/v9 v9.18.0` — Redis client for shared session state. The `idmap.WithRedisClient` and `session.WithRedisClient` options both accept `*redis.Client`.
- `golang.org/x/oauth2 v0.35.0` (indirect) — already present; usable for fetching upstream Agent Cards that require OAuth bearer tokens.
- `golang.org/x/sync v0.20.0` (indirect) — already present; `singleflight.Group` used in PR #930 for session deduplication; same pattern applies to agent card fetch deduplication.

**`Makefile` / `build/*.mk`**  
The Makefile delegates to modular `.mk` files in `build/`. Key targets:
- `make test-unit` — runs `go test ./...` against unit tests.
- `make test-controller-integration` — runs envtest-based controller integration tests.
- `make lint` — golangci-lint with project config in `.golangci.yml`.
- `make generate-all` — runs `controller-gen` for CRD deepcopy and manifests, then syncs Helm charts. Adding the `A2AAgentRegistration` CRD will require running this target after defining types in `api/v1alpha1/`.
- `build/deploy.mk` — `make local-setup` / `make deploy` targets for local Kind cluster work.

**`Dockerfile` / `Dockerfile.controller`**  
Two separate Dockerfiles, one per binary (`cmd/mcp-broker-router/main.go` and `cmd/main.go`). The A2A broker and router components live inside `cmd/mcp-broker-router/main.go`'s binary. No new Dockerfile is needed; A2A components are added to the existing binary.

**`config/istio/envoyfilter.yaml`**  
The EnvoyFilter that wires ext_proc into Envoy's filter chain:
```yaml
processing_mode:
  request_header_mode: SEND
  response_header_mode: SEND
  request_body_mode: STREAMED
  response_body_mode: NONE
```
The `response_body_mode: NONE` is the baseline; A2A streaming responses (`message/stream`) require `ModeOverride` in the response headers phase — the same mechanism already used for elicitation (`internal/mcp-router/response_handlers.go:53`). This means no EnvoyFilter YAML change is required for basic A2A; the `ModeOverride` path handles per-request streaming opt-in.

The filter targets port 8080 on workloads labeled `gateway.io/name: mcp-gateway` and routes gRPC to `outbound|50051||mcp-broker.mcp-system.svc.cluster.local`. For A2A traffic to flow through the same ext_proc, it must arrive on port 8080 — which is the existing gateway listener. A2A routes will share this listener.

### 2.2 Gateway Core Components

**`cmd/mcp-broker-router/main.go`**  
The single entry point for both the broker and router binaries. Key structure:
- `setUpHTTPServer()` (line 317) creates `http.ServeMux`, registers handlers for `/`, `/healthz`, `/readyz`, `/.well-known/oauth-protected-resource`, `/status`, `/status/`, and `/mcp`. The A2A federated agent card endpoint `/.well-known/agent.json` will be registered here alongside the existing endpoints. This is the natural integration point: one line `mux.HandleFunc("/.well-known/agent.json", a2aBroker.ServeAgentCard)`.
- `setUpRouter()` (line 373) creates the gRPC server and `ExtProcServer`. A new `A2ATaskStore` field will be injected here alongside `SessionCache` and `ElicitationMap`.
- `LoadConfig()` (line 396) reads the YAML config via Viper and populates `mcpConfig.Servers`. A new `mcpConfig.A2AAgents` slice will be added to `config.MCPServersConfig` and populated from the same config file under a new `a2aAgents` key.

**`internal/broker/broker.go`**  
`MCPBroker` interface (line 23) defines the contract between the HTTP server and the broker. The `mcpBrokerImpl` (line 59) holds `mcpServers map[config.UpstreamMCPID]*upstream.MCPManager` and a `listeningMCPServer *server.MCPServer`. A2A does not use the MCP server SDK, so the broker gets a parallel structure: a new `A2ABroker` interface responsible for agent card federation, upstream card fetching, and A2A request proxying. The existing `broker.NewBroker()` functional option pattern (`WithEnforceCapabilityFilter`, `WithManagerTickerInterval`) will be mirrored for A2A broker construction.

The `OnConfigChange(ctx, conf)` hook (line 167) is how the broker receives updated config from the controller. A2A will register a new observer via the same `config.Observer` interface.

**`internal/broker/upstream/manager.go`**  
The `MCPManager` struct (line 73) manages a single upstream MCP server for the broker: it holds a `ticker`, `toolsMap`, `toolsLock sync.RWMutex`, and a `done chan struct{}`. The analogous `A2AAgentManager` for A2A will follow this structure: periodic Agent Card refresh (instead of tool list refresh), connection health checks, and lifecycle management (Start/Stop). The `MCP` interface (line 58) defines the upstream interaction contract; an analogous `A2AAgent` interface will define `FetchAgentCard()`, `Ping()`, `GetConfig()`.

### 2.3 MCP Protocol Handling

**`internal/mcp-router/request_handlers.go`**  
This is the central routing decision file. Every A2A integration point in the router traces back here.

`MCPRequest` struct (line 75): holds `ID`, `JSONRPC`, `Method`, `Params`, `Headers`, and private routing state. For A2A, a parallel `A2ARequest` struct is needed — it also carries a JSON-RPC method and params, but the dispatch logic, header injection, and session semantics differ completely. Rather than overloading `MCPRequest`, a clean `A2ARequest` type avoids the risk of accidentally running MCP routing logic on A2A traffic.

`RouteMCPRequest()` (line 208): the central dispatch switch. The existing cases are `isElicitationResponse()`, `methodToolCall`, and default (broker passthrough). The A2A dispatch will be added as a prefix check before this switch in the `Process()` loop — using URL path (`/a2a` prefix) detected in `HandleRequestHeaders()` to set a flag, then routing to `RouteA2ARequest()` instead of `RouteMCPRequest()`.

`HandleToolCall()` (line 243): the most relevant model for A2A task routing. It: (1) validates session JWT, (2) looks up server config via `Broker.GetServerInfo(toolName)`, (3) injects `:authority`, `:path`, `x-mcp-*` headers, (4) rewrites the request body, and (5) returns `ProcessingResponse` instructions to Envoy. The A2A equivalent `HandleA2ATaskSend()` will follow the same pattern: (1) validate session JWT, (2) look up agent config via `A2ABroker.GetAgentInfo(agentName)`, (3) inject `x-a2a-agent`, `x-a2a-task-id`, `x-a2a-method` headers, (4) pass body through unchanged or with task ID injection, (5) return routing instructions.

`initializeMCPSeverSession()` (line 513): uses `singleflight.Do` (added in PR #930) to prevent duplicate backend sessions. A2A does not have an initialize handshake in the same sense — A2A task routing is purely based on the task ID store. No equivalent function is needed.

`HandleNoneToolCall()` (line 617): passes non-tool-call requests to the broker. A2A requests that are not routable (e.g., an A2A request arrives but the agent name is unknown) will return a JSON-RPC error response directly from the router rather than falling through to the MCP broker.

Constants (line 63): `methodToolCall = "tools/call"`, `methodInitialize = "initialize"`. New A2A method constants will be added:
```go
const (
    a2aMethodMessageSend         = "message/send"
    a2aMethodMessageStream       = "message/stream"
    a2aMethodTasksGet            = "tasks/get"
    a2aMethodTasksCancel         = "tasks/cancel"
    a2aMethodTasksResubscribe    = "tasks/resubscribe"
    a2aMethodTasksPushNotifSet   = "tasks/pushNotification/set"
    a2aMethodTasksPushNotifGet   = "tasks/pushNotification/get"
)
```

`HeadersBuilder` (`internal/mcp-router/headers.go`, line 35): fluent builder with methods like `WithAuthority()`, `WithMCPToolName()`, `WithMCPMethod()`. New `WithA2AAgent()`, `WithA2ATaskID()`, `WithA2AMethod()` methods will be added here for A2A header injection. The header constants `x-a2a-agent`, `x-a2a-task-id`, `x-a2a-method` mirror the existing `x-mcp-servername`, `x-mcp-toolname`, `x-mcp-method`.

### 2.4 Envoy ext_proc Integration

**`internal/mcp-router/server.go`**  
`ExtProcServer` struct (line 39): holds `RoutingConfig`, `JWTManager`, `SessionCache`, `ElicitationMap`, `Broker`, and metrics. Two new fields will be added: `A2ATaskStore a2a.TaskStore` and `A2ABroker a2a.Broker`. This avoids adding A2A types to the existing `MCPBroker` interface.

`Process()` loop (line 72): the gRPC stream handler. The loop processes four message types: `ProcessingRequest_RequestHeaders`, `ProcessingRequest_RequestBody`, `ProcessingRequest_ResponseHeaders`, `ProcessingRequest_ResponseBody`.

Protocol detection happens at the header phase. In the `ProcessingRequest_RequestHeaders` case (line 94), the `:path` header is already extracted (line 111). A2A detection: if the path starts with `/a2a`, set a local `isA2A bool` flag. This flag is then checked at the `ProcessingRequest_RequestBody` phase to dispatch to `RouteA2ARequest()` instead of `RouteMCPRequest()`.

At the `ProcessingRequest_ResponseHeaders` phase (line 246), the existing code checks `mcpRequest.isToolCall()` to decide whether to enable SSE streaming via `ModeOverride`. For A2A, `isA2A && a2aRequest.isStreamingMethod()` will trigger the same `ModeOverride` pattern (setting `ResponseBodyMode: STREAMED`). This reuses the exact same Envoy ext_proc streaming path that elicitation already uses.

The `sseRewriter` in `internal/mcp-router/elicitation.go` (line 24) processes SSE response bodies line-by-line, parsing `data:` prefixed JSON-RPC messages and rewriting IDs. For A2A `message/stream`, the upstream agent's SSE stream carries task update events. An `a2aSSEPassthrough` type (much simpler than `sseRewriter` — no ID rewriting needed) will handle streaming the A2A SSE body through the `ProcessingRequest_ResponseBody` phase.

**`internal/mcp-router/response_builder.go`**  
Provides `NewResponse()` and methods like `WithStreamingResponse()`, `WithRequestBodyHeadersAndBodyReponse()`, `WithImmediateResponse()`. All of these are available for A2A routing instructions without modification. A2A routing will use the same `WithRequestBodyHeadersAndBodyReponse()` for synchronous task submission and `WithStreamingResponse()` for `message/stream`.

### 2.5 Kubernetes CRDs & Gateway API

**`api/v1alpha1/types.go`**  
`MCPServerRegistration` (line 20): the model for the new `A2AAgentRegistration` CRD. Key fields:
- `Spec.TargetRef TargetReference` — references the HTTPRoute pointing to the upstream A2A agent.
- `Spec.Prefix string` — for tool namespacing. The A2A equivalent is a `skillPrefix` that namespaces A2A skills during federation.
- `Spec.CredentialRef *SecretReference` — optional auth credentials. A2A agents may require OAuth bearer tokens; the credential mechanism is identical.
- `Status.Conditions []metav1.Condition` — standard Kubernetes condition pattern for `AgentCardDiscovered`, `EndpointReachable`, and `Ready`.

`TargetReference` (line 68) is reused as-is for `A2AAgentRegistration` — it already supports `HTTPRoute` references, which is all A2A needs.

`MCPVirtualServer` (line 141) is a conceptual model for A2A skill filtering: a future `A2AVirtualAgent` could expose a subset of skills from federated agents. For the PoC, this is out of scope but the pattern is clear.

**`config/crd/mcp.kuadrant.io_mcpserverregistrations.yaml`**  
The generated CRD YAML for `MCPServerRegistration`. Running `make generate-all` after defining `A2AAgentRegistration` types will produce the analogous `mcp.kuadrant.io_a2aagentregistrations.yaml`.

**`config/rbac/role.yaml`**  
Defines `ClusterRole` rules for the controller. The `A2AAgentRegistration` controller will need `get;list;watch;update` on `a2aagentregistrations` and `a2aagentregistrations/status`, identical to the MCPServerRegistration RBAC block.

### 2.6 Policy & Security (Kuadrant Integration)

**`config/e2e/auth/mcps-auth-policy.yaml`** and **`config/samples/remote-github/authpolicy.yaml`**  
AuthPolicy resources that attach to HTTPRoutes. These demonstrate how Kuadrant policy enforcement works: the AuthPolicy references a specific HTTPRoute by name, and Authorino validates tokens on all traffic matching that route's matchers. A2A agent HTTPRoutes can be protected by identical AuthPolicy resources — no gateway-level changes are needed. This is a key architectural advantage: Kuadrant's policy model is already route-level, so A2A agents automatically get the same enforcement surface as MCP servers.

**`config/samples/oauth-token-exchange/`**  
Demonstrates how the gateway handles OAuth token exchange for downstream MCP server calls. The A2A equivalent (agents calling each other through the gateway) uses the same token exchange pattern — client presents OAuth token, gateway validates via AuthPolicy, optionally performs RFC 8693 token exchange before forwarding to the upstream A2A agent.

**`internal/broker/oauth_protected_resource_handler.go`**  
Serves `/.well-known/oauth-protected-resource` per OAuth 2.0 Protected Resource Metadata (RFC 9728). The A2A federated agent card handler at `/.well-known/agent.json` will register alongside this endpoint in `setUpHTTPServer()`. Both serve well-known discovery documents; neither requires JWT validation.

**`docs/design/security-architecture.md`** and **`docs/guides/authorization.md`**  
Document the two-layer auth model: gateway-level auth (AuthPolicy on the MCP route) and per-server auth (AuthPolicy on each backend HTTPRoute). A2A inherits this model directly. An A2A agent's HTTPRoute can carry its own AuthPolicy for agent-specific auth, while the main gateway route carries the client-facing policy.

### 2.7 Session Management

**`internal/session/cache.go`**  
`Cache` struct (line 15) with `inmemory *sync.Map`, `innerMu sync.Mutex` for copy-on-write serialization, and `extClient *redis.Client`. Methods:
- `AddSession(ctx, key, mcpServerID, mcpSession string)` — stores `map[sessionID → map[serverName → backendSessionID]]`.
- `GetSession(ctx, key)` — retrieves the per-session server→backendSession map.
- `RemoveServerSession(ctx, key, mcpServerID)` — COW deletion preserving the copy-on-write invariant established in PR #888.
- `SetClientElicitation` / `GetClientElicitation` — per-session boolean flag with `clientelicitation:` key prefix.

For A2A, task routing requires a different index: `taskID → {agentEndpoint, upstreamTaskID, sessionID}`. Rather than overloading the existing session map (which maps `sessionID → serverName → backendSession`), two new methods will be added to `Cache`:
```go
func (c *Cache) StoreTaskRoute(ctx context.Context, gatewayTaskID, agentName, upstreamTaskID, sessionID string) error
func (c *Cache) ResolveTaskRoute(ctx context.Context, gatewayTaskID string) (TaskRoute, bool, error)
func (c *Cache) DeleteTaskRoute(ctx context.Context, gatewayTaskID string) error
```
Redis keys: `a2atask:{gatewayTaskID}` → JSON-encoded `TaskRoute`. In-memory: separate `sync.Map` to avoid type-asserting against the existing `map[string]string` values.

**`internal/session/jwt.go`**  
`JWTManager` (line 34) generates and validates session JWTs, exposes `GetExpiresIn()` for timer-based session cleanup. A2A sessions use the same gateway session JWT — clients initialize with the broker at `/mcp`, get a `mcp-session-id` JWT, and include it in all subsequent A2A requests. This means A2A session validation (`s.validateSession(sessionID)` in `request_handlers.go:231`) reuses the existing `JWTManager` without changes.

**`internal/idmap/map.go`**  
`idmap.Map` interface (line 9) with `Store()`, `Lookup()`, `Remove()`. The `Entry` struct holds `BackendID`, `ServerName`, `SessionID`, `GatewaySessionID`. This is the elicitation ID rewriter's backing store. For A2A, the `TaskRoute` in the cache is a structural analogue — same key/value pattern, same Redis/in-memory duality — but task routes are longer-lived (task lifecycle vs single elicitation round-trip) so they need explicit TTL management.

### 2.8 Configuration & Hot-Reload

**`internal/config/types.go`**  
`MCPServersConfig` (line 15) holds `Servers []*MCPServer`, `VirtualServers []*VirtualServer`, `observers []Observer`, and the gateway hostname/key fields. The `Observer` interface (line 99) with `OnConfigChange(ctx, config)` is the hot-reload contract. All components that need config updates implement `Observer` and register via `RegisterObserver()`.

New fields to add to `MCPServersConfig`:
```go
A2AAgents []*A2AAgent  // analogous to Servers
```

New config type:
```go
type A2AAgent struct {
    Name       string      `json:"name"     yaml:"name"`
    URL        string      `json:"url"      yaml:"url"`
    Hostname   string      `json:"hostname" yaml:"hostname"`
    Prefix     string      `json:"prefix,omitempty" yaml:"prefix,omitempty"`
    Auth       *AuthConfig `json:"auth,omitempty"   yaml:"auth,omitempty"`
    Credential string      `json:"credential,omitempty" yaml:"credential,omitempty"`
    Enabled    bool        `json:"enabled"  yaml:"enabled"`
}
```

This is structurally identical to `MCPServer` — same fields, same ID computation, same credential injection pattern. The config writer in `internal/config/config_writer.go` will gain an `UpsertA2AAgent` / `RemoveA2AAgent` method pair, mirroring the existing MCP server config write path.

**`cmd/mcp-broker-router/main.go` — `LoadConfig()`** (line 396)  
The `viper.OnConfigChange` handler and `LoadConfig` function will be extended to read `a2aAgents` from the config YAML and populate `mcpConfig.A2AAgents`. The race-safe `SetServers` pattern (introduced in PR #922) will be applied to `A2AAgents` as well: unmarshal into a local slice, then call `mcpConfig.SetA2AAgents(slice)` under lock.

### 2.9 Testing Patterns

**`tests/e2e/`**  
E2E tests use port-forwards to `deployment/mcp-gateway`, send raw HTTP requests or use `mcp_client.go`, and check state via the broker `/status` endpoint. The A2A E2E tests will follow this same pattern: port-forward to the gateway, send A2A JSON-RPC requests directly via `net/http`, and check task state via `tasks/get`.

**`internal/controller/suite_test.go`**  
Sets up `envtest` for controller integration tests with Ginkgo/Gomega. The `A2AAgentRegistration` controller integration tests will add to this suite with identical setup.

**`internal/mcp-router/request_handlers_test.go`** (and `*_test.go` files throughout)  
Unit tests use `testing` + `testify/assert`. Router unit tests use mock ext_proc streams. A2A router unit tests follow the same pattern: construct an `ExtProcServer` with a mock `A2ATaskStore`, call `RouteA2ARequest()` directly, assert on the resulting `[]*eppb.ProcessingResponse`.

**`tests/servers/`**  
Go-based test servers (`server1`, `server2`, `api-key-server`, etc.) that implement MCP. The new `tests/servers/a2a-server/` will implement a minimal A2A agent: serves `/.well-known/agent.json`, handles `message/send` and `tasks/get` JSON-RPC, and returns deterministic responses for testing.

### 2.10 Any Existing Multi-Protocol or A2A Code

Searching the repo for `a2a`, `agent-to-agent`, `agent card`, `task`, and `multi-agent`:

- No existing A2A types, imports, or handlers are present. This is a greenfield addition.
- The closed PR #836 (A2A task-lifecycle routing infrastructure, closed 2026-05-01) contributed foundational thinking: it proposed a `taskID → serverName` index in the session cache and two new dispatch cases in `request_handlers.go`. The design doc for this LFX project supersedes that PR; its architectural insights inform the approach here.
- `internal/mcp-router/elicitation.go` — the SSE rewriter contains the streaming body processing pattern (`Process()` + `Flush()`) that will be directly reused for A2A streaming responses.
- `internal/idmap/` — the `Map` interface (in-memory and Redis implementations) provides the exact structural model for the A2A task route store.

---

## 3. Background: A2A Protocol Deep Dive

### 3a. A2A Core Concepts

**Agent Card**  
A JSON document served at `/.well-known/agent.json` (v0.2+) or `/.well-known/agent-card.json` (earlier drafts). Contains:
```json
{
  "name": "Weather Agent",
  "version": "1.0.0",
  "url": "https://gateway.example.com/a2a/weather",
  "description": "Provides weather forecasts",
  "skills": [
    { "id": "get_forecast", "name": "Get Forecast", "description": "..." }
  ],
  "authentication": {
    "schemes": ["Bearer"]
  }
}
```
In a federated gateway, the gateway's Agent Card aggregates skills from all registered upstream agents under the gateway's own URL.

**Task**  
The unit of work. JSON-RPC `message/send` submits a task; the response contains an initial Task object with an ID and status. Methods `tasks/get`, `tasks/cancel`, `tasks/resubscribe` operate on an existing Task by ID. Task IDs are opaque strings — the gateway generates a gateway-scoped task ID and maps it to the upstream agent's task ID.

**Message**  
Contains a `role` ("user" or "agent") and a `parts` array. Parts are typed: `TextPart`, `FilePart`, `DataPart`. Multi-modal from the ground up.

**Artifact**  
The output of a completed Task. Typed by MIME type; can be text, JSON, binary. Delivered in the final `tasks/get` response or in the last SSE event of a `message/stream`.

**JSON-RPC 2.0 over HTTP**  
A2A uses the same wire format as MCP — a JSON object with `jsonrpc: "2.0"`, `method`, `params`, and `id`. This means the existing `MCPRequest` JSON parsing infrastructure in `request_handlers.go` can be directly reused for parsing A2A request bodies; only the method constants and dispatch logic differ.

**Streaming & Push**  
`message/stream` (or `tasks/sendSubscribe` in older drafts) submits a task and opens an SSE stream. Each SSE event carries a task status update (`TaskStatusUpdateEvent`) or artifact chunk (`TaskArtifactUpdateEvent`). The stream ends when the task reaches a terminal state. The gateway must proxy this SSE stream without buffering: the `ModeOverride` pattern from elicitation handles this exactly.

Push notifications use a client-registered webhook URL (`tasks/pushNotification/set`). The upstream agent POSTs to that URL when the task completes. The gateway is a transparent proxy for push notification registration — it stores the push config and forwards it to the upstream agent without modification for the PoC scope.

**Authentication**  
A2A specifies the same schemes as OpenAPI: Bearer (OAuth 2.0), API key, mTLS. The gateway enforces these via Kuadrant AuthPolicy on the A2A HTTPRoute — identical to how MCP server auth is handled. The gateway's credential injection for upstream A2A agents (stored in `A2AAgent.Credential`) mirrors the `MCPServer.Credential` mechanism.

### 3b. A2A vs MCP — Gap Assessment

| Dimension | MCP | A2A |
|---|---|---|
| Primary axis | Vertical: agent ↔ tools/data | Horizontal: agent ↔ agent |
| Interaction model | Client invokes stateless tool calls | Clients submit long-running Tasks |
| Discovery mechanism | `initialize` capability handshake | Agent Card at `/.well-known/agent.json` |
| State model | Mostly stateless per tool call | Explicit state machine per Task |
| Long-running operations | Not first-class; tool calls are synchronous | First-class: `working`, `input-required` states |
| Streaming | SSE for initialize notifications only | SSE for task status updates, artifact streaming |
| Multi-modal | Tool inputs/outputs (JSON) | Messages with typed Parts (text, file, data) |
| Session scoping | `mcp-session-id` per client-gateway pair | `mcp-session-id` (gateway) + `taskId` (per-task) |
| Body rewriting needed | Yes: tool name prefix stripping | No: task bodies pass through unchanged |
| Federation primitive | Tool list aggregation | Agent Card skill aggregation |
| Push notifications | Not specified | Webhook-based push notification config |

**Gateway implications**:
- A2A routing is task-ID-based, not tool-name-based. The tool→server lookup (`Broker.GetServerInfo(toolName)`) is replaced by a task-route store lookup (`A2ATaskStore.Resolve(taskID)`).
- A2A streaming requires the same `ModeOverride` (STREAMED response body) as elicitation, but there is no ID rewriting — the SSE body passes through after the routing headers are set.
- A2A discovery is agent-card-based, requiring periodic HTTP fetches from the broker to upstream agents (like the broker's tool-list refresh) rather than capability negotiation at connection time.
- A2A does not require body modification for the PoC scope (no prefix stripping, no body rewriting). The router sets routing headers and lets Envoy forward the unchanged body.

### 3c. A2A Traffic Patterns Through a Gateway

**Agent Card Discovery Flow**
```
A2A Client → GET /.well-known/agent.json → Gateway (port 8001) → Envoy
           → ext_proc (A2A traffic does NOT go through ext_proc for GET requests)
           → Broker HTTP server → ServeAgentCard() → federated card JSON
```
Agent card requests are simple HTTP GETs. They do NOT flow through the ext_proc gRPC path — they are served directly by the broker's HTTP mux. This is correct: the ext_proc is invoked by Envoy's filter for POST requests carrying JSON-RPC bodies; GET requests to well-known endpoints are served by the upstream HTTP server directly.

**Task Submission Flow (`message/send`)**
```
A2A Client → POST /a2a → Gateway → Envoy → ext_proc gRPC stream
           → HandleRequestHeaders(): detect /a2a path, set isA2A=true
           → HandleRequestBody(): parse JSON-RPC, extract method=message/send, agentName
           → RouteA2ARequest(): lookup agent config, inject headers
           → Envoy routes to upstream A2A agent via :authority rewrite
           → Upstream A2A agent → response
           → HandleResponseHeaders(): set gateway taskID in response
           → A2A Client receives response with gateway taskID
```

**Streaming Task Flow (`message/stream`)**
```
A2A Client → POST /a2a → Gateway → ... (same as above) ...
           → HandleResponseHeaders(): set ModeOverride STREAMED
           → A2ASSEPassthrough.Process(): relay SSE chunks unchanged
           → A2A Client receives streaming task updates
```

**Task Polling Flow (`tasks/get`)**
```
A2A Client → POST /a2a → ext_proc
           → HandleRequestBody(): parse method=tasks/get, extract taskId
           → RouteA2ARequest(): lookup taskId in A2ATaskStore → get agentEndpoint
           → Inject :authority=agentEndpoint, x-a2a-task-id=upstreamTaskID
           → Envoy routes to correct upstream agent
```

**Push Notification Registration (`tasks/pushNotification/set`)**
```
A2A Client → POST /a2a → ext_proc
           → RouteA2ARequest(): method=tasks/pushNotification/set
           → Lookup taskId → get agentEndpoint
           → Forward to upstream agent unchanged
```

---

## 4. Architecture Design

### 4a. Overall Pipeline Architecture

```
                    ┌─────────────────────────────────────────────────────────┐
                    │  Kubernetes Cluster                                       │
                    │                                                           │
  A2A Client ───► Envoy Gateway (port 8001) ───► Envoy ext_proc filter ──┐   │
                    │                              (port 8080)              │   │
  MCP Client ───►  │                              gRPC stream to :50051   │   │
                    │                                                       ▼   │
                    │                              ExtProcServer.Process()  │   │
                    │                              ├── isA2A?               │   │
                    │                              │   └─ RouteA2ARequest() │   │
                    │                              └── else RouteMCPRequest()   │
                    │                                                           │
                    │  Broker HTTP (:8080)                                      │
                    │  ├── /mcp → MCP StreamableHTTPServer                      │
                    │  ├── /.well-known/agent.json → A2ACardHandler             │
                    │  ├── /.well-known/oauth-protected-resource                │
                    │  ├── /healthz /readyz /status                             │
                    │                                                           │
                    │  Controller (cmd/main.go)                                 │
                    │  ├── MCPReconciler (watches MCPServerRegistration)        │
                    │  └── A2AReconciler (watches A2AAgentRegistration)         │
                    │      └── writes config Secret → broker/router read it     │
                    └─────────────────────────────────────────────────────────┘
```

**Data formats at each stage:**
- A2A Client → Gateway: `POST /a2a` with `Content-Type: application/json`, body `{"jsonrpc":"2.0","method":"message/send","params":{...},"id":1}`
- ExtProcServer → Envoy: `ProcessingResponse` with `HeaderMutation` setting `:authority=agent-hostname`, `x-a2a-agent=agent-name`, `x-a2a-task-id=upstream-task-id`
- Gateway → Upstream A2A Agent: original JSON-RPC body (unchanged for PoC), routing headers set by ext_proc
- Upstream A2A Agent → Gateway: JSON response (sync) or SSE stream (async). SSE events: `data: {"jsonrpc":"2.0","result":{"id":"t1","status":{"state":"working"}}}`

### 4b. A2A Router (ext_proc) Design

The A2A router is NOT a separate ext_proc server. It is additional dispatch logic inside the existing `ExtProcServer.Process()` loop. This avoids a second gRPC connection, reuses all existing session/JWT infrastructure, and follows the established pattern (elicitation was added as additional cases in the existing loop, not a new server).

**Protocol Detection (Request Headers Phase)**

In `Process()` at `ProcessingRequest_RequestHeaders`:
```go
path := getSingleValueHeader(localRequestHeaders.Headers, ":path")
isA2A := strings.HasPrefix(path, "/a2a")
```

The `/a2a` path prefix is the canonical discriminator. All A2A traffic is sent to the `/a2a` endpoint; all MCP traffic goes to `/mcp`. This is a hard boundary: no content inspection needed at the header phase.

**A2A Request Struct**

```go
// internal/mcp-router/a2a_request.go
type A2ARequest struct {
    ID      any               `json:"id"`
    JSONRPC string            `json:"jsonrpc"`
    Method  string            `json:"method"`
    Params  map[string]any    `json:"params,omitempty"`
    Headers *corev3.HeaderMap `json:"-"`
    taskID  string            `json:"-"` // extracted from params or generated
    agentName string          `json:"-"` // resolved from agent registry
}

func (r *A2ARequest) isStreamingMethod() bool {
    return r.Method == a2aMethodMessageStream
}

func (r *A2ARequest) isTaskMethod() bool {
    switch r.Method {
    case a2aMethodTasksGet, a2aMethodTasksCancel, a2aMethodTasksResubscribe,
         a2aMethodTasksPushNotifSet, a2aMethodTasksPushNotifGet:
        return true
    }
    return false
}

func (r *A2ARequest) extractTaskID() string {
    if r.Params == nil { return "" }
    if id, ok := r.Params["id"].(string); ok { return id }
    return ""
}
```

**Routing Dispatch (`RouteA2ARequest`)**

```go
func (s *ExtProcServer) RouteA2ARequest(ctx context.Context, req *A2ARequest) []*eppb.ProcessingResponse {
    switch {
    case req.Method == a2aMethodMessageSend || req.Method == a2aMethodMessageStream:
        return s.HandleA2ATaskSend(ctx, req)
    case req.isTaskMethod():
        return s.HandleA2ATaskOperation(ctx, req)
    default:
        // unknown A2A method: return JSON-RPC error without forwarding
        return NewResponse().WithImmediateJSONRPCError(200, req.ID, -32601, "method not found").Build()
    }
}
```

**`HandleA2ATaskSend()` — Task Submission Routing**

```go
func (s *ExtProcServer) HandleA2ATaskSend(ctx context.Context, req *A2ARequest) []*eppb.ProcessingResponse {
    // 1. Validate gateway session JWT (same as MCP tool calls)
    if err := s.validateSession(req.GetSessionID()); err != nil { ... }

    // 2. Determine target agent from request params or header
    agentName := req.GetSingleHeaderValue("x-a2a-agent")
    if agentName == "" {
        agentName = req.extractAgentName() // from params.metadata.agent or route default
    }

    // 3. Look up agent config from A2ABroker
    agentConfig, err := s.A2ABroker.GetAgentInfo(agentName)
    if err != nil { /* return JSON-RPC error */ }

    // 4. Generate gateway task ID, store mapping (will be populated from response)
    gatewayTaskID := uuid.New().String()

    // 5. Build routing headers
    headers := NewHeaders()
    headers.WithAuthority(agentConfig.Hostname)
    headers.WithPath(agentConfig.Path())
    headers.WithA2AAgent(agentName)
    headers.WithA2ATaskID(gatewayTaskID)
    headers.WithA2AMethod(req.Method)

    // 6. Return routing instruction (body unchanged — no prefix stripping for A2A)
    response := NewResponse()
    if req.isStreamingMethod() {
        response.WithStreamingResponse(headers.Build(), nil) // body nil = keep original
    } else {
        response.WithRequestBodyHeadersResponse(headers.Build())
    }
    return response.Build()
}
```

**`HandleA2ATaskOperation()` — Task Lifecycle Routing**

For `tasks/get`, `tasks/cancel`, etc., the task ID is already known. The router looks up the upstream agent from the task store:

```go
func (s *ExtProcServer) HandleA2ATaskOperation(ctx context.Context, req *A2ARequest) []*eppb.ProcessingResponse {
    clientTaskID := req.extractTaskID()
    if clientTaskID == "" { /* return error */ }

    route, ok, err := s.A2ATaskStore.Resolve(ctx, clientTaskID)
    if err != nil { /* return 500 */ }
    if !ok { /* return JSON-RPC error: task not found */ }

    headers := NewHeaders()
    headers.WithAuthority(route.AgentHostname)
    headers.WithPath(route.AgentPath)
    headers.WithA2AAgent(route.AgentName)
    // rewrite the task ID in the body to the upstream task ID
    // (required because the upstream agent knows only its own task ID)
    body := rewriteTaskID(req, route.UpstreamTaskID)

    return NewResponse().WithRequestBodyHeadersAndBodyReponse(headers.Build(), body).Build()
}
```

**Response Headers Phase — Task ID Extraction**

When the upstream agent responds to `message/send`, its response contains a `result.id` (the upstream task ID). The response headers phase intercepts this to populate the task store mapping:

```go
// in HandleResponseHeaders(), after ModeOverride decision:
if isA2A && a2aReq.Method == a2aMethodMessageSend {
    // task ID extraction happens in response body phase
    a2aResp = &a2aResponseCapture{req: a2aReq, taskStore: s.A2ATaskStore}
}
```

The `a2aResponseCapture` accumulates the response body, parses the JSON-RPC result for `result.id`, and calls `A2ATaskStore.Store(ctx, gatewayTaskID, agentName, upstreamTaskID, sessionID)`.

**A2A Headers in `headers.go`**

```go
const (
    a2aAgentHeader  = "x-a2a-agent"
    a2aTaskIDHeader = "x-a2a-task-id"
    a2aMethodHeader = "x-a2a-method"
)

func (hb *HeadersBuilder) WithA2AAgent(name string) *HeadersBuilder { ... }
func (hb *HeadersBuilder) WithA2ATaskID(id string) *HeadersBuilder { ... }
func (hb *HeadersBuilder) WithA2AMethod(method string) *HeadersBuilder { ... }
```

### 4c. A2A Broker Design

**New file: `internal/broker/a2a_broker.go`**

```go
// A2ABroker manages upstream A2A agents and serves the federated Agent Card.
type A2ABroker interface {
    // GetAgentInfo returns config for the named A2A agent.
    GetAgentInfo(agentName string) (*config.A2AAgent, error)

    // ServeAgentCard handles GET /.well-known/agent.json
    ServeAgentCard(w http.ResponseWriter, r *http.Request)

    // OnConfigChange implements config.Observer
    OnConfigChange(ctx context.Context, conf *config.MCPServersConfig)

    // Shutdown releases resources
    Shutdown(ctx context.Context) error
}
```

**Agent Card Federation**

`buildFederatedAgentCard()` aggregates cards from all registered upstream A2A agents:

```go
func (b *a2aBrokerImpl) buildFederatedAgentCard() AgentCard {
    b.agentLock.RLock()
    defer b.agentLock.RUnlock()

    skills := make([]Skill, 0)
    for _, mgr := range b.agents {
        card := mgr.GetCachedCard()
        if card == nil { continue }
        for _, skill := range card.Skills {
            // prefix skill ID to avoid conflicts
            prefixed := skill
            prefixed.ID = mgr.Config().Prefix + skill.ID
            skills = append(skills, prefixed)
        }
    }
    return AgentCard{
        Name:        b.gatewayName,
        Version:     "1.0.0",
        URL:         b.gatewayURL + "/a2a",
        Description: "Kuadrant MCP Gateway - A2A Federated Agent",
        Skills:      skills,
        Authentication: AuthSchemes{Schemes: []string{"Bearer"}},
    }
}
```

**Skill merge strategy**: union of all upstream skills, with prefix applied to avoid ID collisions (mirrors the MCP tool prefix pattern). Authentication: the gateway advertises its own authentication scheme (Bearer/OAuth 2.0) regardless of what upstream agents require — the gateway is the enforcement point.

**Caching**: each `A2AAgentManager` holds a `cachedCard *AgentCard` with a TTL. The manager's ticker (like `MCPManager.ticker`) periodically re-fetches the Agent Card from the upstream agent's `/.well-known/agent.json`. On `OnConfigChange`, the card cache is invalidated for changed agents.

```go
type A2AAgentManager struct {
    config     *config.A2AAgent
    cachedCard *AgentCard
    cardMu     sync.RWMutex
    cardExpiry time.Time
    cardTTL    time.Duration // default 5 minutes
    ticker     *time.Ticker
    done       chan struct{}
    logger     *slog.Logger
}
```

### 4d. A2A Controller & CRD Design

**Recommendation: New `A2AAgentRegistration` CRD**

Option A (new CRD) is chosen over Option B (extend MCPServerRegistration with a `protocol` field) for these reasons:
1. A2A agents have a distinct field set — `agentCardURL` for Agent Card override, no concept of tool prefix (skill prefix instead), different status conditions (`AgentCardDiscovered` vs `DiscoveredTools` count). Forcing these into `MCPServerRegistration` produces optionally-empty fields that create an awkward API.
2. Kuadrant's Gateway API-influenced design philosophy (visible in the existing CRD structure) favors protocol-specific resource types — `MCPServerRegistration` for MCP, `A2AAgentRegistration` for A2A. This follows the Gateway API pattern of `HTTPRoute`, `GRPCRoute`, `TCPRoute`.
3. Controller isolation: the A2A controller can be developed, tested, and reviewed independently of the MCP controller without the risk of regressions.

**`api/v1alpha1/a2aagentregistration_types.go`**

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=a2aar
// +kubebuilder:printcolumn:name="Prefix",type="string",JSONPath=".spec.skillPrefix"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.targetRef.name"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Skills",type="integer",JSONPath=".status.discoveredSkills"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

type A2AAgentRegistration struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   A2AAgentRegistrationSpec   `json:"spec,omitempty"`
    Status A2AAgentRegistrationStatus `json:"status,omitempty"`
}

type A2AAgentRegistrationSpec struct {
    // targetRef references the HTTPRoute pointing to the upstream A2A agent.
    // +required
    TargetRef TargetReference `json:"targetRef"`

    // skillPrefix is prepended to all skill IDs from this agent during federation.
    // Prevents skill ID collisions across multiple registered agents.
    // +optional
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="skillPrefix is immutable once set"
    SkillPrefix string `json:"skillPrefix,omitempty"`

    // agentCardURL overrides the well-known URL for fetching the Agent Card.
    // If not set, defaults to {upstream-url}/.well-known/agent.json
    // +optional
    AgentCardURL string `json:"agentCardURL,omitempty"`

    // credentialRef references a Secret with auth credentials for the upstream agent.
    // +optional
    CredentialRef *SecretReference `json:"credentialRef,omitempty"`
}

type A2AAgentRegistrationStatus struct {
    // conditions represent the current state of this registration.
    // Condition types: Ready, AgentCardDiscovered, EndpointReachable
    // +listType=map
    // +listMapKey=type
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // discoveredSkills is the count of skills in the most recently fetched Agent Card.
    // +optional
    DiscoveredSkills int32 `json:"discoveredSkills,omitempty"`
}
```

**`internal/controller/a2aagentregistration_controller.go`**

Controller reconcile loop, mirroring `MCPReconciler`:

```go
type A2AReconciler struct {
    client.Client
    Scheme             *runtime.Scheme
    DirectAPIReader    client.Reader
    ConfigReaderWriter A2AConfigReaderWriter
    MCPExtFinder       MCPGatewayExtensionFinderValidator
}

func (r *A2AReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
    a2aar := &mcpv1alpha1.A2AAgentRegistration{}
    if err := r.Get(ctx, req.NamespacedName, a2aar); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // deletion handling with finalizer (identical pattern to MCPReconciler)
    // ...

    // resolve HTTPRoute → endpoint and hostname
    targetRoute, err := r.getTargetHTTPRoute(ctx, a2aar)
    // ...

    // build A2AAgent config from HTTPRoute
    agentConfig, err := r.buildA2AAgentConfig(ctx, targetRoute, a2aar)
    // ...

    // write to config Secret in all valid MCPGatewayExtension namespaces
    for _, ns := range validNamespaces {
        r.ConfigReaderWriter.UpsertA2AAgent(ctx, *agentConfig, ns)
    }

    // set status: try fetching Agent Card to verify endpoint reachability
    return r.setA2AAgentRegistrationStatus(ctx, a2aar, agentConfig)
}
```

Status conditions set by the controller:
- `AgentCardDiscovered=True` — successfully fetched and parsed Agent Card from upstream
- `EndpointReachable=True` — HTTP GET to upstream succeeded (even if card parsing fails)
- `Ready=True` — both above conditions True

### 4e. Policy Enforcement Design

**AuthPolicy Integration**

A2A requests carry OAuth 2.0 bearer tokens in the `Authorization` header — identical to MCP requests. The existing Kuadrant AuthPolicy mechanism applies without change:

1. Client presents `Authorization: Bearer <token>` to the gateway.
2. Kuadrant AuthPolicy on the gateway's HTTPRoute (the `/a2a` route) triggers Authorino to validate the JWT.
3. Authorino validates the token, extracts claims, and injects `x-mcp-authorized` (or a new `x-a2a-authorized`) signed trusted header.
4. The gateway forwards the request to the ext_proc with the validated header.
5. The ext_proc uses the header in skill-level authorization decisions.

For the PoC, skill-level authorization is: if `x-a2a-authorized` header is present and signed (same mechanism as `x-mcp-authorized` for tool filtering), the A2A broker will filter the skills returned in the Agent Card to only those the client is authorized to use. This is the direct A2A analog of the existing `enforceCapabilityFilter` / `FilterTools` mechanism in `broker.go:154`.

**RateLimitPolicy Integration**

RateLimitPolicy attaches to the `/a2a` HTTPRoute by `targetRef`. Rate limits by:
- `x-a2a-method` header: throttle `message/stream` (resource-intensive streaming tasks) more strictly than `tasks/get` (lightweight polling).
- Source IP or client identity (from JWT claims extracted by Authorino).

No gateway code changes are needed for rate limiting — it is handled entirely by Kuadrant's Limitador component at the Envoy filter level before ext_proc.

**Skill Authorization (future, noted in design doc)**  
For post-PoC: extend the A2A broker's `ServeAgentCard()` to filter skills based on the `x-a2a-authorized` signed header, analogous to `FilterTools()` in `filtered_tools_handler.go`. The gateway advertises only the skills the client is authorized to invoke.

### 4f. Session & Task State Management

**Task Store Interface**

```go
// internal/session/a2a_task_store.go (or added to cache.go)

type TaskRoute struct {
    AgentName      string `json:"agentName"`
    AgentHostname  string `json:"agentHostname"`
    AgentPath      string `json:"agentPath"`
    UpstreamTaskID string `json:"upstreamTaskID"`
    SessionID      string `json:"sessionID"` // gateway session that owns this task
    CreatedAt      int64  `json:"createdAt"`
}

// TaskStore maps gateway-assigned task IDs to upstream agent task context.
type TaskStore interface {
    Store(ctx context.Context, gatewayTaskID string, route TaskRoute) error
    Resolve(ctx context.Context, gatewayTaskID string) (TaskRoute, bool, error)
    Delete(ctx context.Context, gatewayTaskID string) error
}
```

**Implementation: extend `session.Cache`**

The task store is implemented as new methods on `session.Cache`, following the same in-memory/Redis duality:

```go
// In-memory: store in a separate sync.Map (avoid type collision with session map)
func (c *Cache) StoreTaskRoute(ctx context.Context, gatewayTaskID string, route TaskRoute) error {
    data, err := json.Marshal(route)
    if err != nil { return err }
    if c.inmemory != nil {
        c.taskRoutes.Store(gatewayTaskID, string(data))
        return nil
    }
    return c.extClient.Set(ctx, "a2atask:"+gatewayTaskID, string(data), 24*time.Hour).Err()
}

func (c *Cache) ResolveTaskRoute(ctx context.Context, gatewayTaskID string) (TaskRoute, bool, error) {
    var route TaskRoute
    if c.inmemory != nil {
        val, ok := c.taskRoutes.Load(gatewayTaskID)
        if !ok { return route, false, nil }
        return route, true, json.Unmarshal([]byte(val.(string)), &route)
    }
    data, err := c.extClient.Get(ctx, "a2atask:"+gatewayTaskID).Result()
    if errors.Is(err, redis.Nil) { return route, false, nil }
    if err != nil { return route, false, err }
    return route, true, json.Unmarshal([]byte(data), &route)
}
```

`Cache` gets a new `taskRoutes sync.Map` field alongside `inmemory sync.Map`. The `innerMu` serialization lock applies to task route mutations the same way it applies to session map COW mutations (PR #888 pattern).

**TTL**: In-memory task routes have no TTL in the PoC (tasks are cleaned up when the gateway session expires via the `sessionCloser` timer in `initializeMCPSeverSession`). Redis task routes use a 24h TTL to match the default JWT session duration.

**Distinction from MCP Session Cache**

| Dimension | MCP Session Cache | A2A Task Store |
|---|---|---|
| Key | `gatewaySessionID` | `gatewayTaskID` |
| Value | `map[serverName → backendSessionID]` | `TaskRoute{agentName, upstreamTaskID, ...}` |
| Populated by | `initializeMCPSeverSession()` on first tool call | Response body capture after `message/send` |
| Lifetime | Gateway JWT session lifetime | Task lifetime (task terminal state or session expiry) |
| Redis key prefix | (none) | `a2atask:` |

### 4g. Envoy Configuration & ext_proc Path

**EnvoyFilter — No Changes Required for Basic A2A**

The existing EnvoyFilter (`config/istio/envoyfilter.yaml`) targets port 8080, processes all HTTP traffic through ext_proc, and allows mode override (`allow_mode_override: true`). The A2A extension requires:
1. A2A requests must arrive on port 8080 — they do, through the same Gateway listener.
2. `request_body_mode: STREAMED` is already set — A2A request bodies are accumulated by the existing chunked body buffer in `Process()`.
3. `response_body_mode: NONE` is the default — streaming responses use `ModeOverride` in `HandleResponseHeaders()`, already implemented for elicitation.

**HTTPRoute for A2A**

A new HTTPRoute is needed to route `/a2a/*` traffic to the broker service, and per-agent HTTPRoutes for each upstream A2A agent (mirroring the per-MCP-server HTTPRoute pattern):

```yaml
# Gateway-level: /a2a/* → broker A2A handler (for agent card and direct proxying)
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: a2a-gateway-route
  namespace: mcp-system
spec:
  parentRefs:
  - name: mcp-gateway
    namespace: gateway-system
  hostnames:
  - "mcp.example.com"
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /a2a
    backendRefs:
    - name: mcp-broker
      port: 8080
---
# Per-agent: route with URL rewrite to upstream A2A agent
# (same pattern as per-MCP-server routes)
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: weather-agent-route
  namespace: mcp-test
spec:
  ...
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /a2a/weather
    backendRefs:
    - name: weather-agent-service
      port: 8080
```

**Protocol Detection in ext_proc**

```
Request arrives at ext_proc:
  ProcessingRequest_RequestHeaders:
    path = getSingleValueHeader(headers, ":path")
    isA2A = strings.HasPrefix(path, "/a2a")
    ↓
  ProcessingRequest_RequestBody:
    if isA2A:
        parse body into A2ARequest
        call RouteA2ARequest(ctx, a2aReq)
    else:
        parse body into MCPRequest
        call RouteMCPRequest(ctx, mcpReq)
```

The URL path discriminator is unambiguous: `/a2a` is for A2A, `/mcp` is for MCP. There is no scenario where content-type inspection or JSON-RPC method inspection is needed at the header phase.

---

## 5. Implementation Phases

### Phase 1 — Codebase Deep Dive & A2A Analysis (Weeks 1–2)

**Week 1: Repository Mapping**

*Files read:* All files listed in Section 2. Primary focus: `internal/mcp-router/`, `internal/broker/`, `internal/session/`, `internal/config/`, `api/v1alpha1/`, `internal/controller/`.

*Deliverables:*
- `docs/a2a/codebase-analysis.md`: per-component analysis documenting every integration point identified above, with line-number citations.
- List of questions for mentors (see Section 9).

*Testable output:* Document reviewed with mentor; no code changes this week.

**Week 2: A2A Protocol Study & Test Environment**

*Work:*
- Read A2A specification v0.2+ (a2aproject.org), focusing on: Agent Card JSON schema, Task state machine, JSON-RPC method signatures for `message/send`, `message/stream`, `tasks/get`, `tasks/cancel`, streaming SSE event format.
- Build a standalone A2A agent in Go (`tests/servers/a2a-server/main.go`) that:
  - Serves `/.well-known/agent.json` with a minimal card (name, one skill, Bearer auth)
  - Handles `message/send` → returns a completed Task
  - Handles `tasks/get` → returns the task status
  - Handles `message/stream` → sends 3 SSE task update events then a completion event
- Run the standalone agent locally and verify with `curl`.

*Files created:*
- `tests/servers/a2a-server/main.go`
- `tests/servers/a2a-server/Dockerfile`
- `docs/a2a/gap-assessment.md`

*Testable output:* `curl http://localhost:9090/.well-known/agent.json` returns a valid Agent Card. `curl -X POST http://localhost:9090/a2a -d '{"jsonrpc":"2.0","method":"message/send","params":{...},"id":1}'` returns a completed task.

---

### Phase 2 — Design Document (Weeks 3–4)

**Week 3: Write Design Document**

*Work:*
- Write `docs/design/a2a/a2a-design.md` following the project's established design doc format (see `docs/design/CLAUDE.md`): Problem, Summary, Goals/Non-Goals, Job Stories, Design (prerequisites, flow diagrams, component responsibilities, API changes, data storage), Security Considerations.
- Write `docs/design/a2a/tasks/tasks.md` with ordered implementation tasks derived from this plan.
- Write `docs/design/a2a/tasks/e2e_test_cases.md` following the test case format from `tests/e2e/test_cases.md`.
- Submit design PR for mentor review.

*Files created:*
- `docs/design/a2a/a2a-design.md`
- `docs/design/a2a/tasks/tasks.md`
- `docs/design/a2a/tasks/e2e_test_cases.md`

*Testable output:* PR opened; mentor feedback requested.

**Week 4: Iterate on Design & Build A2A Test Infrastructure**

*Work:*
- Address mentor feedback on design doc. Finalize: CRD schema (field names, validation rules), task store interface, SSE passthrough strategy, HTTPRoute pattern for A2A agents.
- Deploy the standalone A2A test server (`tests/servers/a2a-server/`) to the local Kind cluster. Create `config/test-servers/a2a-server-deployment.yaml`, `config/test-servers/a2a-server-service.yaml`, `config/test-servers/a2a-server-httproute.yaml` mirroring the existing test server manifests (e.g., `config/test-servers/server1-deployment.yaml`).
- Verify the test server is reachable through the gateway's Envoy proxy (bypassing ext_proc for now via direct HTTPRoute).

*Files created/modified:*
- `docs/design/a2a/a2a-design.md` (revised)
- `config/test-servers/a2a-server-deployment.yaml`
- `config/test-servers/a2a-server-service.yaml`
- `config/test-servers/a2a-server-httproute.yaml`
- `config/test-servers/kustomization.yaml` (add a2a-server)

*Testable output:* `kubectl port-forward -n test-servers svc/a2a-server 9090:8080` then `curl http://localhost:9090/.well-known/agent.json` returns the A2A test server's card.

---

### Phase 3 — Core A2A Components (Weeks 5–7)

**Week 5: A2AAgentRegistration CRD & Controller**

*Files created:*
- `api/v1alpha1/a2aagentregistration_types.go` — `A2AAgentRegistration`, `A2AAgentRegistrationSpec`, `A2AAgentRegistrationStatus` types as defined in Section 4d.
- `api/v1alpha1/zz_generated.deepcopy.go` — regenerated via `make generate-all`.
- `config/crd/mcp.kuadrant.io_a2aagentregistrations.yaml` — generated.
- `internal/controller/a2aagentregistration_controller.go` — `A2AReconciler.Reconcile()` as outlined in Section 4d.
- `internal/controller/a2aagentregistration_controller_test.go` — unit tests for reconcile cases.

*Key functions implemented:*
- `A2AReconciler.Reconcile()`: finalizer management, HTTPRoute resolution (reuse `r.getTargetHTTPRoute()`), agent config construction (new `buildA2AAgentConfig()`), config write.
- `buildA2AAgentConfig()`: mirrors `buildMCPServerConfig()` in `mcpserverregistration_controller.go:369`, using the same `buildServerInfoFromHTTPRoute()` helper.
- `A2AReconciler.SetupWithManager()`: register watches on `A2AAgentRegistration`, `HTTPRoute`, `Secret` — identical predicate setup to `MCPReconciler.SetupWithManager()`.
- `setA2AAgentRegistrationStatus()`: fetch Agent Card from upstream to verify `AgentCardDiscovered` condition.

*Run `make generate-all` to regenerate CRD YAML and deepcopy.*

*Tests (`internal/controller/a2aagentregistration_controller_test.go`):*
- `TestReconcileNewA2ARegistration`: verifies config is written on new resource.
- `TestReconcileHTTPRouteNotFound`: sets Ready=False when HTTPRoute missing.
- `TestReconcileAgentCardFetchFailure`: sets AgentCardDiscovered=False when upstream unreachable.
- `TestReconcileA2ARegistrationDeletion`: verifies finalizer removal and config cleanup.

*Testable output:* `make test-unit` and `make test-controller-integration` pass.

**Week 6: A2A Router (ext_proc)**

*Files created:*
- `internal/mcp-router/a2a_request.go` — `A2ARequest` type, method constants, helper methods (`isStreamingMethod()`, `isTaskMethod()`, `extractTaskID()`, `extractAgentName()`).
- `internal/mcp-router/a2a_router.go` — `RouteA2ARequest()`, `HandleA2ATaskSend()`, `HandleA2ATaskOperation()`, `a2aSSEPassthrough` type.
- `internal/mcp-router/a2a_router_test.go` — unit tests for all routing cases.

*Files modified:*
- `internal/mcp-router/headers.go` — add `WithA2AAgent()`, `WithA2ATaskID()`, `WithA2AMethod()` to `HeadersBuilder`; add A2A header constants.
- `internal/mcp-router/server.go` — add `A2ATaskStore session.TaskStore` and `A2ABroker a2a.Broker` fields to `ExtProcServer`; add `isA2A bool` local variable in `Process()` loop; add A2A dispatch at `ProcessingRequest_RequestBody` and `ProcessingRequest_ResponseHeaders`.

*Key functions implemented:*
- `RouteA2ARequest(ctx, req)` — method dispatch switch.
- `HandleA2ATaskSend(ctx, req)` — session validation, agent lookup, header injection, streaming mode decision.
- `HandleA2ATaskOperation(ctx, req)` — task store lookup, upstream task ID rewrite, routing.

*Tests:*
- `TestParseA2AJSONRPC`: correctly parses `message/send`, `tasks/get` etc.
- `TestRouteA2ATaskSend`: verifies `:authority`, `:path`, `x-a2a-agent`, `x-a2a-task-id` headers set correctly.
- `TestRouteA2AStreamingMode`: verifies `ModeOverride` set for `message/stream`.
- `TestRouteA2ATasksGet`: task store lookup routes to correct agent.
- `TestProtocolDetectionA2AvssMCP`: `/a2a` path dispatches to A2A handler; `/mcp` dispatches to MCP handler.
- `TestA2AUnknownMethod`: unknown A2A method returns JSON-RPC -32601 error.

*Testable output:* `make test-unit` passes.

**Week 7: A2A Broker**

*Files created:*
- `internal/broker/a2a_broker.go` — `A2ABroker` interface, `a2aBrokerImpl` struct, `NewA2ABroker()`, `ServeAgentCard()`, `buildFederatedAgentCard()`, `GetAgentInfo()`, `OnConfigChange()`, `Shutdown()`.
- `internal/broker/a2a_broker_test.go` — unit tests.
- `internal/broker/upstream/a2a_manager.go` — `A2AAgentManager` struct, `Start()`, `Stop()`, `FetchAgentCard()`, `GetCachedCard()`.

*Files modified:*
- `internal/config/types.go` — add `A2AAgents []*A2AAgent` to `MCPServersConfig`; add `A2AAgent` struct; add `SetA2AAgents()` locked setter.
- `cmd/mcp-broker-router/main.go` — `setUpHTTPServer()`: register `/.well-known/agent.json` handler; `LoadConfig()`: parse `a2aAgents` key; inject `A2ABroker` into `ExtProcServer`.

*Tests:*
- `TestServeFederatedAgentCard`: GET returns merged card with prefixed skills.
- `TestFetchUpstreamAgentCard`: agent manager fetches and parses card correctly.
- `TestAgentCardCacheHit`: second fetch within TTL returns cached value, no HTTP call.
- `TestAgentCardCacheInvalidation`: `OnConfigChange` triggers re-fetch.
- `TestGetAgentInfoFound`: returns config for known agent.
- `TestGetAgentInfoNotFound`: returns error for unknown agent.

*Testable output:* `make test-unit` passes. With local Kind cluster and A2A test server deployed: `curl http://mcp.127-0-0-1.sslip.io:8001/.well-known/agent.json` returns a federated Agent Card containing the test server's skills.

---

### Phase 4 — Integration & Policy (Weeks 8–9)

**Week 8: Protocol Integration & Envoy Config**

*Work:*
- Wire `A2ABroker` and `A2ATaskStore` into the `ExtProcServer` in `setUpRouter()` in `cmd/mcp-broker-router/main.go`.
- Add the `/a2a` HTTPRoute to the test setup (`config/e2e/gateway-1.yaml` or a new A2A-specific manifest).
- Create a sample `A2AAgentRegistration` manifest in `config/samples/a2aagentregistration-test-server.yaml`.
- Register `A2AAgentRegistration` in the controller's scheme in `cmd/main.go` and add `A2AReconciler` to the controller manager.
- Run the full gateway locally and exercise A2A flow end-to-end: create `A2AAgentRegistration`, verify controller sets it Ready, verify broker fetches Agent Card, POST `message/send` to gateway, verify routing to test server.

*Files modified:*
- `cmd/mcp-broker-router/main.go` — wire A2A components.
- `cmd/main.go` — register `A2AAgentRegistration` scheme, add `A2AReconciler.SetupWithManager()`.
- `config/samples/a2aagentregistration-test-server.yaml` — sample manifest.

*Tests:*
- Integration test: `TestA2AFullStack`: deploy test server, create `A2AAgentRegistration`, POST `message/send`, verify response.

*Testable output:* Manual validation with `curl` against the local Kind cluster. MCP traffic must be unaffected — run existing `make test-unit` and `make test-controller-integration`.

**Week 9: Session, Task Store, and Policy**

*Work:*
- Implement `session.TaskStore` interface and methods on `session.Cache` as designed in Section 4f.
- Wire task store into the router: populate task store from `message/send` responses (response body capture in `HandleResponseHeaders`/`ProcessingRequest_ResponseBody`).
- Verify `tasks/get` routing works: POST `message/send`, capture gateway task ID from response, POST `tasks/get` with gateway task ID, verify request routed to correct upstream agent with upstream task ID.
- Create A2A-specific `AuthPolicy` sample in `config/samples/a2a-auth-policy.yaml` demonstrating token validation for `/a2a` traffic.

*Files created/modified:*
- `internal/session/cache.go` — add `taskRoutes sync.Map` field, `StoreTaskRoute()`, `ResolveTaskRoute()`, `DeleteTaskRoute()`.
- `internal/session/cache_test.go` — add tests for task route CRUD and concurrency.
- `internal/mcp-router/a2a_router.go` — add response body capture for task ID extraction.
- `config/samples/a2a-auth-policy.yaml` — sample AuthPolicy.

*Tests:*
- `TestStoreAndResolveTaskRoute`: store a route, resolve by gateway task ID.
- `TestTaskRouteNotFound`: resolve returns false for unknown ID.
- `TestConcurrentTaskRouteAccess`: 100 goroutines writing and reading without data race (run with `-race`).
- `TestTasksGetRouting`: end-to-end routing of `tasks/get` using task store.

*Testable output:* `make test-unit` passes with `-race`. Manual: `tasks/get` routes to correct upstream agent.

---

### Phase 5 — End-to-End & Hardening (Weeks 10–11)

**Week 10: E2E Tests**

*Files created:*
- `tests/e2e/a2a_discovery_test.go` — agent card discovery tests.
- `tests/e2e/a2a_task_execution_test.go` — task submission, completion, streaming tests.

*Test functions (Ginkgo/Gomega, following patterns in `tests/e2e/happy_path_test.go`):*

Discovery:
```go
Describe("A2A Agent Discovery", func() {
    It("serves federated Agent Card at /.well-known/agent.json", func() { ... })
    It("includes skills from all registered A2AAgentRegistrations", func() { ... })
    It("reflects A2AAgentRegistration deletion within one minute", func() { ... })
})
```

Task Execution:
```go
Describe("A2A Task Execution", func() {
    It("routes message/send to the correct upstream agent", func() { ... })
    It("returns gateway task ID in response", func() { ... })
    It("routes tasks/get using gateway task ID", func() { ... })
    It("routes tasks/cancel to the agent that owns the task", func() { ... })
    It("streams task updates via message/stream", func() { ... })
    It("rejects task operation for unknown task ID", func() { ... })
    It("rejects request with invalid gateway session JWT", func() { ... })
    It("returns JSON-RPC -32601 for unknown A2A method", func() { ... })
})
```

MCP Regression:
```go
Describe("MCP Traffic Unaffected by A2A Changes", func() {
    It("tools/list still returns all MCP tools", func() { ... })
    It("tools/call still routes to correct MCP backend", func() { ... })
})
```

*Testable output:* All E2E tests pass against local Kind cluster: `ginkgo -v ./tests/e2e/... -- --gateway-host=mcp.127-0-0-1.sslip.io:8001`.

**Week 11: Performance & Hardening**

*Work:*
- Run existing performance tests (`tests/perf/k6/`) against the gateway with A2A traffic mixed in. Baseline: A2A adds <5ms latency to `message/send` routing (dominated by the gRPC ext_proc hop, which is unchanged by A2A).
- Profile with pprof (port 6060, already wired in `main.go:266`) under concurrent A2A task load. Identify any allocations in the hot path (`RouteA2ARequest`, task store lookup).
- Fix any identified allocations: use pointer maps for agent lookup in `a2aBrokerImpl` (mirrors the `map[string]*T` guidance in CLAUDE.md performance section), guard span attribute calls with `if span.IsRecording()`.
- Run with `-race` detector: `go test -race ./internal/...`.
- Fix any data races identified in `A2AAgentManager.cachedCard` (protect with `cardMu sync.RWMutex`).

*Testable output:* `go test -race ./...` passes. pprof shows no unexpected heap allocations in A2A routing path.

---

### Phase 6 — Documentation & Delivery (Week 12)

**Week 12**

*Files created:*
- `docs/guides/a2a-setup.md` — user-facing guide: how to register an A2A agent with the gateway (create `A2AAgentRegistration`, verify status, test agent card discovery and task routing). Follows the format of `docs/guides/register-mcp-servers.md`.
- `docs/design/a2a/a2a-architecture.md` — technical deep dive for contributors: component responsibilities, data flow, task store design, integration points with existing code.
- Helm chart updates (`charts/mcp-gateway/crds/`) — add generated CRD YAML for `A2AAgentRegistration`.
- RBAC updates (`charts/mcp-gateway/templates/rbac.yaml`) — add `a2aagentregistrations` resource rules.

*Final PR polish:*
- Address all review comments from mentor on Week 8–11 PRs.
- Ensure `make lint` passes (`golangci-lint` with project config).
- Ensure `make generate-all` is clean (no uncommitted generated file diffs).
- Write project report summarizing: what was built, what was deferred, key design decisions and their rationale.

---

## 6. Test Plan

### CRD & Controller Tests

**`api/v1alpha1/a2aagentregistration_types_test.go`** (package `v1alpha1`)
- `TestA2AAgentRegistrationDefaulting` — validates that `skillPrefix` and `agentCardURL` default correctly; `TargetRef.Kind` defaults to `HTTPRoute`.
- `TestA2AAgentRegistrationValidation` — `targetRef.name` is required; `skillPrefix` is immutable once set (CEL validation rule).
- `TestA2AAgentRegistrationStatusConditions` — verifies that `Ready`, `AgentCardDiscovered`, `EndpointReachable` conditions are settable with correct types.

**`internal/controller/a2aagentregistration_controller_test.go`** (package `controller`, Ginkgo/Gomega + envtest)
- `TestReconcileNewA2ARegistration` — creates `A2AAgentRegistration` with valid HTTPRoute; verifies A2A agent config is written to Secret and `Ready=True`.
- `TestReconcileHTTPRouteNotFound` — `A2AAgentRegistration` references non-existent HTTPRoute; verifies `Ready=False` with appropriate message.
- `TestReconcileAgentCardFetchFailure` — upstream is unreachable; verifies `AgentCardDiscovered=False` condition is set.
- `TestReconcileAgentCardInvalidJSON` — upstream returns invalid JSON for Agent Card; verifies `AgentCardDiscovered=False`.
- `TestReconcileCredentialSecretMissingLabel` — credential Secret exists but lacks `mcp.kuadrant.io/secret=true`; verifies reconcile returns error and `Ready=False`.
- `TestReconcileA2ARegistrationDeletion` — deletion: verifies finalizer removed, agent config deleted from Secret.
- `TestReconcileSkillPrefixImmutability` — changing `skillPrefix` after initial set returns validation error (CEL enforced at API server level; test via webhook if configured).

### A2A Router Tests

**`internal/mcp-router/a2a_router_test.go`** (package `mcprouter`, `testing` + `testify`)
- `TestParseA2AJSONRPCMessageSend` — body `{"jsonrpc":"2.0","method":"message/send","params":{...},"id":1}` parses into `A2ARequest` with correct fields.
- `TestParseA2AJSONRPCTasksGet` — `tasks/get` with `params.id` correctly extracts task ID.
- `TestRouteA2ATaskSendSetsHeaders` — `HandleA2ATaskSend` sets `:authority`, `:path`, `x-a2a-agent`, `x-a2a-task-id`, `x-a2a-method` headers; does not modify request body.
- `TestRouteA2ATaskSendStreamingModeOverride` — `message/stream` triggers `ModeOverride` with `ResponseBodyMode: STREAMED` in response headers.
- `TestRouteA2ATasksGetLooksUpStore` — `tasks/get` calls `TaskStore.Resolve()`; routing headers set to agent from resolved route.
- `TestRouteA2ATasksGetUnknownTaskID` — `TaskStore.Resolve()` returns not-found; response is immediate JSON-RPC error `{"jsonrpc":"2.0","error":{"code":-32602,"message":"task not found"},"id":1}`.
- `TestRouteA2ATasksCancel` — `tasks/cancel` routes to correct agent with upstream task ID in body.
- `TestProtocolDetectionPathPrefix` — path `/a2a` sets `isA2A=true`; path `/mcp` sets `isA2A=false`; path `/` sets `isA2A=false`.
- `TestA2AUnknownMethod` — method `unknown/method` with `/a2a` path returns JSON-RPC -32601 immediately without forwarding.
- `TestA2ASessionValidationFails` — expired/invalid JWT returns 404 before agent lookup.
- `TestMCPTrafficUnchangedAfterA2AChanges` — `tools/call` request on `/mcp` path still routes through `HandleToolCall` with no interference from A2A dispatch.

### A2A Broker Tests

**`internal/broker/a2a_broker_test.go`** (package `broker`, `testing` + `testify`)
- `TestServeFederatedAgentCard` — `GET /.well-known/agent.json` returns 200 with `Content-Type: application/json`; body is valid JSON with `name`, `skills`, `url` fields.
- `TestFederatedAgentCardMergesSkills` — two registered agents contribute skills; card contains all skills with correct prefix applied.
- `TestFederatedAgentCardEmptyWhenNoAgents` — no registered agents; card contains empty skills array.
- `TestFetchUpstreamAgentCard` — `A2AAgentManager.FetchAgentCard()` makes HTTP GET to `/.well-known/agent.json` and parses response.
- `TestAgentCardCacheHit` — second `GetCachedCard()` within TTL returns cached value without making another HTTP request.
- `TestAgentCardCacheInvalidation` — `OnConfigChange` with changed agent config clears cached card.
- `TestGetAgentInfoFound` — returns `*config.A2AAgent` for registered agent name.
- `TestGetAgentInfoNotFound` — returns error for unregistered agent name.
- `TestA2ABrokerOnConfigChangeAddsAgent` — new agent in config triggers `A2AAgentManager` creation and card fetch.
- `TestA2ABrokerOnConfigChangeRemovesAgent` — removed agent in config stops its manager and removes from registry.

### Session & Task Store Tests

**`internal/session/cache_test.go`** (additions to existing test file)
- `TestStoreAndResolveTaskRoute` — store `TaskRoute`, resolve by `gatewayTaskID`, verify all fields match.
- `TestResolveNonExistentTaskRoute` — returns `(TaskRoute{}, false, nil)` for unknown ID.
- `TestDeleteTaskRoute` — store then delete; subsequent resolve returns not-found.
- `TestTaskRouteConcurrency` — 100 goroutines each storing unique task IDs concurrently; no panics or races (`-race`).
- `TestTaskRouteDoesNotConflictWithSessionCache` — task route key `a2atask:X` does not collide with session map key `X`; both store/retrieve independently.
- `TestTaskRouteRedisRoundTrip` — with real Redis client (if `REDIS_URL` env set), store and resolve task route via Redis.

### Policy Enforcement Tests

**`internal/broker/a2a_skill_authorization_test.go`** (new file)
- `TestSkillFilteringWithAuthorizedHeader` — `x-a2a-authorized` header containing a signed JWT listing skills `["weather_get_forecast"]`; `ServeAgentCard` returns only the authorized skill.
- `TestSkillFilteringWithoutHeader` — no `x-a2a-authorized` header; returns all skills (no filtering when header absent, consistent with `FilterTools` behavior for MCP).
- `TestSkillFilteringWithInvalidSignature` — invalid signature on `x-a2a-authorized`; returns all skills (fail-open, consistent with existing MCP behavior).

### E2E Tests

**`tests/e2e/a2a_discovery_test.go`** (Ginkgo + Gomega)
- `[Happy,A2A] A2A agent card is served at well-known endpoint` — GET `/.well-known/agent.json` through gateway returns valid JSON with skills from the test A2A server.
- `[Happy,A2A] Federated card reflects registered A2AAgentRegistrations` — create `A2AAgentRegistration` for test server; verify skill appears in federated card within 2 minutes.
- `[A2A] Agent card updates within TTL after registration deletion` — delete `A2AAgentRegistration`; verify skill disappears from federated card.

**`tests/e2e/a2a_task_execution_test.go`** (Ginkgo + Gomega)
- `[Happy,A2A] Submit task and receive synchronous completion` — POST `message/send` to `/a2a`; verify 200 response with `result.status.state=completed`.
- `[Happy,A2A] tasks/get routes to correct upstream agent` — POST `message/send`; extract gateway task ID; POST `tasks/get` with gateway task ID; verify routed to correct agent.
- `[Happy,A2A] Streaming task updates via message/stream` — POST `message/stream`; read SSE events; verify `working` → `completed` state transitions received.
- `[A2A] Task operation for unknown task ID returns error` — POST `tasks/get` with random task ID; verify JSON-RPC error response.
- `[A2A] A2A request with invalid JWT rejected` — POST `message/send` with invalid `mcp-session-id`; verify 404 response.
- `[A2A,Security] A2A traffic does not bypass gateway auth` — POST `message/send` without `Authorization` header to auth-protected A2A route; verify 401/403 response from Kuadrant AuthPolicy.
- `[Happy] MCP tools/call unaffected by A2A changes` — verify existing MCP test case still passes.
- `[Happy] MCP tools/list unaffected by A2A changes` — verify existing MCP test case still passes.

---

## 7. Dependencies & Environment

### Go Dependencies (from go.mod)

| Dependency | Version | Status | Justification |
|---|---|---|---|
| `github.com/envoyproxy/go-control-plane/envoy` | v1.37.0 | Existing | ext_proc gRPC types: `ExternalProcessor_ProcessServer`, `ProcessingRequest`, `ProcessingResponse`, `HttpHeaders`, `BodyResponse` |
| `google.golang.org/grpc` | v1.80.0 | Existing | gRPC server registration: `grpc.NewServer()`, `extProcV3.RegisterExternalProcessorServer()` |
| `sigs.k8s.io/controller-runtime` | v0.23.1 | Existing | A2A controller: `ctrl.NewControllerManagedBy`, `reconcile.Request`, `client.Client` |
| `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go` | v0.35.2 | Existing | Kubernetes API types and client |
| `sigs.k8s.io/gateway-api` | v1.4.1 | Existing | `gatewayv1.HTTPRoute`, `gatewayv1.Gateway` for CRD controller |
| `istio.io/client-go` | v1.29.0 | Existing | `EnvoyFilter` types for operator |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | Existing | `JWTManager.Validate()` for A2A session JWT validation |
| `github.com/redis/go-redis/v9` | v9.18.0 | Existing | `Cache.extClient` for Redis-backed task store |
| `github.com/google/uuid` | v1.6.0 | Existing | `uuid.New().String()` for gateway task ID generation |
| `golang.org/x/sync` | v0.20.0 | Existing (indirect) | `singleflight.Group` for agent card fetch deduplication |
| `golang.org/x/oauth2` | v0.35.0 | Existing (indirect) | Fetching upstream Agent Cards that require OAuth client credentials |
| `github.com/stretchr/testify` | v1.11.1 | Existing | Unit test assertions |
| `github.com/onsi/ginkgo/v2` | v2.28.1 | Existing | E2E test framework |
| `github.com/onsi/gomega` | v1.39.1 | Existing | E2E test matchers |
| `go.opentelemetry.io/otel` | v1.43.0 | Existing | Span attributes for A2A routing observability |

**No new dependencies required.** All needed libraries are already present. This is a deliberate design choice: A2A integration is built on the same foundation as MCP, avoiding dependency sprawl.

### External Tools (all existing in dev environment)

- `kind` — local Kubernetes cluster (`make local-setup`).
- `istioctl` / Istio `v1.29.*` — Gateway API provider and Envoy proxy.
- `kubectl`, `helm` — cluster management.
- `controller-gen` — `make generate-all` for CRD deepcopy and YAML.
- `golangci-lint` — `make lint`.

### A2A Test Infrastructure

- `tests/servers/a2a-server/` — Go-based test A2A agent server (new, built in Week 2). Serves `/.well-known/agent.json`, handles `message/send`, `message/stream`, `tasks/get`, `tasks/cancel`. Minimal and deterministic for testing; not a production-grade A2A agent.
- `config/test-servers/a2a-server-*.yaml` — Kubernetes manifests for deploying the test server in the Kind cluster.
- `config/samples/a2aagentregistration-test-server.yaml` — sample `A2AAgentRegistration` resource pointing to the test server's HTTPRoute.

### No New Binaries, No Separate Repo

A2A support is a native extension of the existing `cmd/mcp-broker-router/main.go` binary. The `A2AAgentRegistration` controller is added to `cmd/main.go`. No separate service, no new Docker image (until the project reaches production maturity). This minimizes operational complexity and keeps the PoC scope manageable.

---

## 8. Technical Risks & Mitigations

**Risk 1: A2A protocol specification is still evolving**  
The A2A spec reached stable v0.2 in early 2025 but continues to evolve. Features like Signed Agent Cards and multi-tenancy are v1.0 targets, not yet stable.  
*Mitigation:* Build the PoC against the stable v0.2 specification. Design the `AgentCard` struct and `A2ARequest` parsing to be schema-version-agnostic (read `name`, `skills`, `url` from any parseable JSON; ignore unknown fields). Track the [a2aproject GitHub repo](https://github.com/a2aproject/A2A) for spec changes. The design doc will explicitly document which spec version the PoC targets and which features are deferred.

**Risk 2: SSE streaming through Envoy ext_proc is complex**  
The `ModeOverride` pattern (set `ResponseBodyMode: STREAMED` in the response headers phase) is already proven for elicitation in `response_handlers.go:53`. However, A2A streaming involves longer-lived streams than elicitation responses.  
*Mitigation:* Prototype `message/stream` routing in Week 6 using the exact same `ModeOverride` code path. The A2A SSE passthrough (`a2aSSEPassthrough`) is simpler than `sseRewriter` — no ID rewriting, just relay chunks. If `ModeOverride` has unexpected behavior for long-lived SSE streams, fall back to polling-based task status (client polls `tasks/get` instead of streaming) and document the limitation.

**Risk 3: Federated Agent Card merge semantics are ambiguous**  
When two upstream agents both offer a skill named `get_weather`, and both have different authentication requirements, what should the federated card say?  
*Mitigation:* Establish explicit merge rules in the design doc (Week 3): (1) skill IDs are prefixed by the agent's `skillPrefix` to prevent collisions — no two skills in the federated card will have the same ID, (2) authentication scheme in the federated card is the gateway's own scheme (Bearer/OAuth), not the upstream agents' schemes — the gateway is the enforcement point. Validate these rules with mentors before implementation.

**Risk 4: In-memory task store is not HA**  
The task route store (`session.Cache.taskRoutes sync.Map`) is per-pod. In a scaled deployment with multiple broker-router replicas, a `tasks/get` request may arrive at a different pod than the one that handled `message/send`.  
*Mitigation:* Explicitly scope in-memory task store for the PoC. Document the production migration path: `StoreTaskRoute()` / `ResolveTaskRoute()` use the same Redis codepath as `AddSession()` / `GetSession()` when `extClient` is set — Redis support is a one-line config change. The PoC demonstrates correctness; HA is a follow-on task.

**Risk 5: A2A and MCP traffic may interfere in the ext_proc server**  
A bug in A2A protocol detection could cause A2A requests to be routed through MCP handlers (or vice versa), potentially corrupting session state.  
*Mitigation:* Use URL path (`/a2a` vs `/mcp`) as the sole discriminator, not content-type or body inspection. This is a hard boundary: the path is immutable from the moment the request arrives, cannot be spoofed by body content, and is set before the body is processed. Write `TestProtocolDetectionPathPrefix` as a unit test covering the boundary condition exhaustively. Run `make lint` and `-race` continuously to catch type assertion errors.

**Risk 6: Gateway session JWT reuse for A2A may have unintended implications**  
A2A clients reuse the same gateway session JWT (obtained via MCP `initialize`) for A2A requests. This means a client that initializes with MCP also gets A2A access, which may not be the intended policy.  
*Mitigation:* Document this behavior explicitly in the design doc and flag it as an open question for mentors (Section 9, question #5). For the PoC, reusing the same session JWT is correct because: (1) it reuses the existing JWT validation infrastructure without modification, (2) policy enforcement (what the client can do) is handled by Kuadrant AuthPolicy, not by whether the session is MCP-derived. A separate A2A session initialization could be added in a follow-on if mentors decide it's needed.

**Risk 7: Upstream A2A agent returns task IDs that are not URL-safe**  
A2A task IDs are opaque strings. If an upstream agent returns a task ID containing characters that Redis keys disallow or that cause JSON parsing issues, the task store could be corrupted.  
*Mitigation:* The gateway generates its own gateway-scoped task ID using `uuid.New().String()` (hyphenated UUID, always URL-safe). The upstream task ID is stored as a value in the task store, never used as a key. JSON marshaling handles arbitrary string values safely.

---

## 9. Open Questions for Mentors

1. **A2A spec version:** Which spec version should the PoC target? Should we support v0.2 only, or aim for v1.0 compatibility where feasible (e.g., the `/.well-known/agent.json` vs `/.well-known/agent-card.json` endpoint naming differs between versions)?

2. **CRD naming and placement:** Should the new CRD be named `A2AAgentRegistration` in the `mcp.kuadrant.io` group (consistent with existing CRDs), or would the Kuadrant team prefer a new group like `a2a.kuadrant.io` to signal that this is an experimental extension?

3. **A2A endpoint path convention:** The design uses `/a2a` as the path prefix for A2A traffic through the gateway. Is this the convention the kube-agentic-networking SIG prefers, or should we align with a different path (e.g., `/agents`, `/.a2a`)?

4. **Gateway session sharing between MCP and A2A:** The design reuses the MCP `mcp-session-id` JWT for A2A requests. Is this the right approach, or should A2A have a separate session initialization endpoint? The advantage of reuse is no new client-side setup; the disadvantage is that MCP `initialize` implicitly grants A2A access.

5. **A2A skill-level authorization:** Should the PoC include A2A skill-level authorization (filtering the federated Agent Card based on a signed `x-a2a-authorized` header), analogous to the existing MCP tool filtering (`enforceCapabilityFilter`)? Or is this out of PoC scope?

6. **Binary co-location:** The design adds A2A components to the existing `cmd/mcp-broker-router/main.go` binary. Is there a preference for a separate A2A binary, or is co-location with the MCP broker/router the right operational model for the gateway?

7. **EnvoyFilter changes:** The current EnvoyFilter targets port 8080 and applies ext_proc to all traffic. Should A2A traffic use a separate EnvoyFilter patch (e.g., on a distinct listener port), or is the shared filter with path-based protocol detection acceptable?

8. **Upstream A2A agent authentication:** When the broker fetches Agent Cards from upstream agents, should it present credentials from `A2AAgentRegistration.spec.credentialRef`? The MCP broker does not authenticate its Agent Card fetches (it uses `MCPManager`'s credential for MCP `initialize`). Is the same credential used for both the card fetch and the proxied A2A requests?

9. **A2A test server language:** The design proposes a Go-based A2A test server analogous to `tests/servers/server1/`. Would the team prefer a TypeScript server (consistent with the `everything-server`) to validate cross-language A2A compatibility?

10. **Community engagement:** What is the expected cadence of mentor check-ins? Are there specific kube-agentic-networking SIG meetings or A2A community calls the mentee should attend? Is there a Kuadrant Slack workspace invite available?

---

*End of implementation plan.*
