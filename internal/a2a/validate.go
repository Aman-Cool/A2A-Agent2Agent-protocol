package a2a

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// agentInterfaces is the minimal read-only view of an AgentCard used for fail-closed
// validation: the v1.0 supportedInterfaces list (url + protocolBinding + protocolVersion),
// with the v0.3 top-level url as a fallback. The raw card bytes are never modified — a signed
// card is still served verbatim; validation only decides whether it is served at all.
type agentInterfaces struct {
	URL                 string           `json:"url"`
	SupportedInterfaces []agentInterface `json:"supportedInterfaces"`
}

type agentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

// validateCard checks that every interface a card advertises is one a stock client can only
// use to reach the gateway. A spec-following client selects a declared interface and invokes
// its url with its binding, so a verbatim-served card must not advertise any interface that
// would take a client off the gateway or onto a transport the gateway does not front. It
// returns a non-empty reason when the card must not be served (fail closed):
//
//   - unparseable JSON, or no advertised interface;
//   - a URL that is not http(s), not the agent's gateway path, or (when the gateway host is
//     known) not the gateway host;
//   - a binding other than JSONRPC — the gateway fronts only JSONRPC, so a GRPC or HTTP+JSON
//     interface (which the reference client's transport selection would pick) is a bypass;
//   - a protocolVersion that is not a v1 major — the router is v1-specific.
//
// externalHost is compared by hostname when known; when empty, host is not checked. Port and
// scheme-is-https are the gateway listener's concern (the broker does not know its own external
// scheme or port), so only the http(s) family is enforced here.
func validateCard(raw []byte, externalHost, namespace, prefix string) string {
	var card agentInterfaces
	if err := json.Unmarshal(raw, &card); err != nil {
		return "card is not valid JSON"
	}
	ifaces := card.SupportedInterfaces
	if len(ifaces) == 0 && card.URL != "" {
		ifaces = []agentInterface{{URL: card.URL}}
	}
	if len(ifaces) == 0 {
		return "card advertises no interface URL"
	}
	wantPath := "/a2a/" + namespace + "/" + prefix
	wantHost := hostOnly(externalHost)
	for i := range ifaces {
		if reason := validateInterface(ifaces[i], wantPath, wantHost); reason != "" {
			return reason
		}
	}
	return ""
}

func validateInterface(iface agentInterface, wantPath, wantHost string) string {
	parsed, err := url.Parse(iface.URL)
	if err != nil || parsed.Host == "" {
		return fmt.Sprintf("card advertises unparseable interface URL %q", iface.URL)
	}
	if s := strings.ToLower(parsed.Scheme); s != "http" && s != "https" {
		return fmt.Sprintf("card advertises non-http(s) interface URL %q", iface.URL)
	}
	if strings.TrimRight(parsed.Path, "/") != wantPath {
		return fmt.Sprintf("card advertises non-gateway interface URL %q (want path %s)", iface.URL, wantPath)
	}
	if wantHost != "" && parsed.Hostname() != wantHost {
		return fmt.Sprintf("card advertises interface host %q, gateway host is %q", parsed.Hostname(), wantHost)
	}
	// empty binding defaults to JSONRPC (the preferred transport); anything else is a bypass
	// onto a transport the gateway does not serve.
	if b := iface.ProtocolBinding; b != "" && !strings.EqualFold(b, "JSONRPC") {
		return fmt.Sprintf("card advertises interface with unsupported binding %q (gateway fronts JSONRPC only)", b)
	}
	if v := iface.ProtocolVersion; v != "" && v != "1" && !strings.HasPrefix(v, "1.") {
		return fmt.Sprintf("card advertises interface protocolVersion %q, gateway is v1-specific", v)
	}
	return ""
}

// hostOnly strips an optional :port from a configured public host, so host
// comparison is by hostname regardless of how publicHost was written.
func hostOnly(h string) string {
	if h == "" {
		return ""
	}
	if u, err := url.Parse("//" + h); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return h
}
