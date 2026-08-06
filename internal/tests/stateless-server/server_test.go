package statelessserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserToolFilterMiddleware_PrivateScopeOnAuth(t *testing.T) {
	middleware := userToolFilterMiddleware()

	handler := middleware(func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		r := &mcp.ListToolsResult{
			Tools: []*mcp.Tool{
				{Name: "hello_world", InputSchema: map[string]any{"type": "object"}},
				{Name: "list_repos", InputSchema: map[string]any{"type": "object"}},
				{Name: "run_pipeline", InputSchema: map[string]any{"type": "object"}},
			},
		}
		r.CacheScope = "public"
		return r, nil
	})

	t.Run("no auth returns public scope", func(t *testing.T) {
		req := &mcp.ListToolsRequest{Extra: &mcp.RequestExtra{Header: http.Header{}}}
		result, err := handler(context.Background(), "tools/list", req)
		require.NoError(t, err)
		tr := result.(*mcp.ListToolsResult)
		assert.Equal(t, "public", tr.CacheScope)
		assert.Len(t, tr.Tools, 1, "only hello_world visible without auth (headers tool not in input)")
	})

	t.Run("with auth returns private scope", func(t *testing.T) {
		req := &mcp.ListToolsRequest{Extra: &mcp.RequestExtra{Header: http.Header{
			"Authorization": []string{"Bearer user-a-token"},
		}}}
		result, err := handler(context.Background(), "tools/list", req)
		require.NoError(t, err)
		tr := result.(*mcp.ListToolsResult)
		assert.Equal(t, "private", tr.CacheScope)
		assert.Len(t, tr.Tools, 2, "hello_world and list_repos visible for user-a")
	})
}
