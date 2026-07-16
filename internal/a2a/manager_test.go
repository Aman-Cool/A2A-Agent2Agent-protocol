package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

func newTestBroker() *Broker {
	return NewBroker(slog.Default(), NewMemoryStore(), time.Minute)
}

func agentFor(url string) *config.A2AAgent {
	return &config.A2AAgent{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: url}
}

func TestRefreshCard_200StoresRawCard(t *testing.T) {
	const card = `{"name":"weather","supportedInterfaces":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wellKnownAgentCard {
			t.Errorf("unexpected card path %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(card))
	}))
	defer srv.Close()

	b := newTestBroker()
	b.refreshCard(context.Background(), "mcp-test", "weather", agentFor(srv.URL))

	e, ok := b.store.Get("mcp-test", "weather")
	if !ok || string(e.Raw) != card {
		t.Fatalf("card not stored verbatim: %q ok=%v", e.Raw, ok)
	}
	if e.ETag != `"v1"` || e.SHA256 == "" {
		t.Fatalf("card metadata not captured: %+v", e)
	}
}

func TestRefreshCard_304KeepsStoredCard(t *testing.T) {
	var gotINM string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	b := newTestBroker()
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte("cached"), ETag: `"v1"`, SHA256: "abc"})
	b.refreshCard(context.Background(), "mcp-test", "weather", agentFor(srv.URL))

	if gotINM != `"v1"` {
		t.Fatalf("conditional GET header not sent, got %q", gotINM)
	}
	e, _ := b.store.Get("mcp-test", "weather")
	if string(e.Raw) != "cached" {
		t.Fatalf("304 must keep the cached card, got %q", e.Raw)
	}
}

func TestRefreshCard_ErrorKeepsStale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // closed server -> connection refused

	b := newTestBroker()
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte("stale")})
	b.refreshCard(context.Background(), "mcp-test", "weather", agentFor(srv.URL))

	e, ok := b.store.Get("mcp-test", "weather")
	if !ok || string(e.Raw) != "stale" {
		t.Fatalf("fetch error must keep the stale card, got %q ok=%v", e.Raw, ok)
	}
}

func TestRefreshCard_Non200KeepsStale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := newTestBroker()
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte("stale")})
	b.refreshCard(context.Background(), "mcp-test", "weather", agentFor(srv.URL))

	e, _ := b.store.Get("mcp-test", "weather")
	if string(e.Raw) != "stale" {
		t.Fatalf("non-200 must keep the stale card, got %q", e.Raw)
	}
}

func TestRefreshCard_200SameContentKeepsCard(t *testing.T) {
	const card = `{"name":"weather"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(card))
	}))
	defer srv.Close()

	b := newTestBroker()
	sum := sha256.Sum256([]byte(card))
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte(card), SHA256: hex.EncodeToString(sum[:])})
	b.refreshCard(context.Background(), "mcp-test", "weather", agentFor(srv.URL))

	e, _ := b.store.Get("mcp-test", "weather")
	if string(e.Raw) != card {
		t.Fatalf("unchanged content must keep the card, got %q", e.Raw)
	}
}

func TestRefreshCard_AppliesCredential(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	b := newTestBroker()
	agent := agentFor(srv.URL)
	agent.Credential = "Bearer sekret"
	b.refreshCard(context.Background(), "mcp-test", "weather", agent)

	if gotAuth != "Bearer sekret" {
		t.Fatalf("credentialRef not applied to card fetch, got %q", gotAuth)
	}
}

func TestCardURL(t *testing.T) {
	if got := cardURL(&config.A2AAgent{URL: "http://agent:9090"}); got != "http://agent:9090/.well-known/agent-card.json" {
		t.Fatalf("derived card url wrong: %q", got)
	}
	override := &config.A2AAgent{URL: "http://agent:9090/", AgentCardURL: "http://agent:9090/custom/card.json"}
	if got := cardURL(override); got != "http://agent:9090/custom/card.json" {
		t.Fatalf("AgentCardURL override not honored: %q", got)
	}
}

func TestRefreshAll_RefreshesEveryAgentUnderItsPathKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"x"}`))
	}))
	defer srv.Close()

	b := newTestBroker()
	// two agents in different namespaces; keys are {namespace}/{agentPrefix}, distinct from Name
	b.SetAgents([]*config.A2AAgent{
		{Name: "ns-a/weather-agent", AgentPrefix: "weather", URL: srv.URL},
		{Name: "ns-b/search-agent", AgentPrefix: "search", URL: srv.URL},
	})
	b.refreshAll(context.Background())

	if _, ok := b.store.Get("ns-a", "weather"); !ok {
		t.Fatal("ns-a/weather card not refreshed")
	}
	if _, ok := b.store.Get("ns-b", "search"); !ok {
		t.Fatal("ns-b/search card not refreshed")
	}
}

func TestSetAgents_EvictsRemovedCard(t *testing.T) {
	b := newTestBroker()
	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather"}})
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte("card")})

	// reconfigure without the weather agent -> its cached card must be evicted
	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/search-agent", AgentPrefix: "search"}})
	if _, ok := b.store.Get("mcp-test", "weather"); ok {
		t.Fatal("removed agent's card must be evicted from the store")
	}
}
