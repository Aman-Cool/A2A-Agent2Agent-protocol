//go:build integration

package controller

import (
	"context"
	"fmt"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// mockA2AConfigReaderWriter is a mock for testing
type mockA2AConfigReaderWriter struct {
	upsertedAgents map[string]config.A2AAgent
	removedAgents  []string
}

func newMockA2AConfigReaderWriter() *mockA2AConfigReaderWriter {
	return &mockA2AConfigReaderWriter{
		upsertedAgents: make(map[string]config.A2AAgent),
		removedAgents:  []string{},
	}
}

func (m *mockA2AConfigReaderWriter) UpsertA2AAgent(_ context.Context, agent config.A2AAgent, namespaceName types.NamespacedName) error {
	key := fmt.Sprintf("%s/%s", namespaceName.Namespace, agent.Name)
	m.upsertedAgents[key] = agent
	return nil
}

func (m *mockA2AConfigReaderWriter) RemoveA2AAgent(_ context.Context, agentName string) error {
	m.removedAgents = append(m.removedAgents, agentName)
	return nil
}

// createTestA2AAgentRegistration creates an A2AAgentRegistration for testing
func createTestA2AAgentRegistration(name, namespace, httpRouteName, prefix string) *mcpv1alpha1.A2AAgentRegistration {
	return &mcpv1alpha1.A2AAgentRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.A2AAgentRegistrationSpec{
			TargetRef: mcpv1alpha1.TargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  httpRouteName,
			},
			AgentPrefix: prefix,
		},
	}
}

// forceDeleteTestA2AAgentRegistration removes finalizers and deletes
func forceDeleteTestA2AAgentRegistration(ctx context.Context, name, namespace string) {
	nn := types.NamespacedName{Name: name, Namespace: namespace}
	resource := &mcpv1alpha1.A2AAgentRegistration{}
	err := testK8sClient.Get(ctx, nn, resource)
	if errors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())

	if controllerutil.ContainsFinalizer(resource, a2aGatewayFinalizer) {
		controllerutil.RemoveFinalizer(resource, a2aGatewayFinalizer)
		Expect(testK8sClient.Update(ctx, resource)).To(Succeed())
	}

	Expect(client.IgnoreNotFound(testK8sClient.Delete(ctx, resource))).To(Succeed())

	Eventually(func(g Gomega) {
		err := testK8sClient.Get(ctx, nn, resource)
		g.Expect(errors.IsNotFound(err)).To(BeTrue())
	}, testTimeout, testRetryInterval).Should(Succeed())
}

// newA2AReconciler creates an A2AReconciler for testing
func newA2AReconciler(configWriter *mockA2AConfigReaderWriter) *A2AReconciler {
	return &A2AReconciler{
		Client:             testIndexedClient,
		Scheme:             testK8sClient.Scheme(),
		DirectAPIReader:    testK8sClient,
		ConfigReaderWriter: configWriter,
		MCPExtFinderValidator: &MCPGatewayExtensionValidator{
			Client:          testIndexedClient,
			DirectAPIReader: testK8sClient,
			Logger:          slog.New(slog.NewTextHandler(GinkgoWriter, &slog.HandlerOptions{Level: slog.LevelDebug})),
		},
	}
}

// waitForA2ARegistrationCacheSync waits for cache to see the resource
func waitForA2ARegistrationCacheSync(ctx context.Context, nn types.NamespacedName) {
	Eventually(func(g Gomega) {
		cached := &mcpv1alpha1.A2AAgentRegistration{}
		g.Expect(testIndexedClient.Get(ctx, nn, cached)).To(Succeed())
	}, testTimeout, testRetryInterval).Should(Succeed())
}

// reconcileA2AUntil re-reconciles until probe passes: the first pass adds the finalizer
// and requeues, and stale-cache reads (finalizer, extension Ready status) resolve across
// retries. A clean-but-unsuccessful pass (e.g. extension not yet visible in the cache)
// is retried rather than accepted, which is why settling on "no requeue" is not enough.
func reconcileA2AUntil(ctx context.Context, reconciler *A2AReconciler, nn types.NamespacedName, probe func(g Gomega)) {
	Eventually(func(g Gomega) {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		g.Expect(err).NotTo(HaveOccurred())
		probe(g)
	}, testTimeout, testRetryInterval).Should(Succeed())
}

