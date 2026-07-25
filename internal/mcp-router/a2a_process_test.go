package mcprouter

import (
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
)

// stubA2ABroker resolves a fixed set of agents keyed by "namespace/prefix".
type stubA2ABroker struct {
	agents map[string]*config.A2AAgent
}

func (s *stubA2ABroker) GetAgentByPath(namespace, prefix string) (*config.A2AAgent, bool) {
	a, ok := s.agents[namespace+"/"+prefix]
	return a, ok
}

// testAgentHost is the resolved agent Hostname the stub broker returns; the router
// sets it as :authority when routing an invocation.
const testAgentHost = "weather-agent.mcp-test.local"

func newA2ATestServer(t *testing.T) *ExtProcServer {
	srv := newTestServer(t)
	srv.A2ABroker = &stubA2ABroker{
		agents: map[string]*config.A2AAgent{
			"mcp-test/weather": {Name: "mcp-test/weather-agent", Hostname: testAgentHost},
		},
	}
	return srv
}

// a2aPostHeadersStep builds a POST request-headers step for the given path.
// (GET discovery passthrough uses WithDoNothingResponse(true), whose nil
// CommonResponse the mock stream cannot validate, so it is covered by the
// resolveA2APath unit tests and the discovery e2e instead.)
func a2aPostHeadersStep(path string, resp []*extProcV3.ProcessingResponse) mockProcessServerMessageAndErr {
	return mockProcessServerMessageAndErr{
		msg: &extProcV3.ProcessingRequest{
			Request: &extProcV3.ProcessingRequest_RequestHeaders{
				RequestHeaders: &extProcV3.HttpHeaders{
					Headers: &corev3.HeaderMap{
						Headers: []*corev3.HeaderValue{
							{Key: ":method", RawValue: []byte("POST")},
							{Key: ":path", RawValue: []byte(path)},
							{Key: "content-type", RawValue: []byte("application/json")},
						},
					},
				},
			},
		},
		resp: resp,
	}
}

func a2aBodyStep(body string, resp []*extProcV3.ProcessingResponse) mockProcessServerMessageAndErr {
	return mockProcessServerMessageAndErr{
		msg: &extProcV3.ProcessingRequest{
			Request: &extProcV3.ProcessingRequest_RequestBody{
				RequestBody: &extProcV3.HttpBody{Body: []byte(body), EndOfStream: true},
			},
		},
		resp: resp,
	}
}

// immediateJSONResponse is the expected shape for an A2A JSON-RPC error.
func immediateJSONResponse(code typev3.StatusCode) *extProcV3.ProcessingResponse {
	return &extProcV3.ProcessingResponse{
		Response: &extProcV3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &extProcV3.ImmediateResponse{
				Body:   []byte("dummy"),
				Status: &typev3.HttpStatus{Code: code},
				Headers: &extProcV3.HeaderMutation{
					SetHeaders: []*corev3.HeaderValueOption{
						{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("application/json")}},
					},
				},
			},
		},
	}
}

// doNothingBody is the expected shape for a passed-through request body.
func doNothingBody() *extProcV3.ProcessingResponse {
	return &extProcV3.ProcessingResponse{
		Response: &extProcV3.ProcessingResponse_RequestBody{
			RequestBody: &extProcV3.BodyResponse{Response: &extProcV3.CommonResponse{}},
		},
	}
}

// routeToAgent is the expected RequestHeaders mutation that routes an invocation
// to the resolved agent: :authority to the agent host, :path to the backend
// endpoint, and the internal MCP headers stripped.
func routeToAgent() *extProcV3.ProcessingResponse {
	return &extProcV3.ProcessingResponse{
		Response: &extProcV3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extProcV3.HeadersResponse{
				Response: &extProcV3.CommonResponse{
					HeaderMutation: &extProcV3.HeaderMutation{
						SetHeaders: []*corev3.HeaderValueOption{
							{Header: &corev3.HeaderValue{Key: ":authority", RawValue: []byte(testAgentHost)}},
							{Header: &corev3.HeaderValue{Key: ":path", RawValue: []byte(a2aBackendPath)}},
						},
						RemoveHeaders: routing.InternalOnlyHeaders,
					},
				},
			},
		},
	}
}

