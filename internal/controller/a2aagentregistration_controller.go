package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
)

const (
	// a2aGatewayFinalizer guards A2AAgentRegistration deletion until config is cleaned up.
	a2aGatewayFinalizer = "mcp.kuadrant.io/a2a-finalizer"

	// A2AHTTPRouteIndex indexes A2AAgentRegistrations by their target HTTPRoute (namespace/name).
	A2AHTTPRouteIndex = "spec.targetRef.a2ahttproute"

	// A2ATargetNamespaceIndex indexes A2AAgentRegistrations by the namespace their targetRef
	// resolves into, so ReferenceGrant changes in that namespace trigger re-reconciles.
	A2ATargetNamespaceIndex = "spec.targetRef.a2anamespace"
)

// A2AAgentConfigReaderWriter adds and removes A2AAgents to the config
type A2AAgentConfigReaderWriter interface {
	UpsertA2AAgent(ctx context.Context, agent config.A2AAgent, namespaceName types.NamespacedName) error
	// RemoveA2AAgent removes an agent from all config secrets cluster-wide
	RemoveA2AAgent(ctx context.Context, agentName string) error
}

// A2AReconciler reconciles A2AAgentRegistration resources
type A2AReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	DirectAPIReader       client.Reader // uncached reader for fetching secrets
	ConfigReaderWriter    A2AAgentConfigReaderWriter
	MCPExtFinderValidator MCPGatewayExtensionFinderValidator
}

// +kubebuilder:rbac:groups=mcp.kuadrant.io,resources=a2aagentregistrations,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=mcp.kuadrant.io,resources=a2aagentregistrations/status,verbs=get;update

// Reconcile reconciles A2AAgentRegistration resources
func (r *A2AReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := logf.FromContext(ctx).WithValues("resource", "a2aagentregistration")
	logger.V(1).Info("Reconciling", "a2aregistrationname", req.Name, "namespace", req.Namespace)

	a2areg := &mcpv1alpha1.A2AAgentRegistration{}
	if err := r.Get(ctx, req.NamespacedName, a2areg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// handle deletion: config must be cleaned up before the finalizer comes off
	if !a2areg.DeletionTimestamp.IsZero() {
		logger.Info("deleting", "a2aregistrationname", a2areg.Name, "namespace", a2areg.Namespace)
		if controllerutil.ContainsFinalizer(a2areg, a2aGatewayFinalizer) {
			if err := r.ConfigReaderWriter.RemoveA2AAgent(ctx, a2aAgentName(a2areg)); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(a2areg, a2aGatewayFinalizer)
			if err := r.Update(ctx, a2areg); err != nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
				}
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	// add finalizer if not present
	if !controllerutil.ContainsFinalizer(a2areg, a2aGatewayFinalizer) {
		if controllerutil.AddFinalizer(a2areg, a2aGatewayFinalizer) {
			if err := r.Update(ctx, a2areg); err != nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
				}
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
		}
	}

	// a cross-namespace targetRef needs the route namespace's consent: being able to
	// create a registration is not permission to expose another namespace's agent
	routeNamespace := targetRefNamespace(a2areg.Namespace, a2areg.Spec.TargetRef)
	if routeNamespace != a2areg.Namespace {
		granted, err := r.hasValidReferenceGrantForRoute(ctx, a2areg, routeNamespace)
		if err != nil {
			return r.failStatus(ctx, a2areg, err.Error(), err)
		}
		if !granted {
			// a grant is an explicit consent state, not a transient failure: withdraw any
			// previously written config so revoking the grant actually revokes the exposure
			if err := r.ConfigReaderWriter.RemoveA2AAgent(ctx, a2aAgentName(a2areg)); err != nil {
				return r.failStatus(ctx, a2areg, err.Error(), err)
			}
			msg := fmt.Sprintf("cross-namespace targetRef requires a ReferenceGrant in namespace %s permitting A2AAgentRegistration to reference HTTPRoute", routeNamespace)
			return r.failStatus(ctx, a2areg, msg, nil)
		}
	}

	// get the HTTPRoute this registration targets, honoring targetRef.namespace
	targetRoute, err := r.getTargetHTTPRoute(ctx, a2areg)
	if err != nil {
		return r.failStatus(ctx, a2areg, err.Error(), err)
	}

	// find gateways that have accepted the httproute
	validGateways, err := findValidGatewaysForHTTPRoute(ctx, r.Client, targetRoute)
	if err != nil {
		return r.failStatus(ctx, a2areg, err.Error(), err)
	}
	if len(validGateways) == 0 {
		err := fmt.Errorf("no valid gateways for httproute")
		return r.failStatus(ctx, a2areg, err.Error(), err)
	}

	// collect namespaces of valid MCPGatewayExtensions whose listener the route attaches to
	validNamespaces := []string{}
	for _, vg := range validGateways {
		mcpGatewayExtensions, err := r.MCPExtFinderValidator.FindValidMCPGatewayExtsForGateway(ctx, vg)
		if err != nil {
			return r.failStatus(ctx, a2areg, err.Error(), err)
		}
		for _, vext := range mcpGatewayExtensions {
			if !httpRouteAttachesToListener(targetRoute, vg, vext) {
				logger.V(1).Info("skipping mcpgatewayextension: httproute does not attach to targeted listener",
					"extension", vext.Name, "namespace", vext.Namespace, "sectionName", vext.Spec.TargetRef.SectionName)
				continue
			}
			validNamespaces = append(validNamespaces, vext.Namespace)
		}
	}
	if len(validNamespaces) == 0 {
		// not an error: no extension is configured for this route yet
		result, err := r.failStatus(ctx, a2areg, "no matching mcpgatewayextensions for attached listener", nil)
		return result, err
	}

	agentConfig, err := r.buildA2AAgentConfig(ctx, targetRoute, a2areg)
	if err != nil {
		return r.failStatus(ctx, a2areg, err.Error(), err)
	}
	for _, configNs := range validNamespaces {
		if err := r.ConfigReaderWriter.UpsertA2AAgent(ctx, *agentConfig, config.NamespaceName(configNs)); err != nil {
			return r.failStatus(ctx, a2areg, err.Error(), err)
		}
	}

	// config written, set status
	if a2areg.Spec.State == mcpv1alpha1.ServerStateDisabled {
		if err := r.updateStatus(ctx, a2areg, false, conditionReasonDisabled, "agent is disabled"); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
			}
			return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
		}
		return reconcile.Result{}, nil
	}
	if err := r.updateStatus(ctx, a2areg, true, conditionReasonReady, "config written successfully"); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
		}
		return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
	}

	return reconcile.Result{}, nil
}

