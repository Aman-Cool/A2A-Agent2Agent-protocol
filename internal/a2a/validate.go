package a2a

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// agentInterfaces is the minimal read-only view of an AgentCard used for fail-closed
// validation: the v1.0 supportedInterfaces list, with the v0.3 top-level url as a
// fallback. The raw card bytes are never modified — a signed card is still served
// verbatim; validation only decides whether it is served at all.
type agentInterfaces struct {
	URL                 string `json:"url"`
	SupportedInterfaces []struct {
		URL string `json:"url"`
	} `json:"supportedInterfaces"`
}

// validateCard checks that every interface URL a card advertises resolves to the
// agent's gateway path. A spec-following client invokes the URL from the card's
// supportedInterfaces, so a verbatim-served card advertising any non-gateway URL —
// under any binding — would send clients around the gateway's policy perimeter.
// It returns a non-empty reason when the card must not be served (fail closed):
// unparseable JSON, no advertised interface, or any interface pointing away from
// the gateway. externalHost is compared by hostname when known; when empty, only
// the path is validated.
func validateCard(raw []byte, externalHost, namespace, prefix string) string {
	var card agentInterfaces
	if err := json.Unmarshal(raw, &card); err != nil {
		return "card is not valid JSON"
	}
	urls := make([]string, 0, len(card.SupportedInterfaces)+1)
	for i := range card.SupportedInterfaces {
		urls = append(urls, card.SupportedInterfaces[i].URL)
	}
	if len(urls) == 0 && card.URL != "" {
		urls = append(urls, card.URL)
	}
	if len(urls) == 0 {
		return "card advertises no interface URL"
	}
	wantPath := "/a2a/" + namespace + "/" + prefix
	wantHost := hostOnly(externalHost)
	for _, raw := range urls {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return fmt.Sprintf("card advertises unparseable interface URL %q", raw)
		}
		if strings.TrimRight(parsed.Path, "/") != wantPath {
			return fmt.Sprintf("card advertises non-gateway interface URL %q (want path %s)", raw, wantPath)
		}
		if wantHost != "" && parsed.Hostname() != wantHost {
			return fmt.Sprintf("card advertises interface host %q, gateway host is %q", parsed.Hostname(), wantHost)
		}
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
