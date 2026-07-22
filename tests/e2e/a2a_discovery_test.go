//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

// A2A discovery is served on the same gateway the MCP e2e suite uses; catalog and card endpoints
// live under the broker's HTTP server, reached at the gateway base URL (gatewayURL without /mcp).
// The card's host must equal the broker's public host for fail-closed interface validation to pass,
// so BeforeAll re-points the a2a-test-server's advertised card URL to that host (gatewayPublicHost).
var a2aGatewayBaseURL = strings.TrimSuffix(gatewayURL, "/mcp")

const (
	// a2aAgentRouteName is a test-owned HTTPRoute to the a2a-test-server. The suite's BeforeSuite
	// wipes HTTPRoutes in the test namespace, so the pre-deployed a2a-server-route is gone; the
	// a2a-test-server Deployment and Service survive, so the test recreates its own route to that
	// Service (mirroring how the MCP e2e tests build their own routes).
	a2aAgentRouteName = "e2e-a2a-server-route"
	a2aAgentSvcName   = "a2a-test-server"
	a2aAgentSvcPort   = 9090
	// a2aAgentHostname must match the listener the default MCPGatewayExtension targets so the
	// controller writes config for the route (httpRouteAttachesToListener matches by port); that
	// listener is *.mcp-gateway.local, the same pattern the MCP e2e routes use.
	a2aAgentHostname = "a2a-server.mcp-gateway.local"
	// a2aAgentPrefix is the agent's routing prefix; the card must advertise the matching gateway path.
	a2aAgentPrefix = "weather"
)

// a2aAgentCardURL is the card URL the a2a-test-server is configured to advertise: the gateway's
// public host plus the agent's gateway path, so it validates against both.
func a2aAgentCardURL() string {
	return "http://" + gatewayPublicHost + a2aHref(a2aAgentPrefix)
}

func a2aHref(prefix string) string { return "/a2a/" + TestServerNameSpace + "/" + prefix }

func a2aGet(path string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a2aGatewayBaseURL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	resp, err := getMCPHTTPClient().Do(req)
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

func newA2AHTTPRoute() *gatewayapiv1.HTTPRoute {
	pathPrefix := gatewayapiv1.PathMatchPathPrefix
	slash := "/"
	port := gatewayapiv1.PortNumber(a2aAgentSvcPort)
	gwNs := gatewayapiv1.Namespace(GatewayNamespace)
	return &gatewayapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: a2aAgentRouteName, Namespace: TestServerNameSpace},
		Spec: gatewayapiv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayapiv1.CommonRouteSpec{
				ParentRefs: []gatewayapiv1.ParentReference{{
					Name:      gatewayapiv1.ObjectName(GatewayName),
					Namespace: &gwNs,
				}},
			},
			Hostnames: []gatewayapiv1.Hostname{a2aAgentHostname},
			Rules: []gatewayapiv1.HTTPRouteRule{{
				Matches: []gatewayapiv1.HTTPRouteMatch{{
					Path: &gatewayapiv1.HTTPPathMatch{Type: &pathPrefix, Value: &slash},
				}},
				BackendRefs: []gatewayapiv1.HTTPBackendRef{{
					BackendRef: gatewayapiv1.BackendRef{
						BackendObjectReference: gatewayapiv1.BackendObjectReference{
							Name: gatewayapiv1.ObjectName(a2aAgentSvcName),
							Port: &port,
						},
					},
				}},
			}},
		},
	}
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

func waitA2ARegistrationReady(name string) {
	Eventually(func(g Gomega) {
		got := &mcpv1alpha1.A2AAgentRegistration{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: TestServerNameSpace}, got)).To(Succeed())
		cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
		g.Expect(cond).NotTo(BeNil())
		g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
}

// A2A discovery mutates the shared broker config (registration triggers a config reload) and the
// shared a2a-test-server deployment, so the suite is Serial.
var _ = Describe("A2A Discovery", Ordered, Serial, func() {
	var testResources []client.Object

	BeforeAll(func() {
		By("pointing the a2a-test-server's advertised card URL at the gateway public host")
		cmd := exec.CommandContext(ctx, "kubectl", "set", "env",
			"deployment/"+a2aAgentSvcName, "-n", TestServerNameSpace, "AGENT_URL="+a2aAgentCardURL())
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		Expect(WaitForDeploymentReady(ctx, TestServerNameSpace, a2aAgentSvcName)).To(Succeed())

		By("creating the HTTPRoute to the a2a-test-server (the suite wipes the pre-deployed one)")
		route := newA2AHTTPRoute()
		CleanupResource(ctx, k8sClient, route) // clear any leftover from a prior run
		Expect(k8sClient.Create(ctx, route)).To(Succeed())

		By("waiting for the route to be Accepted by the gateway")
		Eventually(func(g Gomega) {
			got := &gatewayapiv1.HTTPRoute{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: a2aAgentRouteName, Namespace: TestServerNameSpace}, got)).To(Succeed())
			g.Expect(got.Status.Parents).NotTo(BeEmpty())
			accepted := meta.FindStatusCondition(got.Status.Parents[0].Conditions, string(gatewayapiv1.RouteConditionAccepted))
			g.Expect(accepted).NotTo(BeNil())
			g.Expect(accepted.Status).To(Equal(metav1.ConditionTrue))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	AfterAll(func() {
		CleanupResource(ctx, k8sClient, newA2AHTTPRoute())
	})

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
		// the broker serves the upstream card byte-for-byte, so it still carries the exact URL the
		// agent advertised — proof the card was not mutated in transit
		Expect(card).To(ContainSubstring(a2aAgentCardURL()))
	})

	It("[Security,A2A] fails closed when a card's advertised path does not match the registration", func() {
		// the a2a-test-server advertises .../weather; registering it under a different prefix means
		// the served card's path no longer resolves to this agent's gateway path, so interface
		// validation must reject it — the agent is neither cataloged nor served.
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
