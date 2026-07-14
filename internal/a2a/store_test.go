package a2a

import "testing"

func TestMemoryStoreRoundTrip(t *testing.T) {
	s := NewMemoryStore()

	if _, ok := s.Get("mcp-test", "weather"); ok {
		t.Fatal("expected empty store")
	}

	s.Set("mcp-test", "weather", CardEntry{Raw: []byte("card"), ETag: "v1"})
	e, ok := s.Get("mcp-test", "weather")
	if !ok || string(e.Raw) != "card" || e.ETag != "v1" {
		t.Fatalf("unexpected entry: %+v ok=%v", e, ok)
	}

	s.Delete("mcp-test", "weather")
	if _, ok := s.Get("mcp-test", "weather"); ok {
		t.Fatal("expected entry deleted")
	}
}

// TestMemoryStoreNamespaceScoping guards the namespace-qualified key: the same
// prefix in two namespaces must not collide.
func TestMemoryStoreNamespaceScoping(t *testing.T) {
	s := NewMemoryStore()
	s.Set("ns-a", "weather", CardEntry{Raw: []byte("a")})
	s.Set("ns-b", "weather", CardEntry{Raw: []byte("b")})

	a, _ := s.Get("ns-a", "weather")
	b, _ := s.Get("ns-b", "weather")
	if string(a.Raw) != "a" || string(b.Raw) != "b" {
		t.Fatalf("namespace scoping broken: a=%q b=%q", a.Raw, b.Raw)
	}
	if got := len(s.List()); got != 2 {
		t.Fatalf("expected 2 entries, got %d", got)
	}
}
