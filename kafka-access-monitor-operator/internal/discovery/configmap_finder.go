// internal/discovery/configmap_finder.go
package discovery

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ConfigMapFinder specializes in finding ConfigMaps associated with a Pod.
type ConfigMapFinder struct {
	Client client.Client
}


// NewConfigMapFinder creates a new instance of ConfigMapFinder.
func NewConfigMapFinder(c client.Client) *ConfigMapFinder {
	return &ConfigMapFinder{Client: c}
}

// GetMountedConfigMaps returns a list of all ConfigMaps that are mounted as volumes in the given pod.
func (f *ConfigMapFinder) GetMountedConfigMaps(ctx context.Context, pod *corev1.Pod) ([]corev1.ConfigMap, error) {
	log := log.FromContext(ctx)
	var mountedConfigMaps []corev1.ConfigMap

	// Map to avoid fetching the same ConfigMap multiple times if it's mounted in several places
	uniqueCmNames := make(map[string]bool)

	// 1. Identify the names of the ConfigMaps used in volumes
	for _, volume := range pod.Spec.Volumes {
		if volume.ConfigMap != nil {
			uniqueCmNames[volume.ConfigMap.Name] = true
		}
	}

	if len(uniqueCmNames) == 0 {
		return nil, nil // No ConfigMaps mounted, return an empty list
	}

	// 2. Fetch each unique ConfigMap using the Kubernetes client
	for cmName := range uniqueCmNames {
		cm := corev1.ConfigMap{}
		err := f.Client.Get(ctx, types.NamespacedName{Name: cmName, Namespace: pod.Namespace}, &cm)
		if err != nil {
			// We log the error but continue, as the CM might not exist or we may lack permissions
			log.Error(err, "Could not get mounted ConfigMap", "configMap", cmName, "namespace", pod.Namespace)
			continue
		}
		mountedConfigMaps = append(mountedConfigMaps, cm)
	}

	return mountedConfigMaps, nil
}

// CheckConfigMapData searches for relevant keys within a ConfigMap's data.
// It is now part of the discovery package and receives the keys to search for directly.
// func CheckConfigMapData(data map[string]string, keysToSearch map[string][]string) ([]string, string) {
// 	var detectedEndpoints []string
// 	var sourceDetails []string

// 	for cmKeyToSearch := range keysToSearch {
// 		if value, exists := data[cmKeyToSearch]; exists && value != "" {
// 			// Key found!
// 			endpoints := strings.Split(value, ",")
// 			for _, ep := range endpoints {
// 				// You could add regex validation here
// 				detectedEndpoints = append(detectedEndpoints, strings.TrimSpace(ep))
// 			}
// 			if len(endpoints) > 0 {
// 				sourceDetails = append(sourceDetails, fmt.Sprintf("configmap-key:%s", cmKeyToSearch))
// 			}
// 		}
// 	}
// 	return detectedEndpoints, strings.Join(sourceDetails, ";")
// }

// CheckConfigMapData now parses YAML content from the ConfigMap data.
// func CheckConfigMapData(data map[string]string, keysToSearch map[string][]string) (*KafkaConfigDetails, bool) {
// 	// Iterate over the keys we are supposed to search for (e.g., "config.yaml")
// 	for yamlKey := range keysToSearch {
// 		if yamlContent, exists := data[yamlKey]; exists && yamlContent != "" {
			
// 			// 1. Parse the YAML content from the ConfigMap key's value
// 			var config rootYAMLConfig
// 			if err := yaml.Unmarshal([]byte(yamlContent), &config); err != nil {
// 				// Failed to parse, maybe it's not the YAML we expect. Skip.
// 				continue
// 			}

// 			// If kafka config is empty, skip
// 			if config.Kafka.BootstrapServerConfig == "" {
// 				continue
// 			}

// 			// 2. Extract all the information
// 			details := &KafkaConfigDetails{
// 				Brokers:          strings.Split(config.Kafka.BootstrapServerConfig, ","),
// 				Source:           fmt.Sprintf("configmap-key:%s", yamlKey),
// 				GroupID:          config.Kafka.GroupID,
// 				ResetOffset:      config.Kafka.ResetOffset,
// 				AuthMechanisms:   []string{},
// 				AdditionalConfig: make(map[string]string),
// 			}

// 			// 3. Infer Client Type and Authentication
// 			if details.GroupID != "" {
// 				details.ClientType = "Consumer"
// 			} else {
// 				details.ClientType = "Producer/Unknown" // Assume Producer if no GroupID
// 			}

// 			if config.Kafka.MtlsAuth {
// 				details.AuthMechanisms = append(details.AuthMechanisms, "mTLS")
// 			}
// 			if config.Kafka.OAuth {
// 				details.AuthMechanisms = append(details.AuthMechanisms, "OAuth")
// 			}

// 			// You can add other properties to AdditionalConfig here if needed

// 			return details, true // We found and processed a valid config
// 		}
// 	}

// 	return nil, false // No matching key found or processed
// }