var _ = Describe("A2AAgentRegistration Controller", func() {
	const (
		resourceName  = "test-a2areg"
		httpRouteName = "test-a2a-route"
		gatewayName   = "test-a2a-gw"
		serviceName   = "test-a2a-svc"
		extName       = "test-a2a-ext"
	)

	ctx := context.Background()

	a2aNamespacedName := types.NamespacedName{
		Name:      resourceName,
		Namespace: "default",
	}

	setupGatewayFixtures := func() {
		gw := createTestGateway(gatewayName, "default", "a2a-test.mcp.local")
		Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

		svc := createTestService(serviceName, "default", 9090)
		Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

		httpRoute := createTestHTTPRoute(httpRouteName, "default", "a2a-test.mcp.local", serviceName, 9090, gatewayName, "default")
		Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())

		Eventually(func(g Gomega) {
			route := &gatewayv1.HTTPRoute{}
			g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: httpRouteName, Namespace: "default"}, route)).To(Succeed())
			g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
		}, testTimeout, testRetryInterval).Should(Succeed())

		mcpExt := createTestMCPGatewayExtension(extName, "default", gatewayName, "default")
		Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

		Eventually(func(g Gomega) {
			ext := &mcpv1alpha1.MCPGatewayExtension{}
			g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
			ext.SetReadyCondition(metav1.ConditionTrue, mcpv1alpha1.ConditionReasonSuccess, "ready")
			g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
		}, testTimeout, testRetryInterval).Should(Succeed())
	}

	teardownGatewayFixtures := func() {
		forceDeleteTestA2AAgentRegistration(ctx, resourceName, "default")
		forceDeleteTestMCPGatewayExtension(ctx, extName, "default")
		deleteTestHTTPRoute(ctx, httpRouteName, "default")
		deleteTestService(ctx, serviceName, "default")
		deleteTestGateway(ctx, gatewayName, "default")
	}

	Context("When reconciling a valid registration", func() {
		BeforeEach(setupGatewayFixtures)
		AfterEach(teardownGatewayFixtures)

		It("should add the a2a finalizer on first reconcile", func() {
			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "weather")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: a2aNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, a2aNamespacedName, updated)).To(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(updated, a2aGatewayFinalizer)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should write agent config and set Ready=True", func() {
			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "weather")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			})

			// config written to the extension's namespace with the derived endpoint
			key := fmt.Sprintf("default/%s", a2aAgentName(&mcpv1alpha1.A2AAgentRegistration{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			}))
			agent, ok := configWriter.upsertedAgents[key]
			Expect(ok).To(BeTrue(), "expected agent config upserted for %s, got %v", key, configWriter.upsertedAgents)
			Expect(agent.AgentPrefix).To(Equal("weather"))
			Expect(agent.URL).To(Equal(fmt.Sprintf("http://%s.default.svc.cluster.local:9090", serviceName)))
			Expect(agent.Hostname).To(Equal("a2a-test.mcp.local"))

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, a2aNamespacedName, updated)).To(Succeed())
				ready := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should reject a duplicate agentPrefix in the same namespace with reason PrefixCollision", func() {
			// the winner is created first and named lexicographically smaller, so it owns the
			// prefix under both the creationTimestamp rule and the name tie-break
			const winnerName, loserName = "a-collide-reg", "z-collide-reg"
			winnerNN := types.NamespacedName{Name: winnerName, Namespace: "default"}
			loserNN := types.NamespacedName{Name: loserName, Namespace: "default"}

			Expect(testK8sClient.Create(ctx, createTestA2AAgentRegistration(winnerName, "default", httpRouteName, "weather"))).To(Succeed())
			Expect(testK8sClient.Create(ctx, createTestA2AAgentRegistration(loserName, "default", httpRouteName, "weather"))).To(Succeed())
			DeferCleanup(func() {
				forceDeleteTestA2AAgentRegistration(ctx, winnerName, "default")
				forceDeleteTestA2AAgentRegistration(ctx, loserName, "default")
			})

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, winnerNN)
			waitForA2ARegistrationCacheSync(ctx, loserNN)

			// winner: config written, Ready=True
			reconcileA2AUntil(ctx, reconciler, winnerNN, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).To(HaveKey(fmt.Sprintf("default/default/%s", winnerName)))
			})

			// loser: Ready=False with reason PrefixCollision, and no config written for it
			reconcileA2AUntil(ctx, reconciler, loserNN, func(g Gomega) {
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, loserNN, updated)).To(Succeed())
				ready := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(ready.Reason).To(Equal(conditionReasonPrefixCollision))
			})
			Expect(configWriter.upsertedAgents).NotTo(HaveKey(fmt.Sprintf("default/default/%s", loserName)))
		})

		It("should fan out config to every valid extension namespace and remove from all on deletion", func() {
			const nsTwo = "a2a-fanout-ns-two"
			const extTwoName = "test-a2a-ext-two"
			const grantName = "allow-ext-two-to-gw"

			// a second MCPGatewayExtension in another namespace, targeting the same gateway
			// cross-namespace — permitted by a ReferenceGrant in the gateway's namespace and Ready
			if err := testK8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsTwo}}); err != nil {
				Expect(errors.IsAlreadyExists(err)).To(BeTrue())
			}
			Expect(testK8sClient.Create(ctx, createTestExtReferenceGrant(grantName, "default", nsTwo))).To(Succeed())
			Expect(testK8sClient.Create(ctx, createTestMCPGatewayExtension(extTwoName, nsTwo, gatewayName, "default"))).To(Succeed())
			Eventually(func(g Gomega) {
				ext := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extTwoName, Namespace: nsTwo}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1alpha1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
			DeferCleanup(func() {
				forceDeleteTestMCPGatewayExtension(ctx, extTwoName, nsTwo)
				_ = testK8sClient.Delete(ctx, createTestExtReferenceGrant(grantName, "default", nsTwo))
			})

			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "weather")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			agentName := a2aAgentName(&mcpv1alpha1.A2AAgentRegistration{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			})

			// config fans out to BOTH extension namespaces
			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).To(HaveKey(fmt.Sprintf("default/%s", agentName)))
				g.Expect(configWriter.upsertedAgents).To(HaveKey(fmt.Sprintf("%s/%s", nsTwo, agentName)))
			})

			// deletion removes the agent cluster-wide (both namespaces' config secrets)
			Expect(testK8sClient.Delete(ctx, a2areg)).To(Succeed())
			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.removedAgents).To(ContainElement(agentName))
			})
		})

		It("should derive an https://host:443 endpoint with no path for an Istio Hostname backend", func() {
			const routeName = "a2a-external-route"
			const externalHost = "external-agent.example.com"

			route := createTestA2AHostnameRoute(routeName, "default", "a2a-test.mcp.local", externalHost, 443, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, route)).To(Succeed())
			Eventually(func(g Gomega) {
				fetched := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: routeName, Namespace: "default"}, fetched)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, fetched, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
			DeferCleanup(func() { deleteTestHTTPRoute(ctx, routeName, "default") })

			a2areg := createTestA2AAgentRegistration(resourceName, "default", routeName, "external")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			key := fmt.Sprintf("default/%s", a2aAgentName(&mcpv1alpha1.A2AAgentRegistration{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			}))
			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).To(HaveKey(key))
			})

			// A2A calls buildServerInfoFromHTTPRoute with an empty path, so a Hostname backend
			// yields https://<host>:443 with no trailing path (the agent-specific case)
			agent := configWriter.upsertedAgents[key]
			Expect(agent.URL).To(Equal(fmt.Sprintf("https://%s:443", externalHost)))
			Expect(agent.Hostname).To(Equal("a2a-test.mcp.local"))
		})

		It("should call RemoveA2AAgent and drop the finalizer on deletion", func() {
			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "weather")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			})

			resource := &mcpv1alpha1.A2AAgentRegistration{}
			Expect(testK8sClient.Get(ctx, a2aNamespacedName, resource)).To(Succeed())
			Expect(testK8sClient.Delete(ctx, resource)).To(Succeed())

			Eventually(func(g Gomega) {
				cached := &mcpv1alpha1.A2AAgentRegistration{}
				err := testIndexedClient.Get(ctx, a2aNamespacedName, cached)
				if errors.IsNotFound(err) {
					return
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cached.DeletionTimestamp).NotTo(BeNil())
			}, testTimeout, testRetryInterval).Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: a2aNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(configWriter.removedAgents).To(ContainElement("default/" + resourceName))

			Eventually(func(g Gomega) {
				deleted := &mcpv1alpha1.A2AAgentRegistration{}
				err := testK8sClient.Get(ctx, a2aNamespacedName, deleted)
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("cleans up config on deletion even when the target route is already gone", func() {
			// deletion keys on namespace/name and scrubs every managed config secret, so
			// it must not depend on resolving targetRef — otherwise deleting the route first
			// would strand the config with no way to find where it was written. This pins
			// that resilience against a future "resolve targetRef then clean up" refactor.
			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "weather")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			})

			// tear the whole target context down BEFORE deleting the registration
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			forceDeleteTestMCPGatewayExtension(ctx, extName, "default")
			deleteTestGateway(ctx, gatewayName, "default")

			resource := &mcpv1alpha1.A2AAgentRegistration{}
			Expect(testK8sClient.Get(ctx, a2aNamespacedName, resource)).To(Succeed())
			Expect(testK8sClient.Delete(ctx, resource)).To(Succeed())

			Eventually(func(g Gomega) {
				cached := &mcpv1alpha1.A2AAgentRegistration{}
				err := testIndexedClient.Get(ctx, a2aNamespacedName, cached)
				if errors.IsNotFound(err) {
					return
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cached.DeletionTimestamp).NotTo(BeNil())
			}, testTimeout, testRetryInterval).Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: a2aNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// cleanup still happened, keyed on namespace/name, target absence notwithstanding
			Expect(configWriter.removedAgents).To(ContainElement("default/" + resourceName))

			Eventually(func(g Gomega) {
				deleted := &mcpv1alpha1.A2AAgentRegistration{}
				err := testK8sClient.Get(ctx, a2aNamespacedName, deleted)
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should set Ready=False with reason Disabled when state is Disabled", func() {
			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "weather")
			a2areg.Spec.State = mcpv1alpha1.ServerStateDisabled
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			})

			// config is still written (state travels in the config), status reflects disabled
			Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, a2aNamespacedName, updated)).To(Succeed())
				ready := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(ready.Reason).To(Equal(conditionReasonDisabled))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When the target HTTPRoute does not exist", func() {
		AfterEach(func() {
			forceDeleteTestA2AAgentRegistration(ctx, resourceName, "default")
		})

		It("should set Ready=False", func() {
			a2areg := createTestA2AAgentRegistration(resourceName, "default", "missing-route", "weather")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			// first pass adds the finalizer and requeues; a later pass fails on the missing route
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: a2aNamespacedName})
				g.Expect(err).To(HaveOccurred())
			}, testTimeout, testRetryInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, a2aNamespacedName, updated)).To(Succeed())
				ready := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(ready.Reason).To(Equal(conditionReasonNotReady))
			}, testTimeout, testRetryInterval).Should(Succeed())
			Expect(configWriter.upsertedAgents).To(BeEmpty())
		})
	})

	Context("When the registration has a credentialRef", func() {
		const secretName = "a2a-agent-secret"

		BeforeEach(setupGatewayFixtures)
		AfterEach(func() {
			teardownGatewayFixtures()
			_ = client.IgnoreNotFound(testK8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
			}))
		})

		createSecret := func(labeled bool, key string) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: "default",
				},
				Data: map[string][]byte{
					key: []byte("Bearer agent-token"),
				},
			}
			if labeled {
				secret.Labels = map[string]string{"mcp.kuadrant.io/secret": "true"}
			}
			Expect(testK8sClient.Create(ctx, secret)).To(Succeed())
		}

		newRegistrationWithCredential := func() *mcpv1alpha1.A2AAgentRegistration {
			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "weather")
			a2areg.Spec.CredentialRef = &mcpv1alpha1.SecretReference{Name: secretName, Key: "token"}
			return a2areg
		}

		It("should include the credential in config when the labeled secret exists", func() {
			createSecret(true, "token")
			Expect(testK8sClient.Create(ctx, newRegistrationWithCredential())).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			})
			for _, agent := range configWriter.upsertedAgents {
				Expect(agent.Credential).To(Equal("Bearer agent-token"))
			}
		})

		It("updates broker config when the referenced credential secret value changes", func() {
			createSecret(true, "token")
			Expect(testK8sClient.Create(ctx, newRegistrationWithCredential())).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			})
			for _, agent := range configWriter.upsertedAgents {
				Expect(agent.Credential).To(Equal("Bearer agent-token"))
			}

			// rotate the credential; in production the secret watch (label-predicated,
			// mapped via findA2AAgentRegistrationsForSecret) triggers this reconcile
			secret := &corev1.Secret{}
			Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secret)).To(Succeed())
			secret.Data["token"] = []byte("Bearer rotated-token")
			Expect(testK8sClient.Update(ctx, secret)).To(Succeed())

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				for _, agent := range configWriter.upsertedAgents {
					g.Expect(agent.Credential).To(Equal("Bearer rotated-token"))
				}
			})
		})

		It("should set Ready=False when the secret is missing the required label", func() {
			createSecret(false, "token")
			Expect(testK8sClient.Create(ctx, newRegistrationWithCredential())).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: a2aNamespacedName})
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("missing required label"))
			}, testTimeout, testRetryInterval).Should(Succeed())
			Expect(configWriter.upsertedAgents).To(BeEmpty())
		})

		It("should set Ready=False when the secret is missing the key", func() {
			createSecret(true, "wrong-key")
			Expect(testK8sClient.Create(ctx, newRegistrationWithCredential())).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: a2aNamespacedName})
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("missing key"))
			}, testTimeout, testRetryInterval).Should(Succeed())
			Expect(configWriter.upsertedAgents).To(BeEmpty())
		})

		It("should set Ready=False when the secret does not exist", func() {
			Expect(testK8sClient.Create(ctx, newRegistrationWithCredential())).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: a2aNamespacedName})
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("not found"))
			}, testTimeout, testRetryInterval).Should(Succeed())
			Expect(configWriter.upsertedAgents).To(BeEmpty())

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, a2aNamespacedName, updated)).To(Succeed())
				ready := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("A2AAgentRegistration CRD validation", func() {
		AfterEach(func() {
			forceDeleteTestA2AAgentRegistration(ctx, "cel-probe", "default")
		})

		It("rejects agentPrefix mutation", func() {
			a2areg := createTestA2AAgentRegistration("cel-probe", "default", "some-route", "weather")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			a2areg.Spec.AgentPrefix = "forecast"
			err := testK8sClient.Update(ctx, a2areg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("agentPrefix is immutable"))
		})

		It("rejects targetRef mutation", func() {
			a2areg := createTestA2AAgentRegistration("cel-probe", "default", "some-route", "weather")
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			a2areg.Spec.TargetRef.Name = "other-route"
			err := testK8sClient.Update(ctx, a2areg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("targetRef is immutable"))
		})

		It("rejects invalid agentPrefix pattern", func() {
			a2areg := createTestA2AAgentRegistration("cel-probe", "default", "some-route", "Bad-Prefix")
			err := testK8sClient.Create(ctx, a2areg)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsInvalid(err)).To(BeTrue(), "expected Invalid error, got: %v", err)
		})
	})

	Context("When targetRef references an HTTPRoute in another namespace", func() {
		const routeNamespace = "a2a-routes"
		const grantName = "a2a-route-grant"

		BeforeEach(func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: routeNamespace}}
			err := testK8sClient.Create(ctx, ns)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			gw := createTestGateway(gatewayName, "default", "a2a-cross.mcp.local")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			svc := createTestService(serviceName, routeNamespace, 9090)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			httpRoute := createTestHTTPRoute(httpRouteName, routeNamespace, "a2a-cross.mcp.local", serviceName, 9090, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())

			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: httpRouteName, Namespace: routeNamespace}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			mcpExt := createTestMCPGatewayExtension(extName, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

			Eventually(func(g Gomega) {
				ext := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1alpha1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestA2AAgentRegistration(ctx, resourceName, "default")
			forceDeleteTestMCPGatewayExtension(ctx, extName, "default")
			_ = deleteTestReferenceGrant(ctx, grantName, routeNamespace)
			deleteTestHTTPRoute(ctx, httpRouteName, routeNamespace)
			deleteTestService(ctx, serviceName, routeNamespace)
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("should resolve the cross-namespace route and set Ready=True when a ReferenceGrant permits it", func() {
			grant := createTestA2AReferenceGrant(grantName, routeNamespace, "default")
			Expect(testK8sClient.Create(ctx, grant)).To(Succeed())

			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "crossagent")
			a2areg.Spec.TargetRef.Namespace = routeNamespace
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			})

			Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			for _, agent := range configWriter.upsertedAgents {
				Expect(agent.URL).To(Equal(fmt.Sprintf("http://%s.%s.svc.cluster.local:9090", serviceName, routeNamespace)))
			}

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, a2aNamespacedName, updated)).To(Succeed())
				ready := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should set Ready=False and write no config without a ReferenceGrant", func() {
			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "crossagent")
			a2areg.Spec.TargetRef.Namespace = routeNamespace
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, a2aNamespacedName, updated)).To(Succeed())
				ready := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(ready.Message).To(ContainSubstring("ReferenceGrant"))
			})

			Expect(configWriter.upsertedAgents).To(BeEmpty())
		})

		It("should withdraw the agent config when the ReferenceGrant is revoked", func() {
			grant := createTestA2AReferenceGrant(grantName, routeNamespace, "default")
			Expect(testK8sClient.Create(ctx, grant)).To(Succeed())

			a2areg := createTestA2AAgentRegistration(resourceName, "default", httpRouteName, "crossagent")
			a2areg.Spec.TargetRef.Namespace = routeNamespace
			Expect(testK8sClient.Create(ctx, a2areg)).To(Succeed())

			configWriter := newMockA2AConfigReaderWriter()
			reconciler := newA2AReconciler(configWriter)
			waitForA2ARegistrationCacheSync(ctx, a2aNamespacedName)

			// exposed while consent holds
			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.upsertedAgents).NotTo(BeEmpty())
			})

			// revoke consent
			Expect(deleteTestReferenceGrant(ctx, grantName, routeNamespace)).To(Succeed())

			// revocation withdraws the config and flips Ready=False
			reconcileA2AUntil(ctx, reconciler, a2aNamespacedName, func(g Gomega) {
				g.Expect(configWriter.removedAgents).To(ContainElement("default/" + resourceName))
				updated := &mcpv1alpha1.A2AAgentRegistration{}
				g.Expect(testK8sClient.Get(ctx, a2aNamespacedName, updated)).To(Succeed())
				ready := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(ready.Message).To(ContainSubstring("ReferenceGrant"))
			})
		})
	})
})