// failStatus records a not-ready condition and returns the reconcile result for reconcileErr.
// A nil reconcileErr exits cleanly (the condition is terminal until a watched resource changes).
//
// Previously written config is deliberately left in place on failure (last-known-good,
// mirroring MCPServerRegistration): a transient error must not rip a live agent out of the
// data plane. Config is removed only on deletion and on ReferenceGrant revocation — consent
// is an explicit state, not a transient failure.
func (r *A2AReconciler) failStatus(ctx context.Context, a2areg *mcpv1alpha1.A2AAgentRegistration, message string, reconcileErr error) (reconcile.Result, error) {
	if err := r.updateStatus(ctx, a2areg, false, conditionReasonNotReady, message); err != nil {
		if apierrors.IsConflict(err) {
			// don't log these as they are just noise
			return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
		}
		return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
	}
	if reconcileErr == nil {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, fmt.Errorf("reconcile failed %w", reconcileErr)
}

// a2aAgentName is the config identity for a registration; fan-out upserts and
// cluster-wide removal both key on it.
func a2aAgentName(a2areg *mcpv1alpha1.A2AAgentRegistration) string {
	return fmt.Sprintf("%s/%s", a2areg.Namespace, a2areg.Name)
}

// targetRefNamespace resolves the namespace a targetRef points into, defaulting
// to the owning resource's namespace when unset.
func targetRefNamespace(defaultNamespace string, targetRef mcpv1alpha1.TargetReference) string {
	if targetRef.Namespace != "" {
		return targetRef.Namespace
	}
	return defaultNamespace
}

// hasValidReferenceGrantForRoute reports whether a ReferenceGrant in the route's namespace
// permits this registration to reference HTTPRoutes there — the same consent model
// MCPGatewayExtension uses for cross-namespace Gateway references.
func (r *A2AReconciler) hasValidReferenceGrantForRoute(ctx context.Context, a2areg *mcpv1alpha1.A2AAgentRegistration, routeNamespace string) (bool, error) {
	refGrantList := &gatewayv1beta1.ReferenceGrantList{}
	if err := r.List(ctx, refGrantList, client.InNamespace(routeNamespace)); err != nil {
		return false, fmt.Errorf("failed to list ReferenceGrants: %w", err)
	}
	for i := range refGrantList.Items {
		if referenceGrantAllowsA2ARouteRef(&refGrantList.Items[i], a2areg) {
			return true, nil
		}
	}
	return false, nil
}

// referenceGrantAllowsA2ARouteRef checks if a ReferenceGrant permits the A2AAgentRegistration
// to reference its target HTTPRoute.
func referenceGrantAllowsA2ARouteRef(rg *gatewayv1beta1.ReferenceGrant, a2areg *mcpv1alpha1.A2AAgentRegistration) bool {
	fromAllowed := false
	for _, from := range rg.Spec.From {
		if string(from.Group) == mcpv1alpha1.GroupVersion.Group &&
			string(from.Kind) == "A2AAgentRegistration" &&
			string(from.Namespace) == a2areg.Namespace {
			fromAllowed = true
			break
		}
	}
	if !fromAllowed {
		return false
	}

	for _, to := range rg.Spec.To {
		if string(to.Group) == gatewayv1.GroupVersion.Group {
			// empty kind means all kinds in the group
			if to.Kind == "" || string(to.Kind) == "HTTPRoute" {
				// if name is specified, it must match; empty means all
				if to.Name == nil || *to.Name == "" || string(*to.Name) == a2areg.Spec.TargetRef.Name {
					return true
				}
			}
		}
	}
	return false
}

func (r *A2AReconciler) getTargetHTTPRoute(ctx context.Context, a2areg *mcpv1alpha1.A2AAgentRegistration) (*gatewayv1.HTTPRoute, error) {
	namespaceName := types.NamespacedName{
		Namespace: targetRefNamespace(a2areg.Namespace, a2areg.Spec.TargetRef),
		Name:      a2areg.Spec.TargetRef.Name,
	}
	targetRoute := &gatewayv1.HTTPRoute{}
	if err := r.Get(ctx, namespaceName, targetRoute); err != nil {
		return nil, fmt.Errorf("failed to get targeted httproute %w", err)
	}
	return targetRoute, nil
}

// buildA2AAgentConfig derives the broker config entry for a registration. The endpoint
// carries no path: the A2A endpoint and well-known card paths are protocol-defined, and
// agentCardURL overrides the card fetch location when set.
func (r *A2AReconciler) buildA2AAgentConfig(ctx context.Context, targetRoute *gatewayv1.HTTPRoute, a2areg *mcpv1alpha1.A2AAgentRegistration) (*config.A2AAgent, error) {
	if a2areg.DeletionTimestamp != nil {
		return nil, fmt.Errorf("cant generate config for deleting agent %s/%s", a2areg.Namespace, a2areg.Name)
	}
	serverInfo, err := buildServerInfoFromHTTPRoute(ctx, r.Client, targetRoute, "")
	if err != nil {
		return nil, err
	}

	agentConfig := config.A2AAgent{
		Name:         a2aAgentName(a2areg),
		URL:          serverInfo.Endpoint,
		Hostname:     serverInfo.Hostname,
		AgentPrefix:  a2areg.Spec.AgentPrefix,
		AgentCardURL: a2areg.Spec.AgentCardURL,
		State:        string(a2areg.Spec.State),
	}

	// add credential if configured: used by the broker for card discovery only,
	// never injected into client message/send or tasks/* requests
	if a2areg.Spec.CredentialRef != nil {
		credential, err := readLabeledCredential(ctx, r.DirectAPIReader, a2areg.Namespace, a2areg.Spec.CredentialRef)
		if err != nil {
			return nil, err
		}
		agentConfig.Credential = credential
	}

	return &agentConfig, nil
}

func (r *A2AReconciler) updateStatus(
	ctx context.Context,
	a2areg *mcpv1alpha1.A2AAgentRegistration,
	ready bool,
	reason string,
	message string,
) error {
	conditions, changed := applyReadyCondition(a2areg.Status.Conditions, ready, reason, message)
	a2areg.Status.Conditions = conditions
	if !changed {
		return nil
	}
	return r.Status().Update(ctx, a2areg)
}

// SetupWithManager sets up the reconciler
func (r *A2AReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if err := setupIndexA2ARegistrationToHTTPRoute(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to setup required index from A2AAgentRegistration to httproutes %w", err)
	}
	if err := setupIndexA2ARegistrationToTargetNamespace(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to setup required index from A2AAgentRegistration to target namespaces %w", err)
	}

	controller := ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.A2AAgentRegistration{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&gatewayv1.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findA2AAgentRegistrationsForHTTPRoute),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findA2AAgentRegistrationsForSecret),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				secret := obj.(*corev1.Secret)
				return secret.Labels != nil && secret.Labels[ManagedSecretLabel] == ManagedSecretValue
			})),
		).
		Watches(
			&mcpv1alpha1.MCPGatewayExtension{},
			handler.EnqueueRequestsFromMapFunc(r.findA2AAgentRegistrationsForMCPGatewayExtension),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&gatewayv1beta1.ReferenceGrant{},
			handler.EnqueueRequestsFromMapFunc(r.findA2AAgentRegistrationsForReferenceGrant),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("a2aagentregistration")

	return controller.Complete(r)
}

