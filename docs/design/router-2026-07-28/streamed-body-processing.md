# Streamed Body Processing for `2026-07-28`

## Problem

MCP clients using the 2025 protocol, require the router use `BUFFERED` request body mode so it can parse the JSON-RPC body to extract the method and tool name for routing decisions. It cannot use streamed modes as this causes inconsistent routing. 

Envoy's ext_proc proto defines this constraint on the [`CommonResponse.header_mutation`](https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/ext_proc/v3/external_processor.proto) field:

> "When responding to an HttpBody request, header mutations will only take effect if the current processing mode for the body is BUFFERED."

With the 2026 protocol clients, `Mcp-Method` and `Mcp-Name` headers must be sent and carry the needed routing information. Request bodies still need their tool names to be re-written and validated but the routing decision can be made and returned to Envoy before the body is sent.

## Summary

When the ext_proc adapter identifies a `2026-07-28` client in the request headers phase, it responds with a `ModeOverride` switching the request body mode to `STREAMED`. Routing completes entirely in the headers phase. The request body streams through for prefix stripping.

## Relationship to Router Design

This builds on the [router 2026-07-28 design](router-2026-07-28-design.md). That design established header-based routing and the `Router` interface. This document covers the ext_proc processing mode changes that header-based routing enables.

## Design

### Mode override on request header response

When the adapter receives request headers with `MCP-Protocol-Version: 2026-07-28`, the request header response includes a `ModeOverride`:

```go
responses[0].ModeOverride = &extprochttp.ProcessingMode{
    RequestHeaderMode:   extprochttp.ProcessingMode_SEND,
    ResponseHeaderMode:  extprochttp.ProcessingMode_SEND,
    // switches from buffered to streamed when 2026
    RequestBodyMode:     extprochttp.ProcessingMode_STREAMED, 
    // default to none unless a guardrails config exists or the response is an elicitation (existing behaviour)
    ResponseBodyMode:    extprochttp.ProcessingMode_NONE,
    RequestTrailerMode:  extprochttp.ProcessingMode_SKIP,
    ResponseTrailerMode: extprochttp.ProcessingMode_SKIP,
}
```

`ResponseBodyMode` defaults to `NONE` — the response streams directly from backend to client without ext_proc involvement except in specific circumstances (elicitation/guardrails).

This is already the case. For `2025-11-25` clients, the existing behaviour is unchanged: `BUFFERED` request body for JSON-RPC parsing, `STREAMED` response body for elicitation ID rewriting (opted into when needed).

### Request body: streamed prefix stripping

With `STREAMED` mode, Envoy sends request body chunks to ext_proc as they arrive without waiting for the full body. The router needs the body only for tool name prefix stripping — rewriting `"name": "github_search"` to `"name": "search"` in the JSON-RPC body.

When no prefix is configured for the target server (known based on the server config), the request body needs no mutation. The adapter sets `RequestBodyMode` to `NONE`, eliminating the body phase entirely. Header-body name validation is the responsibility of the target MCP server.

### Response body: default NONE

For `2026-07-28`, the response body mode defaults to `NONE`. The response streams directly from the backend to the client — ext_proc is not involved. No session ID mapping, no elicitation ID rewriting, no SSE stream parsing. An exception to this is if there are guard rails configured (see [guardrails](../guardrails/guardrails-design.md))


### Header mutations and body-phase restrictions

In any non-`BUFFERED` mode, header mutations returned in body-phase responses are silently ignored. This also renders [`clear_route_cache`](https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/ext_proc/v3/external_processor.proto) ineffective in those modes: there are no header changes for Envoy to re-route on.

For `2025-11-25`, the router sets `:authority` and calls `ClearRouteCache` in the body phase after parsing the JSON-RPC method. This requires `BUFFERED` mode — switching to `STREAMED` would silently drop the `:authority` mutation and route requests to the broker instead of the target backend. In practice, the failure is intermittent: with `STREAMED` mode. This makes the issue difficult to diagnose since it appears as flaky routing rather than a consistent failure.

`2026-07-28` avoids this constraint entirely. All header mutations (`:authority`, `Mcp-Name`, `x-mcp-*`) complete in the request headers phase, before body processing begins. Body-phase responses contain only `BodyMutation` for prefix stripping, no header changes. This makes `STREAMED` body mode safe to use.

### Protocol-specific flow

```text
2026-07-28 client (with prefix):
  Request Headers → routing decision + ModeOverride(STREAMED, NONE)
  Request Body    → stream chunks, strip prefix, validate header-body match
  Response Headers→ pass-through
  Response Body   → NONE

2026-07-28 client (no prefix):
  Request Headers → routing decision + ModeOverride(NONE, NONE)
  Request Body    → skipped
  Response Headers→ pass-through
  Response Body   → NONE

2025-11-25 client:
  Request Headers → protocol selection only
  Request Body    → BUFFERED, parse JSON-RPC, routing decision, :authority set
  Response Headers→ session ID rewrite
  Response Body   → STREAMED (elicitation ID rewriting)

2026-07-28 client (guardrails, with prefix):
  Request Headers → routing decision + ModeOverride(STREAMED, FULL_DUPLEX_STREAMED)
  Request Body    → stream chunks, strip prefix
  Response Headers→ pass-through
  Response Body   → FULL_DUPLEX_STREAMED: SSE per-event check, JSON accumulate full body

2025-11-25 client (guardrails):
  Request Headers → protocol selection only
  Request Body    → BUFFERED, parse JSON-RPC, routing decision, :authority set
  Response Headers→ session ID rewrite
  Response Body   → FULL_DUPLEX_STREAMED: SSE per-event check, JSON accumulate full body
```

## Future Considerations

### Response body streaming for guardrails

When guardrails are configured, `ResponseBodyMode` is set to `FULL_DUPLEX_STREAMED` via one of two paths depending on scope. For gateway-level guardrails (Secret has default config IDs), the controller configures `FULL_DUPLEX_STREAMED` directly on the ext_proc filter — it applies to all responses. For per-server guardrails only (empty default IDs), the router sets it dynamically via `ModeOverride` in the request header response, scoped to requests targeting servers with guardrails. Both paths apply to `2026-07-28` and `2025-11-25` clients.

`FULL_DUPLEX_STREAMED` sends response body chunks to ext_proc without waiting for each response, allowing the router to forward chunks to the guardrails service without stalling the upstream read. The handling depends on response type: SSE responses (`text/event-stream`) are checked per-event as they arrive, while JSON responses (`application/json`) are accumulated in full (bounded by `maxBodyBytes`) before sending to guardrails. See the [guardrails design](../guardrails/guardrails-design.md) for details.

The primary integration target is [NeMo Guardrails](https://docs.nvidia.com/nemo/guardrails/reference/guardrails-api-server/chat-completions/chat-completions), which exposes an OpenAI-compatible `POST /v1/chat/completions` endpoint.
