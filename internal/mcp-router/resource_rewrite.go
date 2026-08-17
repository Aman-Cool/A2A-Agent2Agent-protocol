package mcprouter

import (
	"context"
	"log/slog"
)

// resourceURIRewriter rewrites resource URIs in tool call responses by stripping the server prefix.
type resourceURIRewriter struct {
	prefix string
	logger *slog.Logger
}

// Process processes response body and rewrites resource URIs by stripping the prefix.
func (r *resourceURIRewriter) Process(_ context.Context, body []byte) []byte {
	if r.prefix == "" {
		return body
	}
	// URI rewriting happens at the broker level via stripResourcePrefix.
	// The ext_proc adapter just passes through for now.
	return body
}

// Flush flushes any pending data (idempotent).
func (r *resourceURIRewriter) Flush(_ context.Context) []byte {
	return nil
}
