package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// register makes an agent the current registration for its path key, so refreshCard stores
// its card (refreshCard only writes for a currently-registered agent).
func (b *Broker) register(a *config.A2AAgent) {
	b.mu.Lock()
	b.agents[pathKey(a)] = a
	b.mu.Unlock()
}

// gatewayCard returns a minimal card whose interface advertises the agent's gateway path,
// so it passes fail-closed interface validation (no external host set in tests).
func gatewayCard(namespace, prefix string) string {
	return `{"name":"test","supportedInterfaces":[{"url":"http://gw.example/a2a/` + namespace + `/` + prefix + `"}]}`
}

func TestRefreshCard_200StoresRawCard(t *testing.T) {
	card := gatewayCard("mcp-test", "weather")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wellKnownAgentCard {
			t.Errorf("unexpected card path %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(card))
	}))
	defer srv.Close()

	b := newTestBroker()
	a := agentFor(srv.URL)
	b.register(a)
	b.refreshCard(context.Background(), "mcp-test", "weather", a)

	e, ok := b.store.Get("mcp-test", "weather")
	if !ok || string(e.Raw) != card {
		t.Fatalf("card not stored verbatim: %q ok=%v", e.Raw, ok)
	}
	if e.ETag != `"v1"` || e.SHA256 == "" {
		t.Fatalf("card metadata not captured: %+v", e)
	}
}

func TestRefreshCard_OversizeRejected(t *testing.T) {
	big := make([]byte, maxCardBytes+100)
	for i := range big {
		big[i] = 'a'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	b := newTestBroker()
	a := agentFor(srv.URL)
	b.register(a)
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte("stale")})
	b.refreshCard(context.Background(), "mcp-test", "weather", a)

	e, _ := b.store.Get("mcp-test", "weather")
	if string(e.Raw) != "stale" {
		t.Fatalf("oversize card must be rejected and the stale card kept, got %d bytes", len(e.Raw))
	}
}

func TestRefreshCard_SkipsStoreWhenAgentNotCurrent(t *testing.T) {
	card := gatewayCard("mcp-test", "weather")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(card))
	}))
	defer srv.Close()

	b := newTestBroker()
	// the agent is fetched but never registered (removed/replaced during the fetch)
	b.refreshCard(context.Background(), "mcp-test", "weather", agentFor(srv.URL))

	if _, ok := b.store.Get("mcp-test", "weather"); ok {
		t.Fatal("a card fetched for a no-longer-registered agent must not be stored")
	}
}

func TestRefreshCard_DoesNotClobberReplacedAgent(t *testing.T) {
	oldCard := gatewayCard("mcp-test", "weather")
	oldSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oldCard))
	}))
	defer oldSrv.Close()

	b := newTestBroker()
	// a new agent (different upstream URL) now holds the same namespace/prefix, card already cached
	b.register(&config.A2AAgent{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: "http://new-agent:9090"})
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte(`{"name":"new"}`)})

	// a slow fetch for the old agent completes now — it must not overwrite the replacement's card
	b.refreshCard(context.Background(), "mcp-test", "weather", agentFor(oldSrv.URL))

	e, _ := b.store.Get("mcp-test", "weather")
	if string(e.Raw) != `{"name":"new"}` {
		t.Fatalf("a stale fetch clobbered the replacement agent's card: %q", e.Raw)
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
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(gatewayCard("ns-a", "weather")))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(gatewayCard("ns-b", "search")))
	}))
	defer srvB.Close()

	b := newTestBroker()
	// two agents in different namespaces; keys are {namespace}/{agentPrefix}, distinct from Name
	b.SetAgents([]*config.A2AAgent{
		{Name: "ns-a/weather-agent", AgentPrefix: "weather", URL: srvA.URL},
		{Name: "ns-b/search-agent", AgentPrefix: "search", URL: srvB.URL},
	})
	b.refreshAll(context.Background())

	if _, ok := b.store.Get("ns-a", "weather"); !ok {
		t.Fatal("ns-a/weather card not refreshed")
	}
	if _, ok := b.store.Get("ns-b", "search"); !ok {
		t.Fatal("ns-b/search card not refreshed")
	}
}

