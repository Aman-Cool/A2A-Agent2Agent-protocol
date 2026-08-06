package broker

import (
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// collectToolsCacheMetadata returns cache metadata for the upstreams that
// contributed to the tools protocol cache. Uses the precomputed serverIDs
// from rebuildProtocolCaches — no per-tool Meta extraction needed.
func (m *mcpBrokerImpl) collectToolsCacheMetadata() []upstream.CacheMetadata {
	cached := m.statelessTools.Load()
	if cached == nil {
		return nil
	}
	return m.lookupCacheMetadata(cached.serverIDs, func(mgr upstream.ActiveMCPServer) upstream.CacheMetadata {
		return mgr.ToolsCacheMetadata()
	})
}

// collectPromptsCacheMetadata returns cache metadata for the upstreams that
// contributed to the prompts protocol cache.
func (m *mcpBrokerImpl) collectPromptsCacheMetadata() []upstream.CacheMetadata {
	cached := m.statelessPrompts.Load()
	if cached == nil {
		return nil
	}
	return m.lookupCacheMetadata(cached.serverIDs, func(mgr upstream.ActiveMCPServer) upstream.CacheMetadata {
		return mgr.PromptsCacheMetadata()
	})
}

// lookupCacheMetadata fetches cache metadata for each server ID from the
// active managers.
func (m *mcpBrokerImpl) lookupCacheMetadata(serverIDs []config.UpstreamMCPID, metaFn func(upstream.ActiveMCPServer) upstream.CacheMetadata) []upstream.CacheMetadata {
	if len(serverIDs) == 0 {
		return nil
	}

	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	var contributing []upstream.CacheMetadata
	for _, id := range serverIDs {
		mgr, ok := m.mcpServers[id]
		if !ok {
			continue
		}
		contributing = append(contributing, metaFn(mgr))
	}
	return contributing
}
