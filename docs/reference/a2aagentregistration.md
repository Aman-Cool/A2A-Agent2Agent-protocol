# The A2AAgentRegistration Custom Resource Definition (CRD)

- [A2AAgentRegistration](#a2aagentregistration)
- [A2AAgentRegistrationSpec](#a2aagentregistrationspec)
- [TargetReference](#targetreference)
- [SecretReference](#secretreference)
- [A2AAgentRegistrationStatus](#a2aagentregistrationstatus)

## A2AAgentRegistration

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `spec` | [A2AAgentRegistrationSpec](#a2aagentregistrationspec) | Yes | The specification for A2AAgentRegistration custom resource |
| `status` | [A2AAgentRegistrationStatus](#a2aagentregistrationstatus) | No | The status for the custom resource |

## A2AAgentRegistrationSpec

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `targetRef` | [TargetReference](#targetreference) | Yes | An HTTPRoute that points to a backend A2A agent. Immutable once set — replacing an agent means replacing the registration. The controller discovers the backend service from this HTTPRoute and configures the broker to serve the agent's card and route requests to it. The HTTPRoute must have exactly one rule with exactly one backendRef and at least one hostname |
| `agentPrefix` | String | Yes | URL path segment that routes requests to this agent. Requests to `/a2a/{agentPrefix}` are routed to the agent referenced by `targetRef`, and the agent's card is served at `/a2a/{agentPrefix}/.well-known/agent-card.json`. Must match `^[a-z0-9][a-z0-9_]*$`. Immutable once set |
| `agentCardURL` | String | No | Overrides the URL the broker fetches the agent card from. If not specified, the card is fetched from the agent's well-known card path derived from the `targetRef` backend. Must match `^https?://` |
| `credentialRef` | [SecretReference](#secretreference) | No | Reference to a Secret containing authentication credentials used exclusively by the broker for agent card discovery. Never injected into client `message/send` or `tasks/*` requests. The secret must have the label `mcp.kuadrant.io/secret=true` |
| `state` | String | No | Desired operational state of the agent. Enum: `Enabled` (default), `Disabled`. When set to `Disabled`, the agent is removed from the API catalog and requests to its path prefix are no longer routed. The agent can be re-enabled at any time by setting this field back to `Enabled` |

## TargetReference

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `group` | String | No | Group of the target resource. Default: `gateway.networking.k8s.io` |
| `kind` | String | No | Kind of the target resource. Default: `HTTPRoute` |
| `name` | String | Yes | Name of the target HTTPRoute |
| `namespace` | String | No | Namespace of the target HTTPRoute. Defaults to the registration's own namespace. Cross-namespace references require a `ReferenceGrant` in the target namespace (`from`: `A2AAgentRegistration`, `to`: `HTTPRoute`); without one the registration is `Ready=False` and no config is written, and revoking the grant withdraws the agent's config |

## SecretReference

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `name` | String | Yes | Name of the Secret resource |
| `key` | String | No | Key within the Secret that contains the credential value. Default: `token` |

## A2AAgentRegistrationStatus

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `conditions` | []metav1.Condition | No | Latest available observations of the registration's state. The `Ready` condition indicates the agent's config has been written for the gateway; it is not a promise the upstream agent is reachable or serving |
