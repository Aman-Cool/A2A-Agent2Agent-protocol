package a2a

import (
	"strings"
	"testing"
)

func TestValidateCard(t *testing.T) {
	cases := []struct {
		name         string
		card         string
		externalHost string
		wantReason   string // substring; empty = valid
	}{
		{
			name: "v1 single gateway interface",
			card: `{"supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/weather"}]}`,
		},
		{
			name: "v1 multiple gateway interfaces",
			card: `{"supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/weather"},{"url":"https://gw.example/a2a/mcp-test/weather/"}]}`,
		},
		{
			name:       "one direct-upstream interface among gateway ones",
			card:       `{"supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/weather"},{"url":"http://agent.internal:9090/a2a"}]}`,
			wantReason: "non-gateway interface URL",
		},
		{
			name: "v0.3 top-level url advertising the gateway path",
			card: `{"url":"http://gw.example/a2a/mcp-test/weather"}`,
		},
		{
			name:       "v0.3 top-level url advertising the upstream",
			card:       `{"url":"http://agent.internal:9090/a2a"}`,
			wantReason: "non-gateway interface URL",
		},
		{
			name:       "wrong agent path",
			card:       `{"supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/search"}]}`,
			wantReason: "non-gateway interface URL",
		},
		{
			name:       "non-http scheme at the gateway path",
			card:       `{"supportedInterfaces":[{"url":"ftp://gw.example/a2a/mcp-test/weather"}]}`,
			wantReason: "non-http(s)",
		},
		{
			name:       "non-JSONRPC binding at the gateway path",
			card:       `{"supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/weather","protocolBinding":"GRPC"}]}`,
			wantReason: "unsupported binding",
		},
		{
			name: "explicit JSONRPC binding passes",
			card: `{"supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/weather","protocolBinding":"JSONRPC","protocolVersion":"1.0"}]}`,
		},
		{
			name:       "non-v1 protocol version",
			card:       `{"supportedInterfaces":[{"url":"http://gw.example/a2a/mcp-test/weather","protocolVersion":"0.3"}]}`,
			wantReason: "v1-specific",
		},
		{
			name:       "not JSON",
			card:       `not a card`,
			wantReason: "not valid JSON",
		},
		{
			name:       "no interface URL at all",
			card:       `{"name":"weather","supportedInterfaces":[]}`,
			wantReason: "no interface URL",
		},
		{
			name:       "relative interface URL",
			card:       `{"supportedInterfaces":[{"url":"/a2a/mcp-test/weather"}]}`,
			wantReason: "unparseable interface URL",
		},
		{
			name:         "host mismatch when the gateway host is known",
			card:         `{"supportedInterfaces":[{"url":"http://evil.example/a2a/mcp-test/weather"}]}`,
			externalHost: "gw.example",
			wantReason:   "interface host",
		},
		{
			name:         "host match ignores ports on either side",
			card:         `{"supportedInterfaces":[{"url":"http://gw.example:8001/a2a/mcp-test/weather"}]}`,
			externalHost: "gw.example:8080",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := validateCard([]byte(tc.card), tc.externalHost, "mcp-test", "weather")
			if tc.wantReason == "" && reason != "" {
				t.Fatalf("expected valid, got reason %q", reason)
			}
			if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, reason)
			}
		})
	}
}
