# Broker 2026-07-28 Documentation Plan

Documentation for broker 2026-07-28 protocol support, organized by user goals.

## No New User-Facing Guide Required

The broker's 2026 protocol support is transparent to gateway operators. There are no new CRD fields, CLI flags, or configuration options. The broker automatically detects upstream protocol versions and applies the correct behavior:

- `ttlMs` and `cacheScope` are derived from upstream responses, not configured
- `cacheScope:"private"` from 2026 upstreams triggers per-user fetching automatically
- `subscriptions/listen` replaces GET SSE for 2026 upstreams without operator action
- `userSpecificList` continues to work for 2025 upstreams unchanged

The existing `docs/guides/scaling.md` and `docs/guides/authentication.md` guides remain accurate.

## Security Architecture Update (`docs/design/security-architecture.md`)

### When I need to understand how cache scope protects per-user tool lists

When a security reviewer or contributor needs to assess the trust model for `cacheScope` aggregation, they want to understand how the broker prevents tool list cross-contamination.

**Cover:**
- Cache scope aggregation: pessimistic `"private"` when any upstream is private
- Why wrong `"public"` on a response with per-user tools is a tool list leak
- `ttlMs` manipulation: malicious upstream returning large ttlMs bounded by `min()` across upstreams
- `subscriptions/listen` uses the same `credentialRef` auth as `ListTools`

### When I need to understand how ttlMs affects tool freshness

When a contributor is working on routing table refresh or client-side caching, they want to understand what the aggregated `ttlMs` means.

**Cover:**
- Aggregated `ttlMs` reflects worst-case staleness of the cached portion
- `ttlMs:0` upstreams are always-fetched — they don't contribute to the aggregate
- The aggregate is `min(non-zero ttlMs)` — bounds freshness to the most volatile upstream

## Design Doc Update (`docs/design/overview.md`)

### When I need to understand the broker's protocol handling architecture

When a contributor is working on the broker, they want to understand the `ProtocolHandler` interface and how version-specific behavior is isolated.

**Cover:**
- `ProtocolHandler` interface and its two implementations
- How to add 2026-specific broker behavior without touching shared code
- What deleting `ProtocolHandler2025` removes when 2025 is dropped

## API Reference — No Changes

`userSpecificList` stays for 2025 backward compat. No new CRD fields. No updates to `docs/reference/` required.
