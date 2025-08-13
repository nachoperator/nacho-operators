package discovery

import (
	"context"
	//"encoding/base64"

	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SecretFinder specializes in finding and parsing specific secrets.
type SecretFinder struct {
	Client client.Client
}

// NewSecretFinder creates a new instance of SecretFinder.
func NewSecretFinder(c client.Client) *SecretFinder {
	return &SecretFinder{Client: c}
}

// ServiceAccountJSON represents the structure of the JSON inside the secret.
type ServiceAccountJSON struct {
	ClientEmail string `json:"client_email"`
}

// FindAuthnServiceAccountEmail inspects a pod's volumes to find one named "mas-stack-authn-sa-volume",
// extracts the associated Secret's name, and then parses it to find the client_email.
func (f *SecretFinder) FindAuthnServiceAccountEmail(ctx context.Context, pod *corev1.Pod) (string, error) {
	log := log.FromContext(ctx)
	const targetVolumeName = "mas-stack-authn-sa-volume"
	var secretName string

	// 1. Find the volume with the target name within the pod spec
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == targetVolumeName {
			// Found the volume, check if it's populated by a Secret
			if volume.Secret != nil {
				secretName = volume.Secret.SecretName
				log.V(1).Info("Found target volume and associated secret", "pod", pod.Name, "volume", targetVolumeName, "secretName", secretName)
				break // Stop searching once found
			}
		}
	}

	// If the volume or secret name was not found, it's not an error, just exit.
	if secretName == "" {
		log.V(1).Info("Pod does not have the target volume mounted", "pod", pod.Name, "targetVolume", targetVolumeName)
		return "", nil
	}

	// 2. Get the Secret from the Kubernetes API using the discovered name
	secret := &corev1.Secret{}
	err := f.Client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: pod.Namespace}, secret)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get authn secret", "secretName", secretName, "namespace", pod.Namespace)
			return "", err
		}
		log.V(1).Info("Authn secret reference found in volume, but secret itself not found", "secretName", secretName, "namespace", pod.Namespace)
		return "", nil
	}

	// 3. Extract the JSON content from the secret's data
	jsonData, ok := secret.Data["authn-service-account.json"]
	if !ok {
		return "", fmt.Errorf("secret %s found, but key 'authn-service-account.json' is missing", secretName)
	}
	
	// 4. Decode Base64 data (secrets are always encoded)
	// decodedJson, err := base64.StdEncoding.DecodeString(string(jsonData))
	// if err != nil {
	// 	// If decoding fails, it's a real problem with the secret's content
	// 	return "", fmt.Errorf("failed to decode base64 data from secret %s: %w", secretName, err)
	// }

	// 5. Parse the JSON to find the client_email field
	var saJSON ServiceAccountJSON
	if err := json.Unmarshal(jsonData, &saJSON); err != nil {
		log.Error(err, "Failed to unmarshal service account JSON from secret", "secretName", secretName)
		return "", err
	}

	return saJSON.ClientEmail, nil
}
