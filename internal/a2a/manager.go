package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// wellKnownAgentCard is the A2A well-known AgentCard path (unchanged v0.3 → v1.0).
const wellKnownAgentCard = "/.well-known/agent-card.json"

// maxCardBytes caps a fetched AgentCard, guarding against a hostile or runaway upstream.
const maxCardBytes = 1 << 20 // 1 MiB

// Start runs the card-refresh loop until ctx is canceled: it refreshes every registered
// agent's card once immediately, then on each tick. Refresh is poll-only — A2A defines no
// card-change push — so the staleness bound is the refresh interval.
func (b *Broker) Start(ctx context.Context) {
	b.refreshAll(ctx)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.refreshAll(ctx)
		}
	}
}

// refreshAll refreshes the card for every currently registered agent. It snapshots the
// index first so it never holds the lock across an HTTP call.
func (b *Broker) refreshAll(ctx context.Context) {
	b.mu.RLock()
	snapshot := make(map[string]*config.A2AAgent, len(b.agents))
	for k, v := range b.agents {
		snapshot[k] = v
	}
	b.mu.RUnlock()

	for key, agent := range snapshot {
		namespace, prefix, _ := strings.Cut(key, "/")
		b.refreshCard(ctx, namespace, prefix, agent)
	}
}

// refreshNewCards fetches, in the background, the cards for agents that have no cached entry
// yet — a newly-registered agent, most often. Without this a new agent's card is unavailable
// until the next refresh tick (up to the ticker interval); this makes it available promptly.
// Only uncached agents are fetched, so a config change never re-fetches the whole set, and the
// fetches run sequentially in a single goroutine to avoid a burst against upstreams.
func (b *Broker) refreshNewCards(agents map[string]*config.A2AAgent) {
	type target struct {
		namespace, prefix string
		agent             *config.A2AAgent
	}
	var pending []target
	for key, agent := range agents {
		namespace, prefix, _ := strings.Cut(key, "/")
		if _, ok := b.store.Get(namespace, prefix); !ok {
			pending = append(pending, target{namespace, prefix, agent})
		}
	}
	if len(pending) == 0 {
		return
	}
	go func() {
		for _, t := range pending {
			b.refreshCard(context.Background(), t.namespace, t.prefix, t.agent)
		}
	}()
}

// refreshCard fetches one agent's AgentCard with a conditional GET and updates the store
// only when the content changes. On any error it leaves the existing (stale) entry in
// place — stale-on-error — so a transient upstream blip never drops a servable card. The
// card is stored as raw bytes so a signed card's JWS signature survives byte-for-byte.
func (b *Broker) refreshCard(ctx context.Context, namespace, prefix string, agent *config.A2AAgent) {
	prev, hasPrev := b.store.Get(namespace, prefix)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL(agent), nil)
	if err != nil {
		b.logger.Warn("a2a card request build failed", "agent", agent.Name, "error", err)
		return
	}
	// credentialRef is used ONLY for the card fetch, never surfaced to clients.
	if agent.Credential != "" {
		req.Header.Set("Authorization", agent.Credential)
	}
	if hasPrev {
		if prev.ETag != "" {
			req.Header.Set("If-None-Match", prev.ETag)
		}
		if prev.LastModified != "" {
			req.Header.Set("If-Modified-Since", prev.LastModified)
		}
	}

	resp, err := b.client.Do(req)
	if err != nil {
		b.logger.Warn("a2a card fetch failed, keeping stale card", "agent", agent.Name, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotModified && hasPrev:
		prev.FetchedAt = time.Now()
		b.storeIfCurrent(namespace, prefix, agent, prev)
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxCardBytes+1))
		if err != nil {
			b.logger.Warn("a2a card read failed, keeping stale card", "agent", agent.Name, "error", err)
			return
		}
		if int64(len(body)) > maxCardBytes {
			// reject rather than cache a truncated card — a clipped signed card would fail verification
			b.logger.Warn("a2a card exceeds max size, keeping stale card",
				"agent", agent.Name, "limit", maxCardBytes)
			return
		}
		sum := sha256.Sum256(body)
		sha := hex.EncodeToString(sum[:])
		if hasPrev && sha == prev.SHA256 {
			// content unchanged despite a 200 (upstream sent no validators) — refresh metadata
			// only; a stored entry already passed validation when it was stored
			prev.FetchedAt = time.Now()
			prev.ETag = resp.Header.Get("ETag")
			prev.LastModified = resp.Header.Get("Last-Modified")
			b.storeIfCurrent(namespace, prefix, agent, prev)
			return
		}
		b.mu.RLock()
		host := b.externalHost
		b.mu.RUnlock()
		if reason := validateCard(body, host, namespace, prefix); reason != "" {
			b.failCardValidation(namespace, prefix, agent, reason)
			return
		}
		b.storeIfCurrent(namespace, prefix, agent, CardEntry{
			Raw:          body,
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			SHA256:       sha,
			FetchedAt:    time.Now(),
		})
		b.logger.Debug("a2a card refreshed", "agent", agent.Name)
	default:
		b.logger.Warn("a2a card fetch non-200, keeping stale card",
			"agent", agent.Name, "status", resp.StatusCode)
	}
}

// storeIfCurrent writes entry to the card store only if agent is still the registered agent
// for (namespace, prefix). A card fetch runs without the lock, so a slow fetch can complete
// after the agent was removed or replaced (a new registration reusing the same
// namespace-qualified prefix); without this check that stale result would clobber the current
// agent's card. Identity is compared by card URL — stable across benign config churn, unlike
// the agent pointer. Holding the lock across the store write keeps the check and the write
// atomic against SetAgents; the lock order (broker lock, then store lock) is never inverted.
// Storing a validated card also clears any earlier validation failure, so an agent recovers
// as soon as its upstream serves a conforming card again.
func (b *Broker) storeIfCurrent(namespace, prefix string, agent *config.A2AAgent, entry CardEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := namespace + "/" + prefix
	cur, ok := b.agents[key]
	if !ok || cardURL(cur) != cardURL(agent) {
		return
	}
	delete(b.invalid, key)
	b.store.Set(namespace, prefix, entry)
}

// failCardValidation marks the agent's card invalid and drops any cached copy, so the agent
// is excluded from the catalog and its card is not served. This is deliberately NOT
// stale-on-error: a successful fetch of a non-conforming card is a configuration or security
// state, not a transient blip — serving the previous card would mask that the agent now
// advertises a gateway bypass. The same current-agent guard as storeIfCurrent applies.
func (b *Broker) failCardValidation(namespace, prefix string, agent *config.A2AAgent, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := namespace + "/" + prefix
	cur, ok := b.agents[key]
	if !ok || cardURL(cur) != cardURL(agent) {
		return
	}
	b.invalid[key] = reason
	b.store.Delete(namespace, prefix)
	b.logger.Warn("a2a card failed validation, agent excluded from discovery",
		"agent", agent.Name, "reason", reason)
}

// cardURL returns the agent's AgentCard URL: the explicit AgentCardURL override if set,
// otherwise the well-known path under the agent's base URL.
func cardURL(a *config.A2AAgent) string {
	if a.AgentCardURL != "" {
		return a.AgentCardURL
	}
	return strings.TrimRight(a.URL, "/") + wellKnownAgentCard
}