// setupIndexA2ARegistrationToHTTPRoute indexes registrations by target HTTPRoute. The index
// resolves targetRef.namespace exactly like getTargetHTTPRoute so cross-namespace watches fire.
func setupIndexA2ARegistrationToHTTPRoute(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &mcpv1alpha1.A2AAgentRegistration{}, A2AHTTPRouteIndex, func(rawObj client.Object) []string {
		a2areg := rawObj.(*mcpv1alpha1.A2AAgentRegistration)
		targetRef := a2areg.Spec.TargetRef
		if targetRef.Kind == "HTTPRoute" {
			return []string{httpRouteIndexValue(targetRefNamespace(a2areg.Namespace, targetRef), targetRef.Name)}
		}
		return []string{}
	})
}

// setupIndexA2ARegistrationToTargetNamespace indexes registrations by the namespace their
// targetRef resolves into, used by the ReferenceGrant watch.
func setupIndexA2ARegistrationToTargetNamespace(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &mcpv1alpha1.A2AAgentRegistration{}, A2ATargetNamespaceIndex, func(rawObj client.Object) []string {
		a2areg := rawObj.(*mcpv1alpha1.A2AAgentRegistration)
		return []string{targetRefNamespace(a2areg.Namespace, a2areg.Spec.TargetRef)}
	})
}

