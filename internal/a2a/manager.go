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
		b.store.Set(namespace, prefix, prev)
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxCardBytes))
		if err != nil {
			b.logger.Warn("a2a card read failed, keeping stale card", "agent", agent.Name, "error", err)
			return
		}
		sum := sha256.Sum256(body)
		sha := hex.EncodeToString(sum[:])
		if hasPrev && sha == prev.SHA256 {
			// content unchanged despite a 200 (upstream sent no validators) — refresh metadata only
			prev.FetchedAt = time.Now()
			prev.ETag = resp.Header.Get("ETag")
			prev.LastModified = resp.Header.Get("Last-Modified")
			b.store.Set(namespace, prefix, prev)
			return
		}
		b.store.Set(namespace, prefix, CardEntry{
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

// cardURL returns the agent's AgentCard URL: the explicit AgentCardURL override if set,
// otherwise the well-known path under the agent's base URL.
func cardURL(a *config.A2AAgent) string {
	if a.AgentCardURL != "" {
		return a.AgentCardURL
	}
	return strings.TrimRight(a.URL, "/") + wellKnownAgentCard
}
