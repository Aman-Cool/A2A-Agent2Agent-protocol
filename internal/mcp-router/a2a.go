package mcprouter

// a2a request-side routing: classify discovery (GET card/catalog, passthrough to
// the broker) vs invocation (POST JSON-RPC, routed to the resolved agent), resolve
// the agent from the namespace-qualified path, and classify the JSON-RPC method.
// task ids pass through unchanged — the gateway never rewrites them; fail-closed
// ownership binding is a later phase. see docs/design/a2a/a2a-design.md.

import (
	"encoding/json"
	"strings"
)

const (
	a2aPathPrefix = "/a2a/"
	// a2aBackendPath is the upstream A2A JSON-RPC endpoint. The registration
	// endpoint carries no path (it is protocol-defined), so the router rewrites the
	// public /a2a/{namespace}/{prefix} path to this fixed backend path.
	a2aBackendPath = "/a2a"
)

// v1.0 JSON-RPC method names the gateway routes (a2a.proto service methods).
const (
	a2aMethodSendMessage     = "SendMessage"
	a2aMethodSendStreaming   = "SendStreamingMessage"
	a2aMethodGetTask         = "GetTask"
	a2aMethodCancelTask      = "CancelTask"
	a2aMethodSubscribeToTask = "SubscribeToTask"
)

// JSON-RPC error codes: standard (-32700, -32602) plus A2A spec §8.2 (-32001..-32007).
const (
	a2aErrParse            = -32700 // request body is not valid JSON-RPC
	a2aErrInvalidParams    = -32602 // path resolves to no registered agent
	a2aErrPushNotSupported = -32003 // request embeds a push-notification config
	a2aErrUnsupportedOp    = -32004 // method is not routed by this gateway
)

// a2aState is per-stream state for an in-flight a2a request, kept separate from
// mcpRequest so the two protocols never share parsing state.
type a2aState struct {
	discovery bool   // GET card/catalog fetch: passed through to the broker
	agent     string // resolved agent name (invocation only), for observation attribution
	method    string // v1 JSON-RPC method (invocation only)
	streaming bool
}

func isA2APath(path string) bool {
	return strings.HasPrefix(path, a2aPathPrefix)
}

func isSupportedA2AMethod(method string) bool {
	switch method {
	case a2aMethodSendMessage, a2aMethodSendStreaming, a2aMethodGetTask, a2aMethodCancelTask, a2aMethodSubscribeToTask:
		return true
	}
	return false
}

func isA2AStreamingMethod(method string) bool {
	return method == a2aMethodSendStreaming || method == a2aMethodSubscribeToTask
}

// resolveA2APath splits "/a2a/{namespace}/{prefix}" into its namespace and agent
// prefix. Only the first two segments after the /a2a/ prefix identify the agent;
// trailing segments and any query string are ignored.
func resolveA2APath(path string) (namespace, prefix string, ok bool) {
	rest := strings.TrimPrefix(path, a2aPathPrefix)
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest = rest[:i]
	}
	segments := strings.Split(strings.Trim(rest, "/"), "/")
	if len(segments) < 2 || segments[0] == "" || segments[1] == "" {
		return "", "", false
	}
	return segments[0], segments[1], true
}

// a2aRequest is the envelope-only view of a JSON-RPC request. params is decoded
// only far enough to detect an embedded push-notification config; message parts
// and everything else stay raw.
type a2aRequest struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params struct {
		Configuration struct {
			PushNotificationConfig json.RawMessage `json:"pushNotificationConfig"`
		} `json:"configuration"`
	} `json:"params"`
}

// parseA2ARequest reads the json-rpc envelope. hasPush reports an embedded
// push-notification config, which the gateway rejects rather than forwards.
func parseA2ARequest(body []byte) (method string, id json.RawMessage, hasPush bool, err error) {
	var req a2aRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil, false, err
	}
	return req.Method, req.ID, len(req.Params.Configuration.PushNotificationConfig) > 0, nil
}

type a2aErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type a2aErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   a2aErrorObject  `json:"error"`
}

// a2aErrorBody builds a JSON-RPC error envelope, echoing the request id when known.
func a2aErrorBody(id json.RawMessage, code int, message string) string {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body, err := json.Marshal(a2aErrorResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   a2aErrorObject{Code: code, Message: message},
	})
	if err != nil {
		return `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`
	}
	return string(body)
}
