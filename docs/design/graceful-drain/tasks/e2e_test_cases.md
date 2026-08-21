# Graceful Drain — E2E Test Cases

Design: [../graceful-drain-design.md](../graceful-drain-design.md)

Format follows `tests/e2e/test_cases.md`. `Drain` is the feature tag; only the core rollout case is `Happy`, so that tag keeps its meaning.

### [Happy,Drain] Test tool calls survive a rollout restart under load

- When a client issues sustained concurrent tools/call requests against the gateway and the mcp-gateway deployment is restarted with `kubectl rollout restart`, every request should either complete successfully or fail with a retryable JSON-RPC error. No request should fail with a transport-level reset or an ext_proc 5xx. The test runs Serial because it restarts shared infrastructure.

### [Drain] Test readiness fails while draining but liveness stays healthy

- When a broker-router pod receives SIGTERM, its /readyz endpoint should begin returning 503 while /healthz continues to return 200. This distinguishes a pod that is intentionally going away from one the kubelet should restart, and confirms the drain state is observable from outside the process.

### [Drain] Test no new backend sessions are created while draining

- When a pod has entered the draining state and a client attempts to initialize a new stateful session against an upstream it has not used before, the gateway should refuse with a retryable error rather than creating a backend session it is about to abandon. Verified by asserting the upstream test server records no new session for that gateway session after drain begins.

### [Drain] Test existing sessions continue routing while draining

- When a pod is draining and a client issues a tools/call using a backend session mapping that already exists, the request should route and complete normally. Draining refuses new session creation only; it must not break sessions already established, since a replacement pod is expected to keep using them.

### [Drain] Test in-flight tool calls complete during drain

- When a slow tool call is in flight and the serving pod receives SIGTERM, the call should complete and return its result rather than being cut at the socket, provided it finishes within the drain deadline. Uses a test server tool with a controllable delay shorter than the deadline.

### [Drain] Test drain deadline is enforced

- When a tool call outlasts the drain deadline, the pod should stop waiting, proceed to the bounded teardown, and exit within terminationGracePeriodSeconds rather than being killed by the kubelet. Uses a delay longer than the deadline and asserts the pod's exit is clean and the forced-termination metric is incremented.

### [Drain] Test the pod exits within its grace period

- When the deployment is restarted, each terminating pod should exit before terminationGracePeriodSeconds elapses. A pod reaching the grace period would indicate the budget arithmetic has drifted from the pod spec, which is the failure the computed grace period exists to prevent.

### [Drain,Security] Test draining does not weaken request rejection

- When a pod is draining and a request arrives that would normally be rejected, it should still be rejected. Draining changes when traffic stops arriving, never what happens to traffic that does arrive; ext_proc failure_mode_allow remains false throughout.

### [Drain,Security] Test the drain response cannot be induced by a client header

- When a client sends headers attempting to signal drain state, the gateway should ignore them entirely. The drain response is derived from process state only, consistent with the rule that x-mcp-* routing metadata is router-set and never client-settable.

### [Drain] Test steady-state behaviour is unaffected

- When no pod is terminating, session creation, tool invocation, and readiness reporting should behave exactly as before this feature. Regression safety for the normal path, since the drain checks sit on the request path.
