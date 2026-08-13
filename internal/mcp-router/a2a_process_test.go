package mcprouter

import (
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/headers"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
)

func newA2ATestServer(t *testing.T) *ExtProcServer {
	srv := newTestServer(t)
	srv.EnableA2A = true
	return srv
}

// a2aHeadersStep builds a request-headers step for an /a2a request.
func a2aHeadersStep(method, path string) mockProcessServerMessageAndErr {
	return mockProcessServerMessageAndErr{
		msg: &extProcV3.ProcessingRequest{
			Request: &extProcV3.ProcessingRequest_RequestHeaders{
				RequestHeaders: &extProcV3.HttpHeaders{
					Headers: &corev3.HeaderMap{
						Headers: []*corev3.HeaderValue{
							{Key: ":method", RawValue: []byte(method)},
							{Key: ":path", RawValue: []byte(path)},
							{Key: "content-type", RawValue: []byte("application/json")},
							// a client-supplied value the router must strip
							{Key: headers.A2AAgentHeader, RawValue: []byte("spoofed")},
						},
					},
				},
			},
		},
		// the router sets x-a2a-agent from the path and strips the client x-a2a-*
		resp: []*extProcV3.ProcessingResponse{
			{
				Response: &extProcV3.ProcessingResponse_RequestHeaders{
					RequestHeaders: &extProcV3.HeadersResponse{
						Response: &extProcV3.CommonResponse{
							HeaderMutation: &extProcV3.HeaderMutation{
								SetHeaders: []*corev3.HeaderValueOption{
									{Header: &corev3.HeaderValue{Key: headers.A2AAgentHeader, RawValue: []byte("weather")}},
								},
								RemoveHeaders: []string{headers.A2AAgentHeader, headers.A2AMethodHeader},
							},
						},
					},
				},
			},
		},
	}
}

// a2aMethodBodyResp expects a request-body response that sets x-a2a-method.
func a2aMethodBodyResp(method string) *extProcV3.ProcessingResponse {
	return &extProcV3.ProcessingResponse{
		Response: &extProcV3.ProcessingResponse_RequestBody{
			RequestBody: &extProcV3.BodyResponse{
				Response: &extProcV3.CommonResponse{
					HeaderMutation: &extProcV3.HeaderMutation{
						SetHeaders: []*corev3.HeaderValueOption{
							{Header: &corev3.HeaderValue{Key: headers.A2AMethodHeader, RawValue: []byte(method)}},
						},
					},
				},
			},
		},
	}
}

func a2aBodyStep(body string, resp *extProcV3.ProcessingResponse) mockProcessServerMessageAndErr {
	return mockProcessServerMessageAndErr{
		msg: &extProcV3.ProcessingRequest{
			Request: &extProcV3.ProcessingRequest_RequestBody{
				RequestBody: &extProcV3.HttpBody{Body: []byte(body), EndOfStream: true},
			},
		},
		resp: []*extProcV3.ProcessingResponse{resp},
	}
}

func TestProcess_A2APassthrough_SetsHeaders(t *testing.T) {
	srv := newA2ATestServer(t)
	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		a2aHeadersStep("POST", "/a2a/weather"),
		a2aBodyStep(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{}}}`, a2aMethodBodyResp("SendMessage")),
		responseHeadersStep(),
	})
	require.NoError(t, srv.Process(mock))
	mock.verifyAllResponsesConsumed()
}

func TestProcess_A2APassthrough_UnknownMethodNormalized(t *testing.T) {
	srv := newA2ATestServer(t)
	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		a2aHeadersStep("POST", "/a2a/weather"),
		// an unknown method is normalized to "other" so the label stays bounded
		a2aBodyStep(`{"jsonrpc":"2.0","id":1,"method":"ListTasks"}`, a2aMethodBodyResp("other")),
		responseHeadersStep(),
	})
	require.NoError(t, srv.Process(mock))
	mock.verifyAllResponsesConsumed()
}

func TestProcess_A2APassthrough_FailsClosedOnUnparseableBody(t *testing.T) {
	srv := newA2ATestServer(t)
	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		a2aHeadersStep("POST", "/a2a/weather"),
		// an unparseable body is rejected with a JSON-RPC error, never forwarded
		a2aBodyStep(`{not json`, &extProcV3.ProcessingResponse{
			Response: &extProcV3.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extProcV3.ImmediateResponse{
					Body:   []byte("dummy"),
					Status: &typev3.HttpStatus{Code: typev3.StatusCode_OK},
					Headers: &extProcV3.HeaderMutation{
						SetHeaders: []*corev3.HeaderValueOption{
							{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("application/json")}},
						},
					},
				},
			},
		}),
	})
	require.NoError(t, srv.Process(mock))
	mock.verifyAllResponsesConsumed()
}

// with the flag off, an /a2a request takes the normal MCP path (no A2A headers) —
// proving the feature is fully gated.
func TestProcess_A2ADisabled_TakesMCPPath(t *testing.T) {
	srv := newTestServer(t) // EnableA2A stays false
	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestHeaders{
					RequestHeaders: &extProcV3.HttpHeaders{
						EndOfStream: true, // GET-style, no body
						Headers: &corev3.HeaderMap{
							Headers: []*corev3.HeaderValue{
								{Key: ":method", RawValue: []byte("GET")},
								{Key: ":path", RawValue: []byte("/a2a/weather")},
							},
						},
					},
				},
			},
			// the standard MCP HandleRequestHeaders response — :authority set, MCP
			// internal headers stripped ; crucially NOT the A2A x-a2a-agent shape
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_RequestHeaders{
						RequestHeaders: &extProcV3.HeadersResponse{
							Response: &extProcV3.CommonResponse{
								HeaderMutation: &extProcV3.HeaderMutation{
									SetHeaders: []*corev3.HeaderValueOption{
										{Header: &corev3.HeaderValue{Key: ":authority"}},
									},
									RemoveHeaders: []string{"x-mcp-authorized", "x-mcp-virtualserver", "x-mcp-verified-sub"},
								},
							},
						},
					},
				},
			},
		},
		// body phase (endOfStream) → do-nothing, then response headers ends it
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{RequestBody: &extProcV3.HttpBody{}},
			},
			resp: []*extProcV3.ProcessingResponse{
				{Response: &extProcV3.ProcessingResponse_RequestBody{RequestBody: &extProcV3.BodyResponse{Response: &extProcV3.CommonResponse{}}}},
			},
		},
		responseHeadersStep(),
	})
	require.NoError(t, srv.Process(mock))
	mock.verifyAllResponsesConsumed()
}
