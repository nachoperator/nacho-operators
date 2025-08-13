package utils

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ListAllSecrets retrieves all secrets from the cluster using the provided client.
// It returns a slice of ObjectKey (containing Namespace and Name) and an error if one occurs.
func ListAllSecrets(ctx context.Context, k8sClient client.Client, log logr.Logger) ([]client.ObjectKey, error) {
	secretList := &corev1.SecretList{}
	secretKeys := []client.ObjectKey{} // Slice to store Namespace/Name

	// List secrets across all namespaces (empty ListOptions)
	err := k8sClient.List(ctx, secretList, &client.ListOptions{})
	if err != nil {
		log.Error(err, "Failed to list Secrets across all namespaces from utils.ListAllSecrets")
		// Return empty slice and the error
		return secretKeys, err
	}

	// Extract Namespace and Name from each found secret
	for _, secret := range secretList.Items {
		secretKeys = append(secretKeys, client.ObjectKey{
			Namespace: secret.Namespace,
			Name:      secret.Name,
		})
	}

	log.Info("utils.ListAllSecrets function completed", "count", len(secretKeys))
	// Return the list of ObjectKeys and nil error (success)
	return secretKeys, nil
}