func TestProcess_A2AInvocationRouting(t *testing.T) {
	srv := newA2ATestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		a2aPostHeadersStep("/a2a/mcp-test/weather", []*extProcV3.ProcessingResponse{
			routeToAgent(),
		}),
		a2aBodyStep(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{}}}`, []*extProcV3.ProcessingResponse{
			doNothingBody(),
		}),
		responseHeadersStep(),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}

func TestProcess_A2AStreamingObserved(t *testing.T) {
	srv := newA2ATestServer(t)

	// a streamed SSE response: the observer reads it, the body is forwarded unchanged
	sseBody := `data: {"jsonrpc":"2.0","id":1,"result":{"task":{"id":"a2a-task-1","contextId":"a2a-ctx-1","status":{"state":"TASK_STATE_SUBMITTED"}}}}` + "\n" +
		`data: {"jsonrpc":"2.0","id":1,"result":{"statusUpdate":{"taskId":"a2a-task-1","contextId":"a2a-ctx-1","status":{"state":"TASK_STATE_COMPLETED"}}}}` + "\n"

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		a2aPostHeadersStep("/a2a/mcp-test/weather", []*extProcV3.ProcessingResponse{
			routeToAgent(),
		}),
		a2aBodyStep(`{"jsonrpc":"2.0","id":1,"method":"SendStreamingMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"go"}]}}}`, []*extProcV3.ProcessingResponse{
			doNothingBody(),
		}),
		// status 200 → the a2a branch keeps the stream open with a ModeOverride
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_ResponseHeaders{
					ResponseHeaders: &extProcV3.HttpHeaders{
						Headers: &corev3.HeaderMap{
							Headers: []*corev3.HeaderValue{{Key: ":status", RawValue: []byte("200")}},
						},
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				{Response: &extProcV3.ProcessingResponse_ResponseHeaders{ResponseHeaders: &extProcV3.HeadersResponse{}}},
			},
		},
		// the SSE body is forwarded byte-for-byte — observation never mutates it
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_ResponseBody{
					ResponseBody: &extProcV3.HttpBody{Body: []byte(sseBody), EndOfStream: true},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_ResponseBody{
						ResponseBody: &extProcV3.BodyResponse{
							Response: &extProcV3.CommonResponse{
								BodyMutation: &extProcV3.BodyMutation{
									Mutation: &extProcV3.BodyMutation_Body{Body: []byte(sseBody)},
								},
							},
						},
					},
				},
			},
		},
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}

func TestProcess_A2AUnknownAgent(t *testing.T) {
	srv := newA2ATestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		a2aPostHeadersStep("/a2a/mcp-test/unknown", []*extProcV3.ProcessingResponse{
			immediateJSONResponse(typev3.StatusCode_OK),
		}),
		responseHeadersStep(),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}

func TestProcess_A2AUnsupportedMethod(t *testing.T) {
	srv := newA2ATestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		a2aPostHeadersStep("/a2a/mcp-test/weather", []*extProcV3.ProcessingResponse{
			routeToAgent(),
		}),
		a2aBodyStep(`{"jsonrpc":"2.0","id":1,"method":"ListTasks"}`, []*extProcV3.ProcessingResponse{
			immediateJSONResponse(typev3.StatusCode_OK),
		}),
		responseHeadersStep(),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}

func TestProcess_A2AEmbeddedPushRejected(t *testing.T) {
	srv := newA2ATestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		a2aPostHeadersStep("/a2a/mcp-test/weather", []*extProcV3.ProcessingResponse{
			routeToAgent(),
		}),
		a2aBodyStep(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"configuration":{"pushNotificationConfig":{"url":"https://evil"}}}}`, []*extProcV3.ProcessingResponse{
			immediateJSONResponse(typev3.StatusCode_OK),
		}),
		responseHeadersStep(),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}
