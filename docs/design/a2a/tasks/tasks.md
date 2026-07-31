# A2A Protocol Support — Implementation Plan

This plan follows the design's [Implementation Phases](../a2a-design.md#implementation-phases):
phase 1 is the router-only passthrough cut (tracked in #1333) and lands first; phases 2 and 3 are
the direction as currently understood, gated on where A2A support lands long-term (the CRD
question) and on the broker settling after the MCP spec-version work. The phase 2 and 3 designs
have been exercised end-to-end in a working prototype, so their tasks start from validated ground.

## Existing Code Analysis

The following primitives exist in the codebase and are reused directly by the A2A implementation:

| Primitive | Location | Reused for |
|---|---|---|
| ext_proc Process() loop | `internal/mcp-router/ext_proc_adapter.go` | A2A traffic detection and passthrough metadata |
| ResponseBuilder | `internal/mcp-router/response_builder.go` | Building all ext_proc responses |
| HeadersBuilder | `internal/mcp-router/headers.go` | Setting routing headers |
| sseRewriter | `internal/mcp-router/elicitation.go` | Template for a2aSSEObserver (read-only line reader) |
| idmap.Map | `internal/idmap/map.go` | Template for the task-record store (same in-memory/Redis duality) |
| session.Cache | `internal/session/cache.go` | Extended with task-record methods |
| ExtractSubClaim | `internal/jwt/decode.go` | Principal extraction for A2A ownership (per-request OAuth) |
| idmap Redis TTL pattern | `internal/idmap/redis.go` | Fixed safety-net TTL + explicit cleanup; the A2A task-store TTL is decoupled from JWT/session expiry, not derived from it |
| config.Observer | `internal/config/types.go` | A2A broker registers as observer |
| MCPServersConfig.Notify() | `internal/config/types.go` | Triggers A2A broker config updates |
| SecretReaderWriter | `internal/config/config_writer.go` | Extended with UpsertA2AAgent/RemoveA2AAgent |
| MCPReconciler | `internal/controller/mcpserverregistration_controller.go` | Template for A2AReconciler |
| HTTPRouteWrapper | `internal/controller/httproute_wrapper.go` | Used directly in A2AReconciler |
| buildGatewayHTTPRoute() | `internal/controller/broker_router.go` | Modified to add /a2a prefix and /.well-known/api-catalog rules (phase 2) |
| ModeOverride | `internal/routing` response handling + the ext_proc adapter | A2A response observation (phase 3) |

---

## Phase 1 — A2A passthrough with auditing, auth and observability (#1333)

Router-only, behind an experimental `--enable-a2a` flag, default off. No CRD, no broker or
controller changes; users hand-author the HTTPRoutes from the gateway to their agents.

### Task 1: Design Document + Gap Analysis

**Files:**
- `docs/design/a2a/a2a-design.md` (this document's companion)
- `docs/design/a2a/tasks/tasks.md` (this file)
- `docs/design/a2a/tasks/e2e_test_cases.md`

**Acceptance criteria:**
- [ ] Design doc covers all sections per `docs/design/CLAUDE.md` structure
- [ ] Mermaid diagrams for agent card discovery, SendMessage routing, task lifecycle
- [ ] Open design questions surfaced as explicit tradeoff analyses
- [ ] Implementation phases reflect the agreed first cut (#1333)
- [ ] `make spell` passes
- [ ] Maintainers approve design before implementation begins

**Verification:**
```bash
make spell
```

---

### Task 2: A2A Test Server

**Files:**
- `tests/servers/a2a-server/main.go`
- `tests/servers/a2a-server/Dockerfile`
- `config/test-servers/a2a-server-deployment.yaml`
- `config/test-servers/a2a-server-service.yaml`
- `config/test-servers/a2a-server-httproute.yaml`
- `config/test-servers/kustomization.yaml` (updated)

**Acceptance criteria:**
- [ ] `GET /.well-known/agent-card.json` (v1.0 well-known path) returns a valid v1.0 AgentCard (`supportedInterfaces` carrying the server's own address, named `securitySchemes`) configurable via `AGENT_NAME`, `SKILLS`, `AGENT_PREFIX` env vars; optionally JWS-signed to exercise the verbatim-serving path
- [ ] `POST /a2a` dispatches `SendMessage` (blocking by default in v1.0), `GetTask`, `CancelTask`, `SubscribeToTask`
- [ ] SSE streaming via `SendStreamingMessage` (the v1.0 streaming method, a distinct JSON-RPC method — NOT `SendMessage` + `Accept`): three `working` events then `completed`, task IDs at the envelope identity field (the task `id`, then `taskId` on updates)
- [ ] Received request headers are echoed in a response artifact so e2e can assert the router-set `x-a2a-*` headers reached the agent
- [ ] Kubernetes manifests follow `config/test-servers/server1-deployment.yaml` pattern
- [ ] Server added to `config/test-servers/kustomization.yaml`

**Verification:**
```bash
curl http://a2a-test-server.mcp-test.svc.cluster.local:9090/.well-known/agent-card.json
curl -X POST http://a2a-test-server.mcp-test.svc.cluster.local:9090/a2a \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"role":"user","parts":[{"text":"hello"}]}}}'
```

---

### Task 3: Router — `--enable-a2a` Flag + Passthrough Metadata

*Depends on: Task 1*

**Files:**
- `cmd/mcp-broker-router/main.go` (flag, following `--enable-url-elicitation`)
- `internal/mcp-router/ext_proc_adapter.go`
- `internal/mcp-router/a2a.go` (new)
- `internal/headers/headers.go` (A2A header constants, per the design's New router headers)

**Acceptance criteria:**
- [ ] `--enable-a2a` boolean flag, default `false`; with the flag off, no A2A code path runs and all traffic behaves exactly as before
- [ ] Segment-aware `/a2a` path match at `RequestHeaders` (`/a2a/` prefix or exact `/a2a` — a bare `HasPrefix("/a2a")` would also match `/a2ax`)
- [ ] Inbound `x-a2a-*` request headers stripped at `RequestHeaders`, before any policy evaluates (same router-owned-header pattern as `x-mcp-*`)
- [ ] POST bodies parsed envelope-only (JSON-RPC `method` and `id`; params and heavy fields never decoded); `x-a2a-method` and `x-a2a-agent` set at `RequestBody`
- [ ] `x-a2a-agent` derived as the first path segment after `/a2a/` (the documented convention; no namespace qualification in this mode)
- [ ] `x-a2a-method` value for unknown methods per the resolution on #1333 (bounded label values for Telemetry)
- [ ] Unparseable POST on an `/a2a` route fails closed with an `application/json` JSON-RPC `-32700` error; GETs pass through untouched
- [ ] MCP paths (`/mcp` traffic) completely unaffected — regression tests pass with the flag on and off
- [ ] Unit tests with mock ext_proc streams cover all branches above

**Verification:**
```bash
make test-unit
make lint
```

---

### Task 4: Passthrough Documentation

*Depends on: Task 3*

**Files:**
- `docs/guides/a2a-passthrough.md` (new)
- `docs/guides/README.md` (updated)

**Acceptance criteria:**
- [ ] Documents the path convention (the segment after `/a2a/` is the agent identity) and its limits
- [ ] Enabling via the deployment arg (the operator's command-merge preserves user-added flags across reconciles); agent HTTPRoutes must attach to the listener the gateway extension targets
- [ ] AuthPolicy example keyed on `x-a2a-agent`/`x-a2a-method`, and an Istio `Telemetry` example tagging metrics/logs with them
- [ ] Trust-boundary notes: task isolation is the upstream agent's responsibility (the gateway forwards the client's bearer; `a2a-go`'s task store exposes an `Authenticator` hook), and agents behind the gateway should advertise their gateway URL in their card rather than being independently reachable
- [ ] Guide follows `docs/CLAUDE.md` conventions; `make spell` passes

**Verification:**
```bash
make spell
```

---

### Task 5: Passthrough E2E Tests

*Depends on: Tasks 2, 3*

**Files:**
- `tests/e2e/a2a_passthrough_test.go` (new)
- `tests/e2e/test_cases.md` (updated)

**Acceptance criteria:**
- [ ] Hand-authored HTTPRoute wires the A2A test server to the gateway (no CRD)
- [ ] With `--enable-a2a`: a `SendMessage` POST reaches the agent carrying router-set `x-a2a-method` and `x-a2a-agent` (asserted via the test server's header echo), and a client-supplied `x-a2a-*` header is stripped, never reaching the agent or policy
- [ ] Unparseable POST returns the JSON-RPC `-32700` error; GET card fetch passes through
- [ ] With the flag off: `/a2a` traffic passes through with no A2A headers and no rejections
- [ ] MCP regression: `tools/list` and `tools/call` unaffected in both flag states

**Verification:**
```bash
ginkgo -v --label-filter="A2A" ./tests/e2e/...
```

---

## Phase 2 — Registration and Discovery (gated)

Gated on the long-term home for A2A support settling (the CRD question) and on the broker
stabilizing after the MCP spec-version work. Where phase 1 already ships a piece (path detection,
header constants, stripping), the tasks below narrow to the remainder.

### Task 6: A2AAgentRegistration CRD Finalization

**Files:**
- `api/v1alpha1/a2aagentregistration_types.go`
- `api/v1alpha1/zz_generated.deepcopy.go` (regenerated)
- `config/crd/mcp.kuadrant.io_a2aagentregistrations.yaml` (regenerated)
- `charts/mcp-gateway/crds/mcp.kuadrant.io_a2aagentregistrations.yaml` (regenerated)
- `docs/reference/a2aagentregistration.md`

**Acceptance criteria:**
- [ ] `agentPrefix` immutability CEL rule passes `make lint`
- [ ] `agentCardURL` URL format validation present
- [ ] `targetRef` uses `omitzero` (kubeapilinter requirement)
- [ ] `make generate-all` produces no diff after this PR
- [ ] `kubectl apply -f config/crd/...a2aagentregistrations.yaml` succeeds against Kind cluster
- [ ] kubeapilinter passes in CI

**Verification:**
```bash
make generate-all
git diff --exit-code
kubectl apply -f config/crd/mcp.kuadrant.io_a2aagentregistrations.yaml
make lint
```

---

### Task 7: A2AReconciler Scaffold

*Depends on: Task 6*

**Files:**
- `internal/controller/a2aagentregistration_controller.go` (new — scaffold only)
- `cmd/main.go` (register A2AReconciler)

**Acceptance criteria:**
- [ ] `A2AReconciler` struct has `Client`, `Scheme`, `DirectAPIReader`, `ConfigReaderWriter`, `MCPExtFinderValidator` fields
- [ ] `Reconcile()` returns `ctrl.Result{}` — skeleton only
- [ ] `SetupWithManager()` watches `A2AAgentRegistration`, `HTTPRoute`, `Secret` with same predicates as `MCPReconciler`
- [ ] Uses distinct finalizer `"mcp.kuadrant.io/a2a-finalizer"`
- [ ] `make build` passes
- [ ] Controller starts without errors against Kind cluster

**Verification:**
```bash
make build
make deploy
kubectl logs -n mcp-system deployment/mcp-gateway-controller
```

---

### Task 8: A2AReconciler Reconcile Logic + Tests

*Depends on: Task 7*

**Files:**
- `internal/controller/a2aagentregistration_controller.go` (fill in reconcile logic)
- `internal/controller/a2aagentregistration_controller_test.go` (new)
- `internal/controller/a2aagentregistration_controller_integration_test.go` (new)

**Acceptance criteria:**
- [ ] Finalizer added on create, removed only after config is cleaned up
- [ ] `getTargetHTTPRoute()` resolves HTTPRoute using `WrapHTTPRoute()` + `Validate()`, honoring `targetRef.namespace` (defaulting to the registration's namespace) — and the HTTPRoute field index resolves the namespace identically, so cross-namespace watches fire
- [ ] Cross-namespace `targetRef` requires a `ReferenceGrant` in the route's namespace (`from`: `A2AAgentRegistration`, `to`: `HTTPRoute`), mirroring `MCPGatewayExtension`'s grant check; no grant → `Ready=False` and the agent's config is withdrawn (revoking consent revokes the exposure, not just the status); `ReferenceGrant` changes trigger re-reconcile via a `ReferenceGrant` watch
- [ ] `buildA2AAgentConfig()` handles `IsHostnameBackend()` and `IsServiceBackend()` using existing helpers
- [ ] `UpsertA2AAgent()` called for each valid MCPGatewayExtension namespace
- [ ] Status conditions: `Ready=True` (reason `Ready`) when config is written, mirroring `MCPServerRegistration` — `Ready` is not a promise the agent is reachable or serving, and no discovered card content (skills, card fields) appears in status
- [ ] `state: Disabled`: config is still written carrying `state: Disabled` (the broker acts on the flag), and status is `Ready=False, Reason=Disabled`, mirroring `MCPServerRegistration`; re-enabling restores `Ready=True`
- [ ] Controller integration tests: new registration → Ready=True; missing HTTPRoute → Ready=False; deletion removes config; cross-namespace `targetRef` with a `ReferenceGrant` resolves and reconciles; cross-namespace without a grant → Ready=False, no config written; revoking the grant withdraws previously written config

**Verification:**
```bash
make test-controller-integration
```

---

### Task 9: Config Plumbing + Hot-Reload

*Depends on: Task 6 (the reconcile logic in Task 8 writes through `UpsertA2AAgent`, so this can land alongside it)*

**Files:**
- `internal/config/types.go`
- `internal/config/a2a_types.go` (new)
- `internal/config/config_writer.go`

**Acceptance criteria:**
- [ ] `A2AAgents []*A2AAgent` added to `MCPServersConfig` with RWMutex protection
- [ ] `SetA2AAgents()`, `ListA2AAgents()` follow `SetServers()`/`ListServers()` pattern exactly
- [ ] `UpsertA2AAgent()` and `RemoveA2AAgent()` in `SecretReaderWriter` with retry-on-conflict
- [ ] `BrokerConfig` YAML schema gains `a2aAgents` key
- [ ] `Notify()` passes A2A agents to observers alongside MCP servers
- [ ] `go test -race ./internal/config/...` passes

**Verification:**
```bash
go test -race ./internal/config/...
make test-unit
```

---

### Task 10: A2A Broker — Observer Wiring

*Depends on: Task 9*

**Files:**
- `internal/a2a/broker.go` (finalize, wire Observer)
- `internal/a2a/broker_test.go` (extend)

**Acceptance criteria:**
- [ ] `a2a.Broker` implements `config.Observer`: `OnConfigChange()` calls `SetAgents(cfg.ListA2AAgents())`
- [ ] `ServeAPICatalog()` has OTel span `"a2a.ServeAPICatalog"` with `agent.count` attribute, following `HandleToolCall()` pattern
- [ ] `A2AAgentManager` caches the upstream card with a ticker refresh (mirroring `MCPManager`), serving stale-on-error; `ServeAgentCard()` serves the cached card **verbatim** — a signed card's JWS signature must survive byte-for-byte, so the card is not rewritten; the catalog is what advertises the gateway path — not a per-request upstream proxy
- [ ] Card refresh is poll-only (A2A has no card-change push): ticker re-fetch with conditional GET (`If-None-Match`/`If-Modified-Since`) + `version`/SHA-256 change detection; act only on change (in-memory cache swap under RWMutex, no Secret write); staleness bound = ticker interval (reuse `managerTickerInterval`, default 1 min)
- [ ] No discovered card content is surfaced in registration status (`Ready` = config written); the broker's ticker refresh is the only live card sync
- [ ] `credentialRef` is used by the broker ONLY for the card fetch (discovery), never injected into client `SendMessage`/`tasks/*` (router has no `credentialRef` access; invocation auth = forwarded client bearer or RFC 8693 token exchange via AuthPolicy)
- [ ] Unit tests: `OnConfigChange` triggers `SetAgents`; `ServeAgentCard` with unreachable upstream skips gracefully; `ServeAgentCard` serves the signed card verbatim (no url rewrite); `GetAgentByPath` lookup
- [ ] `go test -race ./internal/a2a/...` passes

**Verification:**
```bash
go test -race ./internal/a2a/...
make test-unit
```

---

### Task 11: A2A Broker — Binary Wiring

*Depends on: Task 10*

**Files:**
- `cmd/mcp-broker-router/main.go`
- `cmd/mcp-broker-router/broker.go`
- `cmd/mcp-broker-router/router.go`

**Acceptance criteria:**
- [ ] `a2aBroker` initialized in `main.go` and registered as observer: `cfg.RegisterObserver(a2aBroker)`
- [ ] `/.well-known/api-catalog` (Content-Type `application/linkset+json`) and `/a2a/{namespace}/{prefix}/.well-known/agent-card.json` registered in `setUpHTTPServer()` after `/.well-known/oauth-protected-resource`
- [ ] `A2ABroker` field added to `ExtProcServer` in `createRouter()`
- [ ] `buildGatewayHTTPRoute()` gains the `/a2a` prefix rule (with the `x-a2a-*` strip filter) and the `/.well-known/api-catalog` rule, both behind the A2A gate
- [ ] `make build` passes

**Verification:**
```bash
make build
make deploy
curl http://mcp.127-0-0-1.sslip.io:8001/.well-known/api-catalog
# expect: an empty linkset (no agents registered yet)
```

---

### Task 12: Router — Agent Resolution + Request Routing

*Depends on: Task 11 (phase 1 already ships path detection, header constants, stripping, and the envelope parse; this task adds registry-backed routing)*

**Files:**
- `internal/mcp-router/ext_proc_adapter.go`
- `internal/mcp-router/a2a.go`

**Acceptance criteria:**
- [ ] At `RequestHeaders` phase: extract (namespace, prefix) from `:path`, call `A2ABroker.GetAgentByPath()`, set `:authority` to the agent hostname; `x-a2a-agent` becomes the namespace-qualified agent identity (`{namespace}/{prefix}`), superseding the phase-1 bare-segment convention. Method-specific work stays at `RequestBody` — the JSON-RPC method is known only there
- [ ] Unknown `/a2a/{namespace}/{prefix}` → `application/json` JSON-RPC `-32602`, nothing forwarded
- [ ] Deferred v1.0 methods (`ListTasks`, extended card, `pushNotificationConfig` operations) → `-32004 UnsupportedOperationError`, never forwarded (gateway-scoped ownership arrives in phase 3; the gate lands with the routing that makes it enforceable)
- [ ] A2A spans carry `a2a.method`, `a2a.agent` attributes; MCP path (`/mcp` traffic) completely unaffected — regression tests pass
- [ ] Unit tests cover all branches above

**Verification:**
```bash
make test-unit
# deploy and test end-to-end:
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/a2a/mcp-test/weather \
  -H "Authorization: Bearer <oauth-token>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{...}}}'
```

---

## Phase 3 — Task Ownership and Lifecycle Observability (gated)

Builds on phase 1's headers and phase 2's registration.

### Task 13: Task Ownership Records

*Depends on: Task 12*

**Files:**
- `internal/session/cache.go`
- `internal/session/cache_test.go`
- `internal/routing/session.go` (SessionCache interface)

**Acceptance criteria:**
- [ ] Ownership records stored in the existing `inmemory` `sync.Map` under a `taskowner:` key prefix (immutable principal-string values, no COW; every access is keyed, so no type-assertion hazard)
- [ ] `StoreTaskRecord(ctx, agent, id, principal string, ttl time.Duration) (owner string, created bool, err error)` — insert-only via `LoadOrStore` (in-memory) / `SET NX` (Redis), returning the current owner and whether this call created the record; callers resolve new-insert / same-owner / different-owner / store-unavailable, the latter two failing closed; the response or first task-creating event is withheld until binding succeeds
- [ ] `LookupTaskRecord(ctx, agent, id string) (principal string, found bool, err error)` and `DeleteTaskRecord(ctx, agent, id string) error` for both backends
- [ ] `SessionCache` interface in `internal/routing/session.go` updated with the above signatures
- [ ] Redis key `taskowner:{agent}:{id}`, **fixed retention TTL decoupled from the JWT** (idmap pattern), sized ≥ the agents' task-retention window; records are NOT deleted on terminal states (tasks stay retrievable after completion)
- [ ] Parallel insert-only `(agent, contextId) -> principal` record, bound from the first task/message response or stream event; both send methods verify context ownership when a request carries a `contextId`
- [ ] Principal set from the OAuth `sub`; lookup callers verify the requesting principal owns the task (routing is by path, so the record is used for ownership only); the record is extensible to carry the creating span's trace context for span links
- [ ] `SendMessage` response observation (BUFFERED ModeOverride — the filter's default `response_body_mode` is `NONE`): read `result.task.id` from the v1.0 `SendMessageResponse` oneof (the `result.message` variant creates no task and stores nothing), call `StoreTaskRecord()`, forward the body unchanged
- [ ] `GetTask`/`CancelTask`/`SubscribeToTask` — and sends naming an existing task via `message.taskId`/`referenceTaskIds` — call `LookupTaskRecord()` and verify principal ownership; a missing/expired/mismatched record fails closed with `-32001` (no ID rewrite anywhere)
- [ ] Concurrency test: 100 goroutines reading and writing task records with `-race`
- [ ] `go test -race ./internal/session/...` passes

**Verification:**
```bash
go test -race ./internal/session/...
make test-unit
```

---

### Task 14: SSE Streaming Observation

*Depends on: Task 13*

**Files:**
- `internal/mcp-router/a2a_sse.go` (a2aSSEObserver)
- `internal/mcp-router/ext_proc_adapter.go`

**Acceptance criteria:**
- [ ] `a2aSSEObserver` reads each `data:` line **unchanged** (read-only tap, line-buffered like `sseRewriter`)
- [ ] Reads the streaming event identity field — `result.task.id` on the initial `task` event, `result.statusUpdate.taskId`/`result.artifactUpdate.taskId` on updates (v1.0 has no `kind` discriminator; the variant is which oneof member is present) — and uses it only to `StoreTaskRecord()` (insert-only) on the first `task` event; terminal states end the stream, the record persists to the retention TTL
- [ ] Envelope-only parsing: read the JSON-RPC envelope + result identity fields only; never decode `status`/`artifact`/`history`/`parts` (incl. `FilePart.file.bytes`/`DataPart.data`); no re-marshal, so cost is O(envelope) and Part content is untouched by construction
- [ ] `ResponseHeaders` sets `ModeOverride ResponseBodyMode=STREAMED` for `SendStreamingMessage`/`SubscribeToTask` and `BUFFERED` for `SendMessage`; `GetTask`/`CancelTask` set no override (bare-`Task` responses, nothing to observe); no `content-length` surgery, since nothing is mutated
- [ ] Unit tests: SSE chunks pass through byte-for-byte; the first-event ownership record is stored (insert-only) and survives the terminal state; non-SSE responses unaffected

**Verification:**
```bash
make test-unit
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/a2a/mcp-test/weather \
  -H "Authorization: Bearer <oauth-token>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendStreamingMessage","params":{...}}'
# expect: SSE stream forwarded unchanged (agent-assigned task IDs intact in all events)
```

---

### Task 15: E2E Tests — Discovery + Task Routing

*Depends on: Task 14*

**Files:**
- `tests/e2e/a2a_discovery_test.go`
- `tests/e2e/a2a_task_test.go`
- `tests/e2e/test_cases.md` (updated)

**Acceptance criteria:**
- [ ] Agent card discovery: `GET /.well-known/api-catalog` returns an RFC 9727 catalog (RFC 9264 Linkset) with agent links; `GET /a2a/mcp-test/weather/.well-known/agent-card.json` returns the test server's agent card served verbatim (a signed card's JWS signature intact), with the catalog link — not a rewritten card URL — routing the client to the gateway path
- [ ] Task send: `SendMessage` to `/a2a/{namespace}/{prefix}` routes to correct upstream, returns the agent-assigned task ID unchanged
- [ ] Task get: `GetTask` with that same task ID routes by path and returns the upstream result
- [ ] Task cancel: `CancelTask` propagates to upstream, returns canceled state
- [ ] Agent deregistration: deleting `A2AAgentRegistration` removes the agent from the API catalog within one reconcile cycle

**Verification:**
```bash
ginkgo -v --label-filter="A2A" ./tests/e2e/...
```

---

### Task 16: E2E Tests — Streaming + Auth + Error + Regression

*Depends on: Task 15*

**Files:**
- `tests/e2e/a2a_discovery_test.go` (extend)
- `tests/e2e/a2a_task_test.go` (extend)

**Acceptance criteria:**
- [ ] Streaming: `SendStreamingMessage` delivers SSE chunks with the agent-assigned task IDs passed through unchanged (task `id`, then `taskId` on updates); a per-principal ownership record is created on the first event
- [ ] Auth: request without a valid OAuth bearer returns 401 (AuthPolicy) before reaching upstream
- [ ] Unknown path: `SendMessage` to unregistered `/a2a/{namespace}/{prefix}` returns JSON-RPC `-32602`
- [ ] Ownership: `GetTask` for another principal's task fails closed with `-32001`, indistinguishable from an unknown ID
- [ ] MCP regression: `tools/list` and `tools/call` work correctly after all A2A changes
- [ ] All E2E tests pass: `ginkgo -v ./tests/e2e/... -- --gateway-host=mcp.127-0-0-1.sslip.io:8001`

**Verification:**
```bash
ginkgo -v ./tests/e2e/... -- --gateway-host=mcp.127-0-0-1.sslip.io:8001
```

---

### Task 17: Documentation + Final Polish

**Files:**
- `docs/guides/a2a-agent.md`
- `docs/guides/README.md` (updated)

**Acceptance criteria:**
- [ ] Guide follows `docs/CLAUDE.md` conventions: goal-oriented, numbered steps, verification commands, no internal references
- [ ] Covers: prerequisites, Step 1 (HTTPRoute), Step 2 (A2AAgentRegistration), Step 3 (verify agent card), Step 4 (send a task), credentialRef usage
- [ ] Links to authentication guide for AuthPolicy on the `/a2a` path
- [ ] `make spell` passes
- [ ] Guide reviewed and approved by maintainers

**Verification:**
```bash
make spell
```
