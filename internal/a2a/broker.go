package a2a

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// Broker is the discovery half of A2A support: it observes the a2aAgents config and
// indexes agents by their namespace-qualified routing path. Later commits add the card
// cache, per-agent card serving, and the RFC 9727 catalog.
type Broker struct {
	logger *slog.Logger

	mu     sync.RWMutex
	agents map[string]*config.A2AAgent // keyed by "{namespace}/{agentPrefix}"
}

var _ config.Observer = (*Broker)(nil)

// NewBroker returns an empty Broker.
func NewBroker(logger *slog.Logger) *Broker {
	return &Broker{
		logger: logger,
		agents: map[string]*config.A2AAgent{},
	}
}

// OnConfigChange implements config.Observer: it swaps in the latest enabled agent set.
func (b *Broker) OnConfigChange(_ context.Context, cfg *config.MCPServersConfig) {
	b.SetAgents(cfg.ListA2AAgents())
}

// SetAgents rebuilds the agent index from a config snapshot, keyed by the namespace-qualified
// routing path and skipping disabled agents.
func (b *Broker) SetAgents(agents []*config.A2AAgent) {
	next := make(map[string]*config.A2AAgent, len(agents))
	for _, a := range agents {
		if a == nil || !agentEnabled(a) {
			continue
		}
		next[pathKey(a)] = a
	}
	b.mu.Lock()
	b.agents = next
	b.mu.Unlock()
	b.logger.Debug("a2a broker agents updated", "count", len(next))
}

// GetAgentByPath resolves a namespace-qualified path (namespace, agentPrefix) to its agent.
func (b *Broker) GetAgentByPath(namespace, prefix string) (*config.A2AAgent, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	a, ok := b.agents[namespace+"/"+prefix]
	return a, ok
}

// pathKey is the namespace-qualified routing key "{namespace}/{agentPrefix}". The agent's
// Name is "{namespace}/{registrationName}", so the namespace is its first segment while the
// routing prefix comes from AgentPrefix — the two can differ (e.g. Name "mcp-test/weather-agent",
// prefix "weather").
func pathKey(a *config.A2AAgent) string {
	namespace, _, _ := strings.Cut(a.Name, "/")
	return namespace + "/" + a.AgentPrefix
}

// agentEnabled reports whether the agent should be served; an empty State means Enabled,
// matching the config package's normalizeState convention.
func agentEnabled(a *config.A2AAgent) bool {
	return a.State == "" || a.State == "Enabled"
}