// createTestA2AReferenceGrant creates a ReferenceGrant in the route's namespace permitting
// A2AAgentRegistrations from fromNamespace to reference HTTPRoutes.
func createTestA2AReferenceGrant(name, namespace, fromNamespace string) *gatewayv1beta1.ReferenceGrant {
	return &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1beta1.ReferenceGrantSpec{
			From: []gatewayv1beta1.ReferenceGrantFrom{
				{
					Group:     gatewayv1beta1.Group(mcpv1alpha1.GroupVersion.Group),
					Kind:      "A2AAgentRegistration",
					Namespace: gatewayv1beta1.Namespace(fromNamespace),
				},
			},
			To: []gatewayv1beta1.ReferenceGrantTo{
				{
					Group: gatewayv1beta1.Group(gatewayv1.GroupVersion.Group),
					Kind:  "HTTPRoute",
				},
			},
		},
	}
}

// createTestA2AHostnameRoute creates an HTTPRoute whose backend is an Istio Hostname (an
// external service), attaching to the gateway via hostname — mirroring the external-agent setup.
func createTestA2AHostnameRoute(name, namespace, hostname, externalHost string, port int32, gatewayName, gatewayNamespace string) *gatewayv1.HTTPRoute {
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(gatewayName),
						Namespace: ptr.To(gatewayv1.Namespace(gatewayNamespace)),
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname)},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Group: ptr.To(gatewayv1.Group("networking.istio.io")),
									Kind:  ptr.To(gatewayv1.Kind("Hostname")),
									Name:  gatewayv1.ObjectName(externalHost),
									Port:  ptr.To(gatewayv1.PortNumber(port)),
								},
							},
						},
					},
				},
			},
		},
	}
}

// createTestExtReferenceGrant creates a ReferenceGrant in the gateway's namespace permitting
// MCPGatewayExtensions from fromNamespace to reference Gateways.
func createTestExtReferenceGrant(name, namespace, fromNamespace string) *gatewayv1beta1.ReferenceGrant {
	return &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1beta1.ReferenceGrantSpec{
			From: []gatewayv1beta1.ReferenceGrantFrom{
				{
					Group:     gatewayv1beta1.Group(mcpv1alpha1.GroupVersion.Group),
					Kind:      "MCPGatewayExtension",
					Namespace: gatewayv1beta1.Namespace(fromNamespace),
				},
			},
			To: []gatewayv1beta1.ReferenceGrantTo{
				{
					Group: gatewayv1beta1.Group(gatewayv1.GroupVersion.Group),
					Kind:  "Gateway",
				},
			},
		},
	}
}
