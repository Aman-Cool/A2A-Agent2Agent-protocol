package a2a

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

func TestOnConfigChangeIndexesAgentsByPath(t *testing.T) {
	b := NewBroker(slog.Default())
	cfg := &config.MCPServersConfig{}
	cfg.SetA2AAgents([]*config.A2AAgent{
		{Name: "mcp-test/weather-agent", AgentPrefix: "weather", URL: "http://weather:9090", State: "Enabled"},
		{Name: "other-ns/search-reg", AgentPrefix: "search", URL: "http://search:9090"},
	})

	b.OnConfigChange(context.Background(), cfg)

	// the routing key is {namespace}/{agentPrefix} — it differs from the registration Name
	got, ok := b.GetAgentByPath("mcp-test", "weather")
	if !ok || got.URL != "http://weather:9090" {
		t.Fatalf("expected weather agent, got %+v ok=%v", got, ok)
	}
	if _, ok := b.GetAgentByPath("mcp-test", "weather-agent"); ok {
		t.Fatal("must key by agentPrefix, not the registration name")
	}
	if _, ok := b.GetAgentByPath("other-ns", "search"); !ok {
		t.Fatal("empty State should be treated as enabled")
	}
}

func TestSetAgentsSkipsDisabledAndReplaces(t *testing.T) {
	b := NewBroker(slog.Default())
	b.SetAgents([]*config.A2AAgent{
		{Name: "ns/a", AgentPrefix: "a", State: "Enabled"},
		{Name: "ns/b", AgentPrefix: "b", State: "Disabled"},
	})
	if _, ok := b.GetAgentByPath("ns", "a"); !ok {
		t.Fatal("enabled agent missing")
	}
	if _, ok := b.GetAgentByPath("ns", "b"); ok {
		t.Fatal("disabled agent must be skipped")
	}

	// SetAgents replaces, it does not merge
	b.SetAgents([]*config.A2AAgent{{Name: "ns/c", AgentPrefix: "c"}})
	if _, ok := b.GetAgentByPath("ns", "a"); ok {
		t.Fatal("SetAgents must replace the prior index")
	}
	if _, ok := b.GetAgentByPath("ns", "c"); !ok {
		t.Fatal("new agent missing after replace")
	}
}
