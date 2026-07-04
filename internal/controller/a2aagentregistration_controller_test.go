package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

func TestReferenceGrantAllowsA2ARouteRef(t *testing.T) {
	a2areg := &mcpv1alpha1.A2AAgentRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "registrations"},
		Spec: mcpv1alpha1.A2AAgentRegistrationSpec{
			TargetRef: mcpv1alpha1.TargetReference{Kind: "HTTPRoute", Name: "target-route", Namespace: "routes"},
		},
	}

	grant := func(fromKind, fromNS, toKind string, toName *string) *gatewayv1beta1.ReferenceGrant {
		return &gatewayv1beta1.ReferenceGrant{
			Spec: gatewayv1beta1.ReferenceGrantSpec{
				From: []gatewayv1beta1.ReferenceGrantFrom{{
					Group:     gatewayv1beta1.Group(mcpv1alpha1.GroupVersion.Group),
					Kind:      gatewayv1beta1.Kind(fromKind),
					Namespace: gatewayv1beta1.Namespace(fromNS),
				}},
				To: []gatewayv1beta1.ReferenceGrantTo{{
					Group: gatewayv1beta1.Group(gatewayv1.GroupVersion.Group),
					Kind:  gatewayv1beta1.Kind(toKind),
					Name:  (*gatewayv1.ObjectName)(toName),
				}},
			},
		}
	}

	cases := []struct {
		name string
		rg   *gatewayv1beta1.ReferenceGrant
		want bool
	}{
		{"exact match", grant("A2AAgentRegistration", "registrations", "HTTPRoute", nil), true},
		{"named route match", grant("A2AAgentRegistration", "registrations", "HTTPRoute", ptr.To("target-route")), true},
		{"named route mismatch", grant("A2AAgentRegistration", "registrations", "HTTPRoute", ptr.To("other-route")), false},
		{"wrong from kind", grant("MCPServerRegistration", "registrations", "HTTPRoute", nil), false},
		{"wrong from namespace", grant("A2AAgentRegistration", "other-ns", "HTTPRoute", nil), false},
		{"wrong to kind", grant("A2AAgentRegistration", "registrations", "Gateway", nil), false},
		{"empty to kind allows all in group", grant("A2AAgentRegistration", "registrations", "", nil), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := referenceGrantAllowsA2ARouteRef(c.rg, a2areg); got != c.want {
				t.Errorf("referenceGrantAllowsA2ARouteRef() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestTargetRefNamespace(t *testing.T) {
	cases := []struct {
		name      string
		targetRef mcpv1alpha1.TargetReference
		defaultNs string
		want      string
	}{
		{"empty namespace defaults to owner", mcpv1alpha1.TargetReference{Name: "r"}, "own-ns", "own-ns"},
		{"explicit namespace wins", mcpv1alpha1.TargetReference{Name: "r", Namespace: "other-ns"}, "own-ns", "other-ns"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := targetRefNamespace(c.defaultNs, c.targetRef); got != c.want {
				t.Errorf("targetRefNamespace() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestA2AAgentName(t *testing.T) {
	a2areg := &mcpv1alpha1.A2AAgentRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "weather-agent", Namespace: "mcp-test"},
	}
	if got := a2aAgentName(a2areg); got != "mcp-test/weather-agent" {
		t.Errorf("a2aAgentName() = %q, want %q", got, "mcp-test/weather-agent")
	}
}

func TestA2AReferencesSecret(t *testing.T) {
	withRef := mcpv1alpha1.A2AAgentRegistrationSpec{
		CredentialRef: &mcpv1alpha1.SecretReference{Name: "agent-secret"},
	}
	if !a2aReferencesSecret(withRef, "agent-secret") {
		t.Error("expected spec referencing agent-secret to match")
	}
	if a2aReferencesSecret(withRef, "other-secret") {
		t.Error("expected spec not to match other-secret")
	}
	if a2aReferencesSecret(mcpv1alpha1.A2AAgentRegistrationSpec{}, "agent-secret") {
		t.Error("expected spec without credentialRef not to match")
	}
}

func TestA2AGetTargetHTTPRouteUsesTargetRefNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)

	registrationNsRoute := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "target-route", Namespace: "registrations"},
	}
	targetNsRoute := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "target-route", Namespace: "routes"},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(registrationNsRoute, targetNsRoute).
		Build()

	r := &A2AReconciler{Client: fakeClient}
	a2areg := &mcpv1alpha1.A2AAgentRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "registrations"},
		Spec: mcpv1alpha1.A2AAgentRegistrationSpec{
			AgentPrefix: "weather",
			TargetRef: mcpv1alpha1.TargetReference{
				Name:      "target-route",
				Namespace: "routes",
			},
		},
	}

	got, err := r.getTargetHTTPRoute(context.Background(), a2areg)
	if err != nil {
		t.Fatalf("getTargetHTTPRoute() error: %v", err)
	}
	if got.Namespace != "routes" {
		t.Errorf("resolved route namespace = %q, want %q", got.Namespace, "routes")
	}

	// without an explicit namespace the registration's own namespace is used
	a2areg.Spec.TargetRef.Namespace = ""
	got, err = r.getTargetHTTPRoute(context.Background(), a2areg)
	if err != nil {
		t.Fatalf("getTargetHTTPRoute() error: %v", err)
	}
	if got.Namespace != "registrations" {
		t.Errorf("resolved route namespace = %q, want %q", got.Namespace, "registrations")
	}
}

func TestSetupIndexA2ARegistrationToHTTPRouteResolvesNamespace(t *testing.T) {
	// the index fn must resolve targetRef.namespace exactly like getTargetHTTPRoute,
	// otherwise cross-namespace HTTPRoute changes never trigger a reconcile
	indexFn := func(a2areg *mcpv1alpha1.A2AAgentRegistration) []string {
		targetRef := a2areg.Spec.TargetRef
		if targetRef.Kind == "HTTPRoute" {
			return []string{httpRouteIndexValue(targetRefNamespace(a2areg.Namespace, targetRef), targetRef.Name)}
		}
		return []string{}
	}

	crossNs := &mcpv1alpha1.A2AAgentRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "registrations"},
		Spec: mcpv1alpha1.A2AAgentRegistrationSpec{
			TargetRef: mcpv1alpha1.TargetReference{Kind: "HTTPRoute", Name: "r", Namespace: "routes"},
		},
	}
	if got := indexFn(crossNs); len(got) != 1 || got[0] != "routes/r" {
		t.Errorf("index value = %v, want [routes/r]", got)
	}

	sameNs := &mcpv1alpha1.A2AAgentRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "registrations"},
		Spec: mcpv1alpha1.A2AAgentRegistrationSpec{
			TargetRef: mcpv1alpha1.TargetReference{Kind: "HTTPRoute", Name: "r"},
		},
	}
	if got := indexFn(sameNs); len(got) != 1 || got[0] != "registrations/r" {
		t.Errorf("index value = %v, want [registrations/r]", got)
	}
}
