package broker

import (
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

func createMockActiveMCPServer(t *testing.T, cfg config.MCPServer, toolsCache, promptsCache upstream.CacheMetadata) upstream.ActiveMCPServer {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&cfg, "", nil)
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
	assert.NoError(t, err)
	manager.SetCacheMetadataForTesting(toolsCache, promptsCache)
	return upstream.NewActiveForTesting(manager)
}

func TestCollectToolsCacheMetadata(t *testing.T) {
	t.Run("returns one metadata entry per contributing server", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1", Prefix: "s1_"},
				upstream.CacheMetadata{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
				upstream.CacheMetadata{},
			),
			"server2": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server2", Prefix: "s2_"},
				upstream.CacheMetadata{TTLMs: 2000, CacheScope: upstream.CacheScopePrivate},
				upstream.CacheMetadata{},
			),
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		entry := protocolCacheEntry[*mcp.Tool]{
			items:     []*mcp.Tool{{Name: "s1_a"}, {Name: "s2_a"}},
			serverIDs: []config.UpstreamMCPID{"server1", "server2"},
		}
		broker.statelessTools.Store(&entry)

		got := broker.collectToolsCacheMetadata()
		assert.Len(t, got, 2)
		ttls := map[int]bool{}
		scopes := map[string]bool{}
		for _, m := range got {
			ttls[m.TTLMs] = true
			scopes[m.CacheScope] = true
		}
		assert.True(t, ttls[1000], "should contain server1 TTLMs 1000")
		assert.True(t, ttls[2000], "should contain server2 TTLMs 2000")
		assert.True(t, scopes[upstream.CacheScopePublic], "should contain public scope")
		assert.True(t, scopes[upstream.CacheScopePrivate], "should contain private scope")
	})

	t.Run("no servers returns nil", func(t *testing.T) {
		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = map[config.UpstreamMCPID]upstream.ActiveMCPServer{}
		entry := protocolCacheEntry[*mcp.Tool]{}
		broker.statelessTools.Store(&entry)

		got := broker.collectToolsCacheMetadata()
		assert.Nil(t, got)
	})

	t.Run("nil cache returns nil", func(t *testing.T) {
		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		got := broker.collectToolsCacheMetadata()
		assert.Nil(t, got)
	})
}

func TestCollectPromptsCacheMetadata(t *testing.T) {
	t.Run("returns one metadata entry per contributing server", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1", Prefix: "s1_"},
				upstream.CacheMetadata{},
				upstream.CacheMetadata{TTLMs: 500, CacheScope: upstream.CacheScopePublic, UserSpecificList: true},
			),
			"server2": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server2", Prefix: "s2_"},
				upstream.CacheMetadata{},
				upstream.CacheMetadata{TTLMs: 1500, CacheScope: upstream.CacheScopePrivate},
			),
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		entry := protocolCacheEntry[*mcp.Prompt]{
			items:     []*mcp.Prompt{{Name: "s1_a"}, {Name: "s2_a"}},
			serverIDs: []config.UpstreamMCPID{"server1", "server2"},
		}
		broker.statelessPrompts.Store(&entry)

		got := broker.collectPromptsCacheMetadata()
		assert.Len(t, got, 2)
		ttls := map[int]bool{}
		userSpecific := map[bool]bool{}
		for _, m := range got {
			ttls[m.TTLMs] = true
			userSpecific[m.UserSpecificList] = true
		}
		assert.True(t, ttls[500], "should contain server1 TTLMs 500")
		assert.True(t, ttls[1500], "should contain server2 TTLMs 1500")
		assert.True(t, userSpecific[true], "should contain server1 UserSpecificList=true")
	})

	t.Run("no servers returns nil", func(t *testing.T) {
		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = map[config.UpstreamMCPID]upstream.ActiveMCPServer{}
		entry := protocolCacheEntry[*mcp.Prompt]{}
		broker.statelessPrompts.Store(&entry)

		got := broker.collectPromptsCacheMetadata()
		assert.Nil(t, got)
	})
}