// findA2AAgentRegistrationsForReferenceGrant finds registrations whose targetRef resolves into
// the grant's namespace, so granting or revoking consent triggers a re-reconcile.
func (r *A2AReconciler) findA2AAgentRegistrationsForReferenceGrant(ctx context.Context, obj client.Object) []reconcile.Request {
	rg := obj.(*gatewayv1beta1.ReferenceGrant)
	log := logf.FromContext(ctx).WithValues("ReferenceGrant", rg.Name, "namespace", rg.Namespace)

	a2aregList := &mcpv1alpha1.A2AAgentRegistrationList{}
	if err := r.List(ctx, a2aregList, client.MatchingFields{A2ATargetNamespaceIndex: rg.Namespace}); err != nil {
		log.Error(err, "Failed to list A2AAgentRegistrations using target namespace index")
		return nil
	}

	var requests []reconcile.Request
	for i := range a2aregList.Items {
		// same-namespace references never need a grant
		if a2aregList.Items[i].Namespace == rg.Namespace {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      a2aregList.Items[i].Name,
				Namespace: a2aregList.Items[i].Namespace,
			},
		})
	}
	return requests
}

// findA2AAgentRegistrationsForHTTPRoute finds all A2AAgentRegistrations that reference the given HTTPRoute
func (r *A2AReconciler) findA2AAgentRegistrationsForHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	httpRoute := obj.(*gatewayv1.HTTPRoute)
	log := logf.FromContext(ctx).WithValues("HTTPRoute", httpRoute.Name, "namespace", httpRoute.Namespace)

	indexKey := httpRouteIndexValue(httpRoute.Namespace, httpRoute.Name)
	a2aregList := &mcpv1alpha1.A2AAgentRegistrationList{}
	if err := r.List(ctx, a2aregList, client.MatchingFields{A2AHTTPRouteIndex: indexKey}); err != nil {
		log.Error(err, "Failed to list A2AAgentRegistrations using index")
		return nil
	}

	var requests []reconcile.Request
	for i := range a2aregList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      a2aregList.Items[i].Name,
				Namespace: a2aregList.Items[i].Namespace,
			},
		})
	}
	return requests
}

