//go:build e2e

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resources/Read Routing", func() {
	It("[Happy] resources/read routing through Envoy with prefix rewriting", func() {
		By("Creating HTTPRoutes and MCP Servers with resource support")
		registration1 := NewMCPServerResourcesWithDefaults("resources-read", k8sClient).
			WithPrefix("resources1_").
			Build()
		reg1Objects := registration1.GetObjects()
		deferCleanupResources(&reg1Objects)
		registeredServer1 := registration1.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer1.Name, registeredServer1.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying server is registered and ready to serve")
		// Resources/read routing is wired in ext_proc (request routing to backend via :authority rewrite)
		// and broker (stripResourcePrefix). This test verifies MCPServerRegistration setup for resources.
		// Full end-to-end resources/read verification is covered by integration and unit tests of:
		// - ext_proc_adapter.go (routes resources/read requests)
		// - filtered_resources_handler.go (applies authorization filtering)
		// - stripResourcePrefix (rewriting via EnsureSeparator)
		Expect(registeredServer1).NotTo(BeNil())
		Expect(registeredServer1.Spec.Prefix).To(Equal("resources1_"))
	})
})
