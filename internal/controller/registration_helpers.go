package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

// readLabeledCredential fetches a credential secret via the uncached reader and returns
// the referenced key's value. The secret must carry the managed-secret label: the label
// is what authorizes the controller to read it, so its absence is an error, not a skip.
// Shared by the MCPServerRegistration and A2AAgentRegistration reconcilers.
func readLabeledCredential(ctx context.Context, reader client.Reader, namespace string, ref *mcpv1alpha1.SecretReference) (string, error) {
	secret := &corev1.Secret{}
	err := reader.Get(ctx, types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("credential secret %s not found", ref.Name)
		}
		return "", fmt.Errorf("failed to get credential secret: %w", err)
	}

	// check for required label
	if secret.Labels == nil || secret.Labels[ManagedSecretLabel] != ManagedSecretValue {
		return "", fmt.Errorf("credential secret %s is missing required label %s=%s",
			ref.Name, ManagedSecretLabel, ManagedSecretValue)
	}

	val, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("credential secret %s missing key %s", ref.Name, ref.Key)
	}
	return string(val), nil
}

// applyReadyCondition updates or appends the Ready condition, preserving
// LastTransitionTime when the status itself did not flip (True<->False), and reports
// whether anything changed so callers can skip no-op status updates that would
// otherwise thrash the API server. Shared by both registration reconcilers.
func applyReadyCondition(conditions []metav1.Condition, ready bool, reason, message string) ([]metav1.Condition, bool) {
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		condition.Status = metav1.ConditionTrue
		condition.Reason = conditionReasonReady
	}

	for i, cond := range conditions {
		if cond.Type == condition.Type {
			// only update LastTransitionTime if the STATUS actually changed (True->False or False->True)
			if cond.Status == condition.Status {
				condition.LastTransitionTime = cond.LastTransitionTime
			}
			changed := cond.Status != condition.Status || cond.Reason != condition.Reason || cond.Message != condition.Message
			conditions[i] = condition
			return conditions, changed
		}
	}
	return append(conditions, condition), true
}
