package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=a2aar
// +kubebuilder:printcolumn:name="Prefix",type="string",JSONPath=".spec.agentPrefix",description="Path prefix for routing"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.targetRef.name",description="Target HTTPRoute. MCP Gateway only supports routes with a single BackendRef"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Credentials",type="string",JSONPath=".spec.credentialRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// A2AAgentRegistration registers an upstream A2A agent for discovery and routing through the gateway.
type A2AAgentRegistration struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of A2AAgentRegistration.
	// +optional
	Spec A2AAgentRegistrationSpec `json:"spec,omitempty"`

	// status defines the observed state of A2AAgentRegistration.
	// +optional
	Status A2AAgentRegistrationStatus `json:"status,omitempty"`
}

// A2AAgentRegistrationSpec defines the desired state of A2AAgentRegistration.
// It specifies which HTTPRoute points to an upstream A2A agent and the path
// prefix requests to that agent are routed on.
type A2AAgentRegistrationSpec struct {
	// targetRef specifies an HTTPRoute that points to a backend A2A agent.
	// The referenced HTTPRoute should have a backend service that implements the A2A protocol.
	// The controller will discover the backend service from this HTTPRoute and configure
	// the broker to serve the agent's card and route requests to it.
	// Immutable once set: retargeting a registration would leave the previous agent's
	// config behind, so replacing an agent means replacing the registration.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="targetRef is immutable once set"
	TargetRef TargetReference `json:"targetRef,omitzero"`

	// agentPrefix is the URL path segment that routes requests to this agent.
	// Requests to /a2a/{agentPrefix} are routed to the agent referenced by targetRef,
	// and the agent's card is served at /a2a/{agentPrefix}/.well-known/agent-card.json.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="agentPrefix is immutable once set"
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9_]*$`
	// +kubebuilder:validation:MinLength=1
	AgentPrefix string `json:"agentPrefix,omitempty"`

	// agentCardURL overrides the URL the broker fetches the agent card from.
	// If not specified, the card is fetched from the agent's well-known card path
	// derived from the targetRef backend.
	// +optional
	// +kubebuilder:validation:Pattern=`^https?://`
	AgentCardURL string `json:"agentCardURL,omitempty"`

	// credentialRef references a Secret containing authentication credentials for fetching the agent card.
	// Used exclusively by the broker for card discovery. Never injected into client
	// message/send or tasks/* requests.
	// +optional
	CredentialRef *SecretReference `json:"credentialRef,omitempty"`

	// state dictates whether the broker should serve and route to this agent.
	// When set to Disabled, the agent is removed from the API catalog and requests
	// to its path prefix are no longer routed. The agent can be re-enabled at any
	// time by setting this field back to Enabled.
	// Defaults to Enabled.
	// +optional
	// +default="Enabled"
	State ServerState `json:"state,omitempty"`
}

// A2AAgentRegistrationStatus represents the observed state of the A2AAgentRegistration resource.
type A2AAgentRegistrationStatus struct {
	// conditions represent the latest available observations of the A2AAgentRegistration's state.
	// The Ready condition indicates the agent's config has been written for the gateway; it is
	// not a promise the upstream agent is reachable or serving.
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true

// A2AAgentRegistrationList contains a list of A2AAgentRegistration
type A2AAgentRegistrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []A2AAgentRegistration `json:"items"`
}