func TestSetAgents_RefreshesNewAgentCard(t *testing.T) {
	card := gatewayCard("mcp-test", "weather")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(card))
	}))
	defer srv.Close()

	b := NewBroker(slog.Default(), NewMemoryStore(), time.Minute)
	b.SetAgents([]*config.A2AAgent{
		{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL},
	})

	// the card is fetched asynchronously on config change, not on the (1-minute) ticker
	deadline := time.Now().Add(2 * time.Second)
	for {
		if e, ok := b.store.Get("mcp-test", "weather"); ok && string(e.Raw) == card {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new agent's card was not refreshed on config change")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSetAgents_DoesNotRefetchCachedAgents(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	b := NewBroker(slog.Default(), NewMemoryStore(), time.Minute)
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte("cached")}) // already cached
	b.SetAgents([]*config.A2AAgent{
		{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL},
	})

	// give any stray goroutine a moment; a cached agent must not be re-fetched
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("cached agent must not be re-fetched on config change, got %d hits", n)
	}
	if e, _ := b.store.Get("mcp-test", "weather"); string(e.Raw) != "cached" {
		t.Fatal("cached card must be preserved")
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

func TestRefreshCard_NonGatewayCardFailsClosed(t *testing.T) {
	// upstream switches from a conforming card to one advertising its own address
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"supportedInterfaces":[{"url":"http://agent.internal:9090/a2a"}]}`))
	}))
	defer srv.Close()

	b := newTestBroker()
	a := agentFor(srv.URL)
	b.register(a)
	b.store.Set("mcp-test", "weather", CardEntry{Raw: []byte(gatewayCard("mcp-test", "weather"))})

	b.refreshCard(context.Background(), "mcp-test", "weather", a)

	// fail closed, not stale-on-error: the previously-valid card must be dropped, not served
	if _, ok := b.store.Get("mcp-test", "weather"); ok {
		t.Fatal("a card that fails validation must drop the cached copy")
	}
	if !b.cardRejected("mcp-test", "weather") {
		t.Fatal("agent must be marked invalid after failed card validation")
	}

	// the card endpoint refuses to serve
	rec := httptest.NewRecorder()
	b.ServeAgentCard(rec, httptest.NewRequest(http.MethodGet, "/a2a/mcp-test/weather/.well-known/agent-card.json", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid card must not be served, got %d", rec.Code)
	}

	// and the agent never enters the catalog
	rec = httptest.NewRecorder()
	b.ServeAPICatalog(rec, httptest.NewRequest(http.MethodGet, apiCatalogPath, nil))
	if strings.Contains(rec.Body.String(), "weather") {
		t.Fatalf("invalid agent must be excluded from the catalog, got %s", rec.Body.String())
	}
}

func TestRefreshCard_UnparseableCardFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	b := newTestBroker()
	a := agentFor(srv.URL)
	b.register(a)
	b.refreshCard(context.Background(), "mcp-test", "weather", a)

	if !b.cardRejected("mcp-test", "weather") {
		t.Fatal("an unparseable card must fail validation")
	}
}

func TestRefreshCard_RecoversAfterCardFixed(t *testing.T) {
	valid := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if valid {
			_, _ = w.Write([]byte(gatewayCard("mcp-test", "weather")))
			return
		}
		_, _ = w.Write([]byte(`{"url":"http://agent.internal:9090/a2a"}`))
	}))
	defer srv.Close()

	b := newTestBroker()
	a := agentFor(srv.URL)
	b.register(a)

	b.refreshCard(context.Background(), "mcp-test", "weather", a)
	if !b.cardRejected("mcp-test", "weather") {
		t.Fatal("non-gateway card must be rejected first")
	}

	// upstream fixes the card -> the next refresh clears the failure and serves again
	valid = true
	b.refreshCard(context.Background(), "mcp-test", "weather", a)
	if b.cardRejected("mcp-test", "weather") {
		t.Fatal("a fixed card must clear the validation failure")
	}
	if _, ok := b.store.Get("mcp-test", "weather"); !ok {
		t.Fatal("a fixed card must be cached and servable again")
	}
}

func TestValidation_UsesExternalHostFromConfig(t *testing.T) {
	// card advertises the right path on the WRONG host; with the gateway host known
	// from config, the host check must reject it
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"supportedInterfaces":[{"url":"http://evil.example/a2a/mcp-test/weather"}]}`))
	}))
	defer srv.Close()

	b := newTestBroker()
	cfg := &config.MCPServersConfig{MCPGatewayExternalHostname: "gw.example"}
	cfg.SetA2AAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL}})
	b.OnConfigChange(context.Background(), cfg)

	deadline := time.Now().Add(2 * time.Second)
	for !b.cardRejected("mcp-test", "weather") {
		if time.Now().After(deadline) {
			t.Fatal("wrong-host card must be rejected when the gateway host is configured")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSetAgents_RefetchesWhenCardURLChanges(t *testing.T) {
	cardA := `{"name":"a","supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/weather"}]}`
	cardB := `{"name":"b","supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/weather"}]}`
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cardA))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cardB))
	}))
	defer srvB.Close()

	b := newTestBroker()
	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srvA.URL}})
	waitForCard(t, b, cardA, "card from the first endpoint was never cached")

	// the registration is re-pointed at a different backend; the old card must not
	// be served until the next tick — the change triggers a prompt re-fetch
	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srvB.URL}})
	waitForCard(t, b, cardB, "card was not re-fetched after the endpoint changed")
}

