# Graceful Drain — Implementation Plan

Design: [../graceful-drain-design.md](../graceful-drain-design.md)
Issue: #1363

> Jira story references are placeholders pending stories under CONNLINK.

## Existing Code Analysis

What this builds on, all already merged:

- `cmd/mcp-broker-router/main.go` — SIGTERM registered (#1362). Shutdown sequence bounded (#1390): `serverDrainTimeout` 8s, `brokerDrainTimeout` 5s, `grpcDrainTimeout` 10s, `telemetryFlushTimeout` 4s, telemetry flushed last.
- `cmd/mcp-broker-router/broker.go:53-63` — `/healthz` returns a static 200; `/readyz` gates on `mcpBroker.IsReady()`.
- `internal/broker/broker.go` — `IsReady()` returns true when no servers are configured, or when any manager reports ready.
- `internal/controller/broker_router.go` — generates the Deployment. `replicas := int32(1)` at `:93`; readiness probe at `:235-248` with `PeriodSeconds: 10`, `FailureThreshold: 3`; Service exposes `http` and `grpc` off the same pod at `:285-300`. No `preStop`, no `terminationGracePeriodSeconds`.
- `internal/mcp-router/` — ext_proc server. No in-flight stream accounting today.
- `internal/routing/router_202511.go` — `initializeMCPServerSession` creates backend sessions lazily, guarded by a singleflight group.

What does not exist and must be added: any notion of lifecycle state, any accounting of in-flight work, and any pod-spec wiring for termination.

---

### Task 1: Lifecycle state (CONNLINK-TBD)

**Files:**

- `cmd/mcp-broker-router/lifecycle.go` (new)
- `cmd/mcp-broker-router/lifecycle_test.go` (new)
- `cmd/mcp-broker-router/main.go`

**Acceptance criteria:**

- [ ] A `State` type with `serving`, `draining`, `terminating`, stored as an atomic value on `app`.
- [ ] Transitions are one-way and idempotent; a second SIGTERM does not reset state or restart the drain.
- [ ] Reads are allocation-free and safe from request-path goroutines.
- [ ] Unit tests cover each transition and concurrent read/write under `-race`.

**Verification:**

```bash
make lint
go test ./cmd/... -race -count=1
```

---

### Task 2: Readiness reflects drain (CONNLINK-TBD)

**Files:**

- `cmd/mcp-broker-router/broker.go`
- `cmd/mcp-broker-router/broker_test.go` (new)

**Acceptance criteria:**

- [ ] `/readyz` returns 503 in `draining` and `terminating`, regardless of `IsReady()`.
- [ ] `/readyz` behaviour in `serving` is unchanged.
- [ ] `/healthz` returns 200 in all three states; it reflects only whether the process can operate.
- [ ] Neither endpoint exposes internal state in its body.

**Verification:**

```bash
make lint
go test ./cmd/... -race -count=1
```

---

### Task 3: In-flight work accounting (CONNLINK-TBD)

**Files:**

- `internal/mcp-router/ext_proc_adapter.go`
- `internal/mcp-router/ext_proc_adapter_test.go`
- `cmd/mcp-broker-router/lifecycle.go`

**Acceptance criteria:**

- [ ] ext_proc streams are counted on open and released on close, including on error paths.
- [ ] New ext_proc streams are refused once draining.
- [ ] A `Wait(ctx)` returns when the count reaches zero or the context expires, whichever first.
- [ ] Counting adds no allocation on the per-request path, consistent with `docs/design/performance.md`.
- [ ] Unit tests with mock ext_proc streams cover open, close, error-path release, and refusal while draining.

**Verification:**

```bash
make lint
go test ./internal/mcp-router/... -race -count=1
go test ./internal/routing/... -bench=. -benchmem -run='^$'
```

---

### Task 4: Refuse new stateful sessions while draining (CONNLINK-TBD)

**Files:**

- `internal/routing/router_202511.go`
- `internal/routing/router_test.go`

**Acceptance criteria:**

- [ ] `initializeMCPServerSession` refuses to create a new backend session while draining and returns a retryable error.
- [ ] Requests using an existing backend session mapping continue to route normally.
- [ ] No backend session is created and then abandoned; the refusal happens before initialization, not after.
- [ ] The drain response is derived from process state only, never from a client-supplied header.
- [ ] Unit tests cover refusal, existing-session passthrough, and the singleflight interaction.

**Verification:**

```bash
make lint
go test ./internal/routing/... -race -count=1
```

---

### Task 5: Drain sequence in `run()` (CONNLINK-TBD)

**Files:**

- `cmd/mcp-broker-router/main.go`

**Acceptance criteria:**

- [ ] SIGTERM moves the process to `draining` before any server shutdown begins.
- [ ] The drain waits for in-flight work up to `drainDeadline`, then moves to `terminating`.
- [ ] The existing bounded teardown from #1390 runs unchanged after the drain.
- [ ] Budgets are named constants in one place, and their sum is asserted against the grace period in a test.
- [ ] Reaching the deadline is logged at warn with the counts still outstanding.

**Verification:**

```bash
make lint
go test ./cmd/... -race -count=1
```

---

### Task 6: Pod lifecycle wiring (CONNLINK-TBD)

**Files:**

- `internal/controller/broker_router.go`
- `internal/controller/broker_router_test.go`
- `config/mcp-system/deployment-broker.yaml`
- `charts/mcp-gateway/templates/`

**Acceptance criteria:**

- [ ] The generated Deployment sets a `preStop` hook sleeping `drainPropagationDelay`.
- [ ] `terminationGracePeriodSeconds` is computed from the shared budget constants plus a safety margin, not hardcoded independently.
- [ ] The static manifest and the Helm chart match the controller output.
- [ ] A controller test asserts the grace period is greater than the sum of all budgets.
- [ ] `make generate-all` produces no diff.

**Verification:**

```bash
make lint
make generate-all && git diff --exit-code
make test-controller-integration
```

---

### Task 7: Drain metrics (CONNLINK-TBD)

**Files:**

- `internal/otel/metrics.go`
- `cmd/mcp-broker-router/lifecycle.go`

**Acceptance criteria:**

- [ ] Drain duration, requests completed during drain, and forced terminations are exported.
- [ ] Naming follows the existing `mcp_broker_*` conventions.
- [ ] Metrics emitted during drain are actually exported, which #1390's ordering change makes possible.
- [ ] Unit tests assert the instruments are registered and recorded.

**Verification:**

```bash
make lint
go test ./internal/otel/... -race -count=1
```

---

### Task 8: Rollout-under-load e2e (CONNLINK-TBD)

**Files:**

- `tests/e2e/graceful_drain_test.go` (new)
- `tests/e2e/test_cases.md`

**Acceptance criteria:**

- [ ] Sustained concurrent `tools/call` load across a `kubectl rollout restart` completes with no transport-level resets.
- [ ] Any failures observed are retryable protocol errors, not connection resets.
- [ ] The pod exits within `terminationGracePeriodSeconds` without being killed.
- [ ] Marked `Serial`, per `tests/e2e/CLAUDE.md`, since it restarts shared infrastructure.
- [ ] Cases added to `tests/e2e/test_cases.md` with the tags in `e2e_test_cases.md`.

**Verification:**

```bash
make test-e2e
```

---

### Task 9: Documentation (CONNLINK-TBD)

**Files:**

- `docs/guides/` — per [documentation.md](documentation.md)
- `docs/release-notes/`

**Acceptance criteria:**

- [ ] An operator-facing section covering what drain does, what it does not promise, and how to read the metrics.
- [ ] The `terminationGracePeriodSeconds` change documented, since it alters observable pod behaviour.
- [ ] Release notes entry per `.claude/rules/breaking-changes.md`.

**Verification:**

```bash
make lint
```

---

## Ordering

Tasks 1 and 2 are independent and can land together. Task 3 gates Task 5, since the drain has nothing to wait on without it. Task 4 is independent of 3 and can land in parallel. Task 6 depends on the budget constants settled in Task 5. Tasks 7 and 8 come last: metrics need the states to exist, and the e2e needs the whole sequence wired.

Task 9 can be drafted alongside Task 6 once the observable pod behaviour is fixed.