// findA2AAgentRegistrationsForSecret finds A2AAgentRegistrations referencing the given secret
func (r *A2AReconciler) findA2AAgentRegistrationsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret := obj.(*corev1.Secret)
	log := logf.FromContext(ctx).WithValues("Secret", secret.Name, "namespace", secret.Namespace)

	// credentials are same-namespace by design: list registrations in the secret's namespace
	a2aregList := &mcpv1alpha1.A2AAgentRegistrationList{}
	if err := r.List(ctx, a2aregList, client.InNamespace(secret.Namespace)); err != nil {
		log.Error(err, "Failed to list A2AAgentRegistrations")
		return nil
	}
	var requests []reconcile.Request
	for i := range a2aregList.Items {
		if a2aReferencesSecret(a2aregList.Items[i].Spec, secret.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      a2aregList.Items[i].Name,
					Namespace: a2aregList.Items[i].Namespace,
				},
			})
		}
	}
	return requests
}

// a2aReferencesSecret checks whether an A2AAgentRegistration references the named secret
func a2aReferencesSecret(spec mcpv1alpha1.A2AAgentRegistrationSpec, secretName string) bool {
	return spec.CredentialRef != nil && spec.CredentialRef.Name == secretName
}

// findA2AAgentRegistrationsForMCPGatewayExtension finds all A2AAgentRegistrations whose HTTPRoutes
// are attached to the Gateway targeted by the given MCPGatewayExtension, so config is written to
// newly valid namespaces as extensions come and go.
func (r *A2AReconciler) findA2AAgentRegistrationsForMCPGatewayExtension(ctx context.Context, obj client.Object) []reconcile.Request {
	mcpExt := obj.(*mcpv1alpha1.MCPGatewayExtension)
	logger := logf.FromContext(ctx).WithValues("MCPGatewayExtension", mcpExt.Name, "namespace", mcpExt.Namespace)

	gatewayNamespace := mcpExt.Spec.TargetRef.Namespace
	if gatewayNamespace == "" {
		gatewayNamespace = mcpExt.Namespace
	}
	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      mcpExt.Spec.TargetRef.Name,
		Namespace: gatewayNamespace,
	}, gateway); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to get Gateway for MCPGatewayExtension")
		}
		return nil
	}

	httpRouteList := &gatewayv1.HTTPRouteList{}
	if err := r.List(ctx, httpRouteList); err != nil {
		logger.Error(err, "Failed to list HTTPRoutes")
		return nil
	}

	var requests []reconcile.Request
	for i := range httpRouteList.Items {
		httpRoute := &httpRouteList.Items[i]
		for _, parentRef := range httpRoute.Spec.ParentRefs {
			parentNs := httpRoute.Namespace
			if parentRef.Namespace != nil {
				parentNs = string(*parentRef.Namespace)
			}
			if string(parentRef.Name) == gateway.Name && parentNs == gateway.Namespace {
				indexKey := httpRouteIndexValue(httpRoute.Namespace, httpRoute.Name)
				a2aregList := &mcpv1alpha1.A2AAgentRegistrationList{}
				if err := r.List(ctx, a2aregList, client.MatchingFields{A2AHTTPRouteIndex: indexKey}); err != nil {
					logger.Error(err, "Failed to list A2AAgentRegistrations using index")
					continue
				}
				for j := range a2aregList.Items {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      a2aregList.Items[j].Name,
							Namespace: a2aregList.Items[j].Namespace,
						},
					})
				}
				break
			}
		}
	}
	return requests
}
