//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	goenv "github.com/caitlinelfring/go-env-default"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

// A2A discovery is served on the base HTTP listener whose public host (mcp.127-0-0-1.sslip.io)
// matches the a2a-test-server's advertised card URL. The card's host must equal the gateway's
// public host for fail-closed interface validation to pass, so these specs target that listener
// rather than the e2e suite's mcp-gateway.local HTTPS listener.
var a2aGatewayBaseURL = goenv.GetDefault("A2A_GATEWAY_URL", "http://mcp.127-0-0-1.sslip.io:8001")

const (
	// a2aAgentRouteName is the HTTPRoute for the deployed a2a-test-server (config/test-servers).
	a2aAgentRouteName = "a2a-server-route"
	// a2aAgentPrefix matches the path baked into the a2a-test-server's AGENT_URL
	// (/a2a/mcp-test/weather), so its served card validates against the gateway path.
	a2aAgentPrefix = "weather"
)

func a2aGet(path string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a2aGatewayBaseURL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// a2aCatalogHrefs fetches the API Catalog and returns the agent hrefs it advertises.
func a2aCatalogHrefs() []string {
	code, body := a2aGet("/.well-known/api-catalog")
	Expect(code).To(Equal(http.StatusOK))
	var doc struct {
		Linkset []struct {
			Item []struct {
				Href string `json:"href"`
			} `json:"item"`
		} `json:"linkset"`
	}
	Expect(json.Unmarshal([]byte(body), &doc)).To(Succeed())
	var hrefs []string
	for _, lc := range doc.Linkset {
		for _, it := range lc.Item {
			hrefs = append(hrefs, it.Href)
		}
	}
	return hrefs
}

func newA2ARegistration(name, prefix string) *mcpv1alpha1.A2AAgentRegistration {
	return &mcpv1alpha1.A2AAgentRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: TestServerNameSpace},
		Spec: mcpv1alpha1.A2AAgentRegistrationSpec{
			AgentPrefix: prefix,
			TargetRef: mcpv1alpha1.TargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  a2aAgentRouteName,
			},
		},
	}
}

func a2aHref(prefix string) string { return "/a2a/" + TestServerNameSpace + "/" + prefix }

func waitA2ARegistrationReady(name string) {
	Eventually(func(g Gomega) {
		got := &mcpv1alpha1.A2AAgentRegistration{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: TestServerNameSpace}, got)).To(Succeed())
		cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
		g.Expect(cond).NotTo(BeNil())
		g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
}

// A2A discovery mutates the shared broker config (registration triggers a config reload), so the
// suite is Serial. Each spec cleans up its registration and waits for the catalog to drain.
var _ = Describe("A2A Discovery", Ordered, Serial, func() {
	var testResources []client.Object

	AfterEach(func() {
		for _, r := range testResources {
			CleanupResource(ctx, k8sClient, r)
		}
		testResources = nil
		By("waiting for the catalog to drain the test agent")
		Eventually(a2aCatalogHrefs, TestTimeoutMedium, TestRetryInterval).
			ShouldNot(ContainElement(a2aHref(a2aAgentPrefix)))
	})

	It("[Happy,A2A] lists a registered agent in the API Catalog and serves its card verbatim", func() {
		reg := newA2ARegistration("e2e-a2a-weather", a2aAgentPrefix)
		testResources = append(testResources, reg)
		Expect(k8sClient.Create(ctx, reg)).To(Succeed())

		By("the registration reaching Ready")
		waitA2ARegistrationReady("e2e-a2a-weather")

		By("the agent entering the catalog once its card is fetched and validated")
		Eventually(a2aCatalogHrefs, TestTimeoutLong, TestRetryInterval).
			Should(ContainElement(a2aHref(a2aAgentPrefix)))

		By("serving the agent card verbatim (advertising the gateway path, not rewritten)")
		code, card := a2aGet(a2aHref(a2aAgentPrefix) + "/.well-known/agent-card.json")
		Expect(code).To(Equal(http.StatusOK))
		// the broker serves the upstream card byte-for-byte, so the card still carries the exact
		// gateway URL the agent advertised — proof the card was not mutated in transit
		Expect(card).To(ContainSubstring(a2aGatewayBaseURL + a2aHref(a2aAgentPrefix)))
	})

	It("[Security,A2A] fails closed when a card's advertised path does not match the registration", func() {
		// the a2a-test-server advertises /a2a/mcp-test/weather; registering it under a different
		// prefix means the served card's path no longer resolves to this agent's gateway path, so
		// interface validation must reject it — the agent is neither cataloged nor served.
		reg := newA2ARegistration("e2e-a2a-mismatch", "notweather")
		testResources = append(testResources, reg)
		Expect(k8sClient.Create(ctx, reg)).To(Succeed())
		waitA2ARegistrationReady("e2e-a2a-mismatch")

		By("the mismatched agent never entering the catalog")
		Consistently(a2aCatalogHrefs, 20*time.Second, TestRetryInterval).
			ShouldNot(ContainElement(a2aHref("notweather")))

		By("the card endpoint failing closed")
		code, _ := a2aGet(a2aHref("notweather") + "/.well-known/agent-card.json")
		Expect(code).To(Equal(http.StatusServiceUnavailable))
	})

	It("[A2A] removes the agent from the API Catalog on deregistration", func() {
		reg := newA2ARegistration("e2e-a2a-dereg", a2aAgentPrefix)
		testResources = append(testResources, reg)
		Expect(k8sClient.Create(ctx, reg)).To(Succeed())
		waitA2ARegistrationReady("e2e-a2a-dereg")
		Eventually(a2aCatalogHrefs, TestTimeoutLong, TestRetryInterval).
			Should(ContainElement(a2aHref(a2aAgentPrefix)))

		By("deleting the registration")
		Expect(k8sClient.Delete(ctx, reg)).To(Succeed())
		Eventually(a2aCatalogHrefs, TestTimeoutMedium, TestRetryInterval).
			ShouldNot(ContainElement(a2aHref(a2aAgentPrefix)))
	})

	It("[Happy,A2A] leaves MCP tool discovery unaffected while an A2A agent is registered", func() {
		reg := newA2ARegistration("e2e-a2a-additive", a2aAgentPrefix)
		testResources = append(testResources, reg)
		Expect(k8sClient.Create(ctx, reg)).To(Succeed())
		waitA2ARegistrationReady("e2e-a2a-additive")
		Eventually(a2aCatalogHrefs, TestTimeoutLong, TestRetryInterval).
			Should(ContainElement(a2aHref(a2aAgentPrefix)))

		By("MCP tools/list still succeeding through the same gateway")
		mcpClient, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(string) {})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = mcpClient.Close() }()
		Eventually(func(g Gomega) {
			tools, err := mcpClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tools).NotTo(BeNil())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})
})
