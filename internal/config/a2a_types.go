package config

// A2AAgent represents an upstream A2A agent the gateway serves cards for and routes requests to.
type A2AAgent struct {
	Name         string      `json:"name"                   yaml:"name"`
	URL          string      `json:"url"                    yaml:"url"`
	Hostname     string      `json:"hostname,omitempty"     yaml:"hostname,omitempty"`
	AgentPrefix  string      `json:"agentPrefix,omitempty"  yaml:"agentPrefix,omitempty"`
	Auth         *AuthConfig `json:"auth,omitempty"         yaml:"auth,omitempty"`
	Credential   string      `json:"credential,omitempty"   yaml:"credential,omitempty"`
	AgentCardURL string      `json:"agentCardURL,omitempty" yaml:"agentCardURL,omitempty"`
	State        string      `json:"state"                  yaml:"state"`
}

// ConfigChanged reports whether the agent differs from existingConfig in any field the
// broker acts on. Used to skip config secret writes that would only trigger spurious
// broker reloads. Auth is file-config only (never controller-written), so it is not compared,
// mirroring MCPServer.ConfigChanged.
func (agent A2AAgent) ConfigChanged(existingConfig A2AAgent) bool {
	return existingConfig.Name != agent.Name ||
		existingConfig.URL != agent.URL ||
		existingConfig.Hostname != agent.Hostname ||
		existingConfig.AgentPrefix != agent.AgentPrefix ||
		existingConfig.Credential != agent.Credential ||
		existingConfig.AgentCardURL != agent.AgentCardURL ||
		normalizeState(existingConfig.State) != normalizeState(agent.State)
}

// GetName returns the agent name; part of the namedEntry contract shared with MCPServer.
func (agent A2AAgent) GetName() string {
	return agent.Name
}
