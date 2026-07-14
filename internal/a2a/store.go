// Package a2a implements the broker side of A2A support: agent discovery via an
// RFC 9727 API Catalog and per-agent AgentCard serving, backed by a pluggable
// card store.
package a2a

import (
	"sync"
	"time"
)

// CardStore is the pluggable backend for cached upstream AgentCards. The default
// implementation is in-memory; the interface keeps the door open for a shared
// backend (for example a registry) so multi-replica gateways can share card
// state without each replica polling upstream independently.
//
// Cards are stored and served as raw bytes so a signed card's JWS signature
// survives byte-for-byte — the store never parses or rewrites the card.
type CardStore interface {
	// Get returns the cached entry for a namespace-qualified agent and whether
	// it was present.
	Get(namespace, prefix string) (CardEntry, bool)
	// Set stores or replaces the cached entry for an agent.
	Set(namespace, prefix string, entry CardEntry)
	// Delete removes an agent's cached entry.
	Delete(namespace, prefix string)
	// List returns every cached entry keyed by "namespace/prefix".
	List() map[string]CardEntry
}

// CardEntry is a cached AgentCard plus the metadata needed for conditional
// refresh (RFC 7232) and change detection.
type CardEntry struct {
	Raw          []byte    // the card exactly as served upstream, never rewritten
	ETag         string    // upstream ETag, for If-None-Match on refresh
	LastModified string    // upstream Last-Modified, for If-Modified-Since on refresh
	SHA256       string    // content hash, for change detection when no validators are present
	FetchedAt    time.Time // when this entry was last refreshed
}

// memoryStore is the default in-memory CardStore, safe for concurrent use.
type memoryStore struct {
	mu    sync.RWMutex
	cards map[string]CardEntry
}

// NewMemoryStore returns an in-memory CardStore.
func NewMemoryStore() CardStore {
	return &memoryStore{cards: map[string]CardEntry{}}
}

// storeKey is the namespace-qualified cache key; two agents sharing a prefix in
// different namespaces stay distinct.
func storeKey(namespace, prefix string) string { return namespace + "/" + prefix }

func (m *memoryStore) Get(namespace, prefix string) (CardEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.cards[storeKey(namespace, prefix)]
	return e, ok
}

func (m *memoryStore) Set(namespace, prefix string, entry CardEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cards[storeKey(namespace, prefix)] = entry
}

func (m *memoryStore) Delete(namespace, prefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cards, storeKey(namespace, prefix))
}

func (m *memoryStore) List() map[string]CardEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]CardEntry, len(m.cards))
	for k, v := range m.cards {
		out[k] = v
	}
	return out
}
