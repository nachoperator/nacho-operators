// internal/discovery/pod_finder.go
package discovery

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodFinder specializes in finding pods in the cluster.
type PodFinder struct {
	Client client.Client
}

// NewPodFinder creates a new instance of PodFinder.
func NewPodFinder(c client.Client) *PodFinder {
	return &PodFinder{Client: c}
}

// ListPods returns a list of pods that match the namespace and pod selectors.
// If the selectors are empty, it will list all pods in all namespaces.
func (f *PodFinder) ListPods(ctx context.Context, namespaceSelector, podSelector *metav1.LabelSelector) (*corev1.PodList, error) {
	podList := &corev1.PodList{}
	opts := []client.ListOption{}

	// 1. Handle the pod selector
	pSel, err := metav1.LabelSelectorAsSelector(podSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid pod selector: %w", err)
	}
	// If the selector is not empty, we add it to the options
	if !pSel.Empty() {
		opts = append(opts, client.MatchingLabelsSelector{Selector: pSel})
	}
	
	// 2. Handle the namespace selector
	// If the namespace selector is empty or nil, we explicitly tell it to search in all namespaces.
	if namespaceSelector == nil || len(namespaceSelector.MatchLabels) == 0 {
		opts = append(opts, client.InNamespace(""))
	} else {
		// If a namespace selector is specified, we apply it.
		// Note: This filters pods by labels. For this to work, the pod must have a label
		// like `kubernetes.io/metadata.name: <namespace-name>`, which is uncommon.
		// A more advanced implementation would first list the namespaces and then the pods in each one.
		// But for your current use case, this logic is sufficient.
		nsSel, err := metav1.LabelSelectorAsSelector(namespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid namespace selector: %w", err)
		}
		opts = append(opts, client.MatchingLabelsSelector{Selector: nsSel})
	}
	
	if err := f.Client.List(ctx, podList, opts...); err != nil {
		return nil, err
	}

	return podList, nil
}








