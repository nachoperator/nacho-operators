package discovery

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	accessmonitorv1alpha1 "github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DiagnosticsFinder is responsible for fetching events and logs for a pod.
type DiagnosticsFinder struct {
	Client    client.Client
	Clientset kubernetes.Interface
}

// NewDiagnosticsFinder creates a new instance of DiagnosticsFinder.
func NewDiagnosticsFinder(c client.Client, cs kubernetes.Interface) *DiagnosticsFinder {
	return &DiagnosticsFinder{Client: c, Clientset: cs}
}

// FetchRecentProblems gets recent warning events and error logs for a given pod,
// returning them as a deduplicated slice of structured ProblemDetail objects.
func (f *DiagnosticsFinder) FetchRecentProblems(ctx context.Context, pod *corev1.Pod, since time.Duration) ([]accessmonitorv1alpha1.ProblemDetail, error) {
	log := log.FromContext(ctx)
	// Usamos un mapa para deduplicar problemas por su mensaje.
	problemsMap := make(map[string]accessmonitorv1alpha1.ProblemDetail)
	const ignoredErrorSubstring = "UNKNOWN_TOPIC_OR_PARTITION"

	// Helper para añadir o actualizar un problema en el mapa
	addOrUpdateProblem := func(msg, problemType string, timestamp metav1.Time) {
		if existing, found := problemsMap[msg]; found {
			existing.Count++
			if timestamp.After(existing.LastSeenTimestamp.Time) {
				existing.LastSeenTimestamp = timestamp
			}
			problemsMap[msg] = existing
		} else {
			problemsMap[msg] = accessmonitorv1alpha1.ProblemDetail{
				Type:              problemType,
				Message:           msg,
				Count:             1,
				LastSeenTimestamp: timestamp,
			}
		}
	}

	// --- 1. Get Warning Events for the Pod
	eventList := &corev1.EventList{}
	fieldSelector := fields.SelectorFromSet(fields.Set{
		"involvedObject.kind":      "Pod",
		"involvedObject.name":      pod.Name,
		"involvedObject.namespace": pod.Namespace,
		"type":                     "Warning",
	})
	listOpts := &client.ListOptions{
		Namespace:     pod.Namespace,
		FieldSelector: fieldSelector,
	}

	if err := f.Client.List(ctx, eventList, listOpts); err != nil {
		log.Error(err, "Failed to list warning events for pod", "pod", pod.Name)
	} else {
		for _, event := range eventList.Items {
			if time.Since(event.LastTimestamp.Time) < since {
				message := fmt.Sprintf("Event: %s - %s", event.Reason, event.Message)
				addOrUpdateProblem(message, "Warning", event.LastTimestamp)
			}
		}
	}

	// --- 2. Get Recent Logs from Each Container and Scan for Errors ---
	for _, container := range pod.Spec.Containers {
		sinceSeconds := int64(since.Seconds())
		logOpts := &corev1.PodLogOptions{
			Container:    container.Name,
			SinceSeconds: &sinceSeconds,
		}

		req := f.Clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, logOpts)
		logStream, err := req.Stream(ctx)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.V(1).Info("Could not get logs, pod not found (likely terminated during reconcile)", "pod", pod.Name, "container", container.Name)
			} else {
				log.Error(err, "Failed to open log stream for container", "pod", pod.Name, "container", container.Name)
			}
			continue
		}
		defer logStream.Close()

		reader := bufio.NewReader(logStream)
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				lineLower := strings.ToLower(line)
				if strings.Contains(lineLower, "error") || strings.Contains(lineLower, "warn") || strings.Contains(lineLower, "panic") {
					const maxLogLineLength = 650
					trimmedLine := strings.TrimSpace(line)

					if len(trimmedLine) > maxLogLineLength {
						trimmedLine = trimmedLine[:maxLogLineLength] + " ... (truncated)"
					}

					message := fmt.Sprintf("Log in %s: %s", container.Name, trimmedLine)
					if strings.Contains(message, ignoredErrorSubstring) {
						continue
					}

					var problemType string
					if strings.Contains(lineLower, "panic") {
						problemType = "Panic"
					} else if strings.Contains(lineLower, "error") {
						problemType = "Error"
					} else {
						problemType = "Warning"
					}
					// Los logs no tienen un timestamp por línea fiable, así que usamos el tiempo actual de la observación.
					addOrUpdateProblem(message, problemType, metav1.Now())
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Error(err, "Error reading log stream for container", "pod", pod.Name, "container", container.Name)
				break
			}
		}
	}

	// Convertir el mapa de problemas a un slice para devolverlo.
	result := make([]accessmonitorv1alpha1.ProblemDetail, 0, len(problemsMap))
	for _, problem := range problemsMap {
		result = append(result, problem)
	}

	return result, nil
}
