//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// a2aPost sends a JSON-RPC invocation to an agent's gateway path and returns the
// status code and body. It reuses the discovery suite's helpers (same package):
// a2aGatewayBaseURL, a2aHref, and the insecure gateway HTTP client.
func a2aPost(prefix, body string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a2aGatewayBaseURL+a2aHref(prefix), strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := getMCPHTTPClient().Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// a2aErrorCode extracts the JSON-RPC error code from a response body.
func a2aErrorCode(body string) int {
	var r struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	Expect(json.Unmarshal([]byte(body), &r)).To(Succeed())
	return r.Error.Code
}

// A2A invocation routes through the shared gateway to the shared a2a-test-server; registering
// the agent reloads the broker config, so the suite is Serial. The agent is registered once for
// the whole block (every spec needs it routable) rather than per-spec.
var _ = Describe("A2A Invocation", Ordered, Serial, func() {
	const invokeRegName = "e2e-a2a-invoke"
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
		CleanupResource(ctx, k8sClient, route)
		Expect(k8sClient.Create(ctx, route)).To(Succeed())
		testResources = append(testResources, route)

		By("waiting for the route to be Accepted by the gateway")
		Eventually(func(g Gomega) {
			got := &gatewayapiv1.HTTPRoute{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: a2aAgentRouteName, Namespace: TestServerNameSpace}, got)).To(Succeed())
			g.Expect(got.Status.Parents).NotTo(BeEmpty())
			accepted := meta.FindStatusCondition(got.Status.Parents[0].Conditions, string(gatewayapiv1.RouteConditionAccepted))
			g.Expect(accepted).NotTo(BeNil())
			g.Expect(accepted.Status).To(Equal(metav1.ConditionTrue))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("registering the weather agent so the router can resolve it")
		reg := newA2ARegistration(invokeRegName, a2aAgentPrefix)
		CleanupResource(ctx, k8sClient, reg)
		Expect(k8sClient.Create(ctx, reg)).To(Succeed())
		testResources = append(testResources, reg)
		waitA2ARegistrationReady(invokeRegName)

		By("the agent entering the catalog (config reloaded, agent resolvable)")
		Eventually(a2aCatalogHrefs, TestTimeoutLong, TestRetryInterval).
			Should(ContainElement(a2aHref(a2aAgentPrefix)))
	})

	AfterAll(func() {
		for i := len(testResources) - 1; i >= 0; i-- {
			CleanupResource(ctx, k8sClient, testResources[i])
		}
		testResources = nil
	})

	It("[Happy,A2A] routes SendMessage to the agent and returns a completed task", func() {
		code, body := a2aPost(a2aAgentPrefix,
			`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"e2e-m1","role":"ROLE_USER","parts":[{"text":"hello weather"}]}}}`)
		Expect(code).To(Equal(http.StatusOK))

		// the v1 SendMessageResponse is a oneof; a task result carries the agent-assigned id
		var r struct {
			Result struct {
				Task struct {
					ID     string `json:"id"`
					Status struct {
						State string `json:"state"`
					} `json:"status"`
				} `json:"task"`
			} `json:"result"`
		}
		Expect(json.Unmarshal([]byte(body), &r)).To(Succeed(), body)
		Expect(r.Result.Task.Status.State).To(Equal("TASK_STATE_COMPLETED"))
		// the task id is the agent's own, passed through unchanged (not gateway-minted)
		Expect(r.Result.Task.ID).To(HavePrefix("a2a-task-"))
	})

	It("[Security,A2A] rejects an unknown agent path with -32602", func() {
		code, body := a2aPost("nosuchagent",
			`{"jsonrpc":"2.0","id":2,"method":"SendMessage","params":{"message":{"messageId":"x","role":"ROLE_USER","parts":[{"text":"hi"}]}}}`)
		Expect(code).To(Equal(http.StatusOK))
		Expect(a2aErrorCode(body)).To(Equal(-32602))
	})

	It("[Security,A2A] rejects an unsupported method with -32004", func() {
		code, body := a2aPost(a2aAgentPrefix, `{"jsonrpc":"2.0","id":3,"method":"ListTasks","params":{}}`)
		Expect(code).To(Equal(http.StatusOK))
		Expect(a2aErrorCode(body)).To(Equal(-32004))
	})

	It("[Security,A2A] rejects an embedded push notification config with -32003", func() {
		code, body := a2aPost(a2aAgentPrefix,
			`{"jsonrpc":"2.0","id":4,"method":"SendMessage","params":{"message":{"messageId":"y","role":"ROLE_USER","parts":[{"text":"hi"}]},"configuration":{"pushNotificationConfig":{"url":"https://evil.example"}}}}`)
		Expect(code).To(Equal(http.StatusOK))
		Expect(a2aErrorCode(body)).To(Equal(-32003))
	})

	It("[A2A] streams SendStreamingMessage events through to completion", func() {
		// "slow" drives the test server through submitted -> working -> completed as SSE events
		code, body := a2aPost(a2aAgentPrefix,
			`{"jsonrpc":"2.0","id":5,"method":"SendStreamingMessage","params":{"message":{"messageId":"e2e-m5","role":"ROLE_USER","parts":[{"text":"slow"}]}}}`)
		Expect(code).To(Equal(http.StatusOK))
		// the stream is forwarded unmodified; the terminal event reaches the client
		Expect(body).To(ContainSubstring("data:"))
		Expect(body).To(ContainSubstring("TASK_STATE_COMPLETED"))
	})
})
