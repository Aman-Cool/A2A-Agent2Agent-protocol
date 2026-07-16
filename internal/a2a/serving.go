package a2a

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	apiCatalogPath     = "/.well-known/api-catalog"
	linksetContentType = "application/linkset+json"
	a2aPathPrefix      = "/a2a/"
)

func a2aTracer() trace.Tracer { return otel.Tracer("a2a") }

// ServeAgentCard serves an agent's cached AgentCard verbatim from the store, at
// /a2a/{namespace}/{prefix}/.well-known/agent-card.json. The card is written byte-for-byte, so
// a signed card's JWS signature is preserved; the broker is a cache, not a per-request proxy.
func (b *Broker) ServeAgentCard(w http.ResponseWriter, r *http.Request) {
	namespace, prefix, ok := parseCardPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, ok := b.GetAgentByPath(namespace, prefix); !ok {
		http.NotFound(w, r)
		return
	}
	entry, ok := b.store.Get(namespace, prefix)
	if !ok {
		// registered, but the card has not been fetched yet — the client can retry
		http.Error(w, "agent card not available yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if entry.ETag != "" {
		w.Header().Set("ETag", entry.ETag)
	}
	_, _ = w.Write(entry.Raw)
}

// parseCardPath extracts (namespace, prefix) from
// /a2a/{namespace}/{prefix}/.well-known/agent-card.json, rejecting any other shape.
func parseCardPath(path string) (namespace, prefix string, ok bool) {
	rest, found := strings.CutPrefix(path, a2aPathPrefix)
	if !found {
		return "", "", false
	}
	rest, found = strings.CutSuffix(rest, wellKnownAgentCard)
	if !found {
		return "", "", false
	}
	namespace, prefix, ok = strings.Cut(rest, "/")
	if !ok || namespace == "" || prefix == "" || strings.Contains(prefix, "/") {
		return "", "", false
	}
	return namespace, prefix, true
}

// ServeAPICatalog serves the RFC 9727 API Catalog as an RFC 9264 Linkset, listing every enabled
// agent's gateway path so a client can discover all agents without knowing upstream addresses.
func (b *Broker) ServeAPICatalog(w http.ResponseWriter, r *http.Request) {
	_, span := a2aTracer().Start(r.Context(), "a2a.ServeAPICatalog")
	defer span.End()

	b.mu.RLock()
	items := make([]linkTarget, 0, len(b.agents))
	for key := range b.agents { // key is "{namespace}/{agentPrefix}"
		items = append(items, linkTarget{Href: a2aPathPrefix + key})
	}
	b.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].Href < items[j].Href })

	span.SetAttributes(attribute.Int("agent.count", len(items)))

	doc := linkset{Linkset: []linkContext{{Anchor: apiCatalogPath, Item: items}}}
	w.Header().Set("Content-Type", linksetContentType)
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		b.logger.Error("failed to encode a2a api catalog", "error", err)
	}
}

// RFC 9264 Linkset document.
type linkset struct {
	Linkset []linkContext `json:"linkset"`
}

type linkContext struct {
	Anchor string       `json:"anchor"`
	Item   []linkTarget `json:"item"`
}

type linkTarget struct {
	Href string `json:"href"`
}
