package a2a

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

func brokerWithWeatherAgent() *Broker {
	b := NewBroker(slog.Default(), NewMemoryStore(), time.Minute)
	b.SetAgents([]*config.A2AAgent{
		{Name: "mcp-test/weather-agent", AgentPrefix: "weather"},
	})
	return b
}

func TestServeAgentCard_ServesVerbatim(t *testing.T) {
	b := brokerWithWeatherAgent()
	const card = `{"name":"weather","signatures":[{"protected":"x"}]}`
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte(card), ETag: `"v1"`})

	req := httptest.NewRequest(http.MethodGet, "/a2a/mcp-test/weather/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	b.ServeAgentCard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != card {
		t.Fatalf("card not served verbatim: %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("unexpected content-type %q", ct)
	}
	if rec.Header().Get("ETag") != `"v1"` {
		t.Fatalf("etag not propagated")
	}
}

func TestServeAgentCard_UnknownAgentIs404(t *testing.T) {
	b := brokerWithWeatherAgent()
	req := httptest.NewRequest(http.MethodGet, "/a2a/mcp-test/unknown/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	b.ServeAgentCard(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown agent, got %d", rec.Code)
	}
}

func TestServeAgentCard_NotYetCachedIs503(t *testing.T) {
	b := brokerWithWeatherAgent() // registered, but no card stored yet
	req := httptest.NewRequest(http.MethodGet, "/a2a/mcp-test/weather/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	b.ServeAgentCard(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when card not yet fetched, got %d", rec.Code)
	}
}

func TestParseCardPath(t *testing.T) {
	cases := []struct {
		path   string
		ns     string
		prefix string
		ok     bool
	}{
		{"/a2a/mcp-test/weather/.well-known/agent-card.json", "mcp-test", "weather", true},
		{"/a2a/ns/p/.well-known/agent-card.json", "ns", "p", true},
		{"/a2a/mcp-test/weather", "", "", false},                       // no card suffix
		{"/a2a/only-one/.well-known/agent-card.json", "", "", false},   // missing prefix
		{"/a2a/ns/pre/fix/.well-known/agent-card.json", "", "", false}, // extra path segment
		{"/other/ns/p/.well-known/agent-card.json", "", "", false},     // wrong prefix
	}
	for _, c := range cases {
		ns, prefix, ok := parseCardPath(c.path)
		if ok != c.ok || ns != c.ns || prefix != c.prefix {
			t.Errorf("parseCardPath(%q) = (%q,%q,%v), want (%q,%q,%v)", c.path, ns, prefix, ok, c.ns, c.prefix, c.ok)
		}
	}
}

func TestServeAPICatalog(t *testing.T) {
	b := NewBroker(slog.Default(), NewMemoryStore(), time.Minute)
	b.SetAgents([]*config.A2AAgent{
		{Name: "mcp-test/weather-agent", AgentPrefix: "weather"},
		{Name: "mcp-test/search-agent", AgentPrefix: "search"},
	})
	// catalog eligibility requires a currently cached card
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte(gatewayCard("mcp-test", "weather"))})
	b.store.Set("mcp-test", "search", CardEntry{Raw: []byte(gatewayCard("mcp-test", "search"))})

	req := httptest.NewRequest(http.MethodGet, apiCatalogPath, nil)
	rec := httptest.NewRecorder()
	b.ServeAPICatalog(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != linksetContentType {
		t.Fatalf("unexpected content-type %q", ct)
	}
	var doc linkset
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("catalog is not valid json: %v", err)
	}
	if len(doc.Linkset) != 1 {
		t.Fatalf("expected one link context, got %d", len(doc.Linkset))
	}
	hrefs := map[string]bool{}
	for _, it := range doc.Linkset[0].Item {
		hrefs[it.Href] = true
	}
	if !hrefs["/a2a/mcp-test/weather"] || !hrefs["/a2a/mcp-test/search"] {
		t.Fatalf("catalog missing expected hrefs: %+v", doc.Linkset[0].Item)
	}
}

func TestServeAPICatalog_EmptyWhenNoAgents(t *testing.T) {
	b := NewBroker(slog.Default(), NewMemoryStore(), time.Minute)
	req := httptest.NewRequest(http.MethodGet, apiCatalogPath, nil)
	rec := httptest.NewRecorder()
	b.ServeAPICatalog(rec, req)

	var doc linkset
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("catalog is not valid json: %v", err)
	}
	if len(doc.Linkset) != 1 || len(doc.Linkset[0].Item) != 0 {
		t.Fatalf("expected an empty item list, got %+v", doc.Linkset)
	}
}

func TestServeAPICatalog_ExcludesUncachedAgent(t *testing.T) {
	b := NewBroker(slog.Default(), NewMemoryStore(), time.Minute)
	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather"}})
	// registered but no card fetched yet -> must not be advertised (its card GET would 503)

	rec := httptest.NewRecorder()
	b.ServeAPICatalog(rec, httptest.NewRequest(http.MethodGet, apiCatalogPath, nil))

	var doc linkset
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("catalog not json: %v", err)
	}
	if len(doc.Linkset) != 1 || len(doc.Linkset[0].Item) != 0 {
		t.Fatalf("a registered-but-uncached agent must not be listed, got %+v", doc.Linkset)
	}
}
