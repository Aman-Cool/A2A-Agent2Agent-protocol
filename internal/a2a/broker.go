package a2a

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

const (
	// DefaultRefreshInterval is the card-refresh ticker interval when none is given.
	DefaultRefreshInterval = time.Minute
	// cardFetchTimeout bounds a single upstream card fetch.
	cardFetchTimeout = 10 * time.Second
)

// Broker is the discovery half of A2A support: it observes the a2aAgents config, indexes
// agents by their namespace-qualified routing path, and caches each agent's AgentCard for
// verbatim serving. Per-agent card serving and the RFC 9727 catalog build on this.
type Broker struct {
	logger   *slog.Logger
	store    CardStore
	client   *http.Client
	interval time.Duration

	mu           sync.RWMutex
	agents       map[string]*config.A2AAgent // keyed by "{namespace}/{agentPrefix}"
	invalid      map[string]string           // key -> reason the card failed validation; excluded from discovery
	externalHost string                      // gateway public host, for card interface validation
}

var _ config.Observer = (*Broker)(nil)

// NewBroker returns a Broker backed by the given card store. interval is the card-refresh
// ticker period; a value <= 0 uses DefaultRefreshInterval.
func NewBroker(logger *slog.Logger, store CardStore, interval time.Duration) *Broker {
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	return &Broker{
		logger:   logger,
		store:    store,
		client:   &http.Client{Timeout: cardFetchTimeout},
		interval: interval,
		agents:   map[string]*config.A2AAgent{},
		invalid:  map[string]string{},
	}
}

// OnConfigChange implements config.Observer: it swaps in the latest enabled agent set and
// the gateway public host used for card interface validation.
func (b *Broker) OnConfigChange(_ context.Context, cfg *config.MCPServersConfig) {
	b.mu.Lock()
	b.externalHost = cfg.GetExternalHostname()
	b.mu.Unlock()
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
	// drop validation state for agents that are no longer registered
	for key := range b.invalid {
		if _, ok := next[key]; !ok {
			delete(b.invalid, key)
		}
	}
	b.mu.Unlock()
	b.evictStaleCards(next)
	b.refreshNewCards(next)
	b.logger.Debug("a2a broker agents updated", "count", len(next))
}

// evictStaleCards drops cached cards for agents that are no longer registered (removed or
// disabled), so the card store tracks the live agent set.
func (b *Broker) evictStaleCards(current map[string]*config.A2AAgent) {
	for key := range b.store.List() {
		if _, ok := current[key]; !ok {
			namespace, prefix, _ := strings.Cut(key, "/")
			b.store.Delete(namespace, prefix)
		}
	}
}

// GetAgentByPath resolves a namespace-qualified path (namespace, agentPrefix) to its agent.
func (b *Broker) GetAgentByPath(namespace, prefix string) (*config.A2AAgent, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	a, ok := b.agents[namespace+"/"+prefix]
	return a, ok
}

// cardRejected reports whether the agent's card failed interface validation.
func (b *Broker) cardRejected(namespace, prefix string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, bad := b.invalid[namespace+"/"+prefix]
	return bad
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
