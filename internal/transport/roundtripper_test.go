package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureTransport records the request it receives.
type captureTransport struct {
	got *http.Request
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.got = req
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	return rec.Result(), nil
}

func newRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://upstream.local/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func TestHeaderRoundTripper_InjectsHeaders(t *testing.T) {
	base := &captureTransport{}
	rt := &HeaderRoundTripper{
		Base:    base,
		Headers: map[string]string{"Authorization": "Bearer abc", "X-Custom": "v"},
	}

	req := newRequest(t)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := base.got.Header.Get("Authorization"); got != "Bearer abc" {
		t.Errorf("expected injected Authorization, got %q", got)
	}
	if got := base.got.Header.Get("X-Custom"); got != "v" {
		t.Errorf("expected injected X-Custom, got %q", got)
	}
}

func TestHeaderRoundTripper_OverridesExistingHeader(t *testing.T) {
	base := &captureTransport{}
	rt := &HeaderRoundTripper{
		Base:    base,
		Headers: map[string]string{"Authorization": "Bearer injected"},
	}

	req := newRequest(t)
	req.Header.Set("Authorization", "Bearer original")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := base.got.Header.Get("Authorization"); got != "Bearer injected" {
		t.Errorf("expected injected value to win, got %q", got)
	}
}

// the round tripper must clone: http.RoundTripper contract forbids mutating
// the caller's request, and callers may retry with the original.
func TestHeaderRoundTripper_DoesNotMutateOriginalRequest(t *testing.T) {
	base := &captureTransport{}
	rt := &HeaderRoundTripper{
		Base:    base,
		Headers: map[string]string{"Authorization": "Bearer abc"},
	}

	req := newRequest(t)
	req.Header.Set("Accept", "application/json")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("original request must not gain injected headers, got %q", got)
	}
	if base.got == req {
		t.Error("base transport must receive a clone, not the original request")
	}
	if got := base.got.Header.Get("Accept"); got != "application/json" {
		t.Errorf("clone must keep the original headers, got %q", got)
	}
}

func TestHeaderRoundTripper_NilHeaders(t *testing.T) {
	base := &captureTransport{}
	rt := &HeaderRoundTripper{Base: base}

	req := newRequest(t)
	req.Header.Set("Accept", "application/json")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip with nil headers: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := base.got.Header.Get("Accept"); got != "application/json" {
		t.Errorf("expected original headers preserved, got %q", got)
	}
}

type statusTransport struct {
	code int
	body string
}

func (s *statusTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rec.WriteHeader(s.code)
	if s.body != "" {
		_, _ = rec.WriteString(s.body)
	}
	return rec.Result(), nil
}

func TestStatusCapturingRoundTripper_PassesThrough2xx(t *testing.T) {
	rt := &StatusCapturingRoundTripper{Base: &statusTransport{code: 200, body: "ok"}}
	resp, err := rt.RoundTrip(newRequest(t))
	if err != nil {
		t.Fatalf("unexpected error on 200: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestStatusCapturingRoundTripper_4xxReturnsTypedError(t *testing.T) {
	body := `{"message":"Access denied"}`
	rt := &StatusCapturingRoundTripper{Base: &statusTransport{code: 403, body: body}}
	_, err := rt.RoundTrip(newRequest(t)) //nolint:bodyclose // error path returns nil response
	if err == nil {
		t.Fatal("expected error on 403")
	}
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPStatusError, got %T: %v", err, err)
	}
	if httpErr.Code != 403 {
		t.Errorf("expected code 403, got %d", httpErr.Code)
	}
	if httpErr.Body != body {
		t.Errorf("expected body %q, got %q", body, httpErr.Body)
	}
}

func TestStatusCapturingRoundTripper_5xxReturnsTypedError(t *testing.T) {
	rt := &StatusCapturingRoundTripper{Base: &statusTransport{code: 502, body: "Bad Gateway"}}
	_, err := rt.RoundTrip(newRequest(t)) //nolint:bodyclose // error path returns nil response
	if err == nil {
		t.Fatal("expected error on 502")
	}
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPStatusError, got %T: %v", err, err)
	}
	if httpErr.Code != 502 {
		t.Errorf("expected code 502, got %d", httpErr.Code)
	}
}
