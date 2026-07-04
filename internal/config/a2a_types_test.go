package config

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

func TestA2AAgent_ConfigChanged(t *testing.T) {
	base := A2AAgent{
		Name:         "mcp-test/weather-agent",
		URL:          "http://weather-agent.mcp-test.svc.cluster.local:9090",
		Hostname:     "weather-agent.mcp.local",
		AgentPrefix:  "weather",
		Credential:   "token-abc",
		AgentCardURL: "http://weather-agent.mcp-test.svc.cluster.local:9090/.well-known/agent-card.json",
		State:        "Enabled",
	}

	mutate := func(fn func(a *A2AAgent)) A2AAgent {
		copied := base
		fn(&copied)
		return copied
	}

	testCases := []struct {
		name     string
		existing A2AAgent
		want     bool
	}{
		{"identical", base, false},
		{"name changed", mutate(func(a *A2AAgent) { a.Name = "other" }), true},
		{"url changed", mutate(func(a *A2AAgent) { a.URL = "http://other:9090" }), true},
		{"hostname changed", mutate(func(a *A2AAgent) { a.Hostname = "other.mcp.local" }), true},
		{"prefix changed", mutate(func(a *A2AAgent) { a.AgentPrefix = "forecast" }), true},
		{"credential changed", mutate(func(a *A2AAgent) { a.Credential = "token-xyz" }), true},
		{"card url changed", mutate(func(a *A2AAgent) { a.AgentCardURL = "http://override/card.json" }), true},
		{"state changed", mutate(func(a *A2AAgent) { a.State = "Disabled" }), true},
		{"empty state treated as Enabled", mutate(func(a *A2AAgent) { a.State = "" }), false},
		{"auth ignored (file-config only)", mutate(func(a *A2AAgent) { a.Auth = &AuthConfig{Type: "bearer"} }), false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			agent := base
			if got := agent.ConfigChanged(tc.existing); got != tc.want {
				t.Errorf("ConfigChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpsertAndRemoveA2AAgent(t *testing.T) {
	srw := newTestSecretReaderWriter(t)
	ctx := context.Background()
	nn := types.NamespacedName{Namespace: "mcp-system", Name: "mcp-gateway-config"}

	agent := A2AAgent{Name: "mcp-test/weather-agent", URL: "http://weather:9090", AgentPrefix: "weather", State: "Enabled"}

	readConfig := func() *BrokerConfig {
		cfg, _, err := srw.readOrCreateConfigSecret(ctx, nn)
		if err != nil {
			t.Fatalf("failed to read config: %v", err)
		}
		return cfg
	}

	// insert creates the secret and appends the agent
	if err := srw.UpsertA2AAgent(ctx, agent, nn); err != nil {
		t.Fatalf("UpsertA2AAgent insert failed: %v", err)
	}
	cfg := readConfig()
	if len(cfg.A2AAgents) != 1 || cfg.A2AAgents[0].Name != agent.Name {
		t.Fatalf("expected 1 agent %q, got %+v", agent.Name, cfg.A2AAgents)
	}

	// upsert with a change replaces in place
	agent.URL = "http://weather-v2:9090"
	if err := srw.UpsertA2AAgent(ctx, agent, nn); err != nil {
		t.Fatalf("UpsertA2AAgent update failed: %v", err)
	}
	cfg = readConfig()
	if len(cfg.A2AAgents) != 1 || cfg.A2AAgents[0].URL != "http://weather-v2:9090" {
		t.Fatalf("expected updated url, got %+v", cfg.A2AAgents)
	}

	// upsert preserves the servers section (section ownership)
	if err := srw.UpsertMCPServer(ctx, MCPServer{Name: "srv", URL: "http://srv/mcp", State: "Enabled"}, nn); err != nil {
		t.Fatalf("UpsertMCPServer failed: %v", err)
	}
	if err := srw.UpsertA2AAgent(ctx, A2AAgent{Name: "other/agent", URL: "http://o:1", AgentPrefix: "other", State: "Enabled"}, nn); err != nil {
		t.Fatalf("UpsertA2AAgent second insert failed: %v", err)
	}
	cfg = readConfig()
	if len(cfg.Servers) != 1 || len(cfg.A2AAgents) != 2 {
		t.Fatalf("expected 1 server and 2 agents, got %d servers %d agents", len(cfg.Servers), len(cfg.A2AAgents))
	}

	// remove deletes only the named agent, leaves servers untouched
	if err := srw.RemoveA2AAgent(ctx, "mcp-test/weather-agent"); err != nil {
		t.Fatalf("RemoveA2AAgent failed: %v", err)
	}
	cfg = readConfig()
	if len(cfg.A2AAgents) != 1 || cfg.A2AAgents[0].Name != "other/agent" {
		t.Fatalf("expected only other/agent to remain, got %+v", cfg.A2AAgents)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers section must be preserved, got %+v", cfg.Servers)
	}

	// yaml round-trips the a2aAgents key
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	roundTrip := &BrokerConfig{}
	if err := yaml.Unmarshal(raw, roundTrip); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(roundTrip.A2AAgents) != 1 {
		t.Fatalf("a2aAgents did not round-trip: %s", raw)
	}
}