func TestSetAgents_RefetchesWhenCredentialChanges(t *testing.T) {
	var lastAuth atomic.Value
	lastAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(gatewayCard("mcp-test", "weather")))
	}))
	defer srv.Close()

	b := newTestBroker()
	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL, Credential: "Bearer one"}})
	waitFor(t, "first fetch", func() bool { return lastAuth.Load() == "Bearer one" })

	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL, Credential: "Bearer two"}})
	waitFor(t, "re-fetch with the rotated credential", func() bool { return lastAuth.Load() == "Bearer two" })
}

func TestSetAgents_RefetchesWhenCACertChanges(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(gatewayCard("mcp-test", "weather")))
	}))
	defer srv.Close()
	_, caPEM := tlsCardServer(t, "x", "y") // any valid PEM

	b := newTestBroker()
	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL}})
	waitFor(t, "first fetch", func() bool { return atomic.LoadInt32(&hits) >= 1 })

	b.SetAgents([]*config.A2AAgent{{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL, CACert: caPEM}})
	waitFor(t, "re-fetch after the CA changed", func() bool { return atomic.LoadInt32(&hits) >= 2 })
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForCard polls until the cached weather card equals want.
func waitForCard(t *testing.T, b *Broker, want, msg string) {
	t.Helper()
	waitFor(t, msg, func() bool {
		e, ok := b.store.Get("mcp-test", "weather")
		return ok && string(e.Raw) == want
	})
}

func TestRefreshCard_StaleFetchDoesNotClobberRotatedCredential(t *testing.T) {
	// upstream serves a valid card; while a fetch for the OLD credential is in flight, the
	// registration rotates its credential — the fingerprint guard must drop the stale result
	card := gatewayCard("mcp-test", "weather")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(card))
	}))
	defer srv.Close()

	b := newTestBroker()
	oldAgent := &config.A2AAgent{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL, Credential: "Bearer old"}
	b.register(oldAgent)
	// the current registration already rotated to a new credential (same URL)
	b.register(&config.A2AAgent{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: srv.URL, Credential: "Bearer new"})

	// a fetch that ran against the OLD credential completes now
	b.refreshCard(context.Background(), "mcp-test", "weather", oldAgent)

	if _, ok := b.store.Get("mcp-test", "weather"); ok {
		t.Fatal("a fetch against a superseded credential must not be committed")
	}
}

func TestRefreshCard_StaleFetchDoesNotClobberOnGatewayCAChange(t *testing.T) {
	b := newTestBroker()
	card := gatewayCard("mcp-test", "weather")
	// the gateway CA bundle rotates while this fetch is in flight; refreshCard captured the
	// pre-rotation value (empty) at its start, so the commit-time compare must mismatch
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.caCertPEM = "rotated-bundle"
		b.mu.Unlock()
		_, _ = w.Write([]byte(card))
	}))
	defer srv.Close()

	a := agentFor(srv.URL)
	b.register(a)
	b.refreshCard(context.Background(), "mcp-test", "weather", a)

	if _, ok := b.store.Get("mcp-test", "weather"); ok {
		t.Fatal("a fetch captured under an old gateway CA must not commit after the bundle rotates")
	}
}
