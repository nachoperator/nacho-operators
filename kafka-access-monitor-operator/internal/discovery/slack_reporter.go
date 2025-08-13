package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	accessmonitorv1alpha1 "github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/api/v1alpha1"
)

// SlackMessage defines the JSON structure for a basic Slack message.
type SlackMessage struct {
	Text string `json:"text"`
}

// SlackReporter is responsible for sending formatted reports to a Slack webhook.
type SlackReporter struct {
	webhookURL string
	httpClient *http.Client
}

// NewSlackReporter creates a new instance of the SlackReporter.
func NewSlackReporter(webhookURL string) *SlackReporter {
	return &SlackReporter{
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// SendReport groups pods by namespace and sends paginated messages for large namespaces.
func (sr *SlackReporter) SendReport(ctx context.Context, pods []accessmonitorv1alpha1.PodAccessInfo) error {
	if len(pods) == 0 {
		return nil // Nothing to report
	}

	summaryMsg := fmt.Sprintf(":mag: *Informe de Acceso a Kafka - %s*\nSe han detectado *%d* clientes en total, detallados a continuación.", time.Now().Format("2006-01-02 15:04:05 MST"), len(pods))
	if err := sr.sendToSlack(ctx, summaryMsg); err != nil {
		return fmt.Errorf("failed to send summary message to Slack: %w", err)
	}

	podsByNamespace := make(map[string][]accessmonitorv1alpha1.PodAccessInfo)
	for _, pod := range pods {
		podsByNamespace[pod.Namespace] = append(podsByNamespace[pod.Namespace], pod)
	}

	for namespace, podsInNamespace := range podsByNamespace {
		sort.Slice(podsInNamespace, func(i, j int) bool {
			if podsInNamespace[i].Namespace != podsInNamespace[j].Namespace {
				return podsInNamespace[i].Namespace < podsInNamespace[j].Namespace
			}
			return podsInNamespace[i].ClientType < podsInNamespace[j].ClientType
		})

		const slackCharLimit = 3500
		var messageChunk strings.Builder
		page := 1

		header := fmt.Sprintf("\n\n*:k8s: Namespace: `%s` (%d clientes)*\n%s\n", namespace, len(podsInNamespace), strings.Repeat("─", 40))
		messageChunk.WriteString(header)

		for _, pod := range podsInNamespace {
			podReport := sr.formatSinglePod(pod)

			if messageChunk.Len()+len(podReport) > slackCharLimit {
				sr.sendChunk(ctx, messageChunk.String(), namespace, page)

				messageChunk.Reset()
				page++
				messageChunk.WriteString(fmt.Sprintf("*:k8s: Namespace: `%s` (Página %d)*\n%s\n", namespace, page, strings.Repeat("─", 40)))
			}
			messageChunk.WriteString(podReport)
		}

		if messageChunk.Len() > len(header) {
			sr.sendChunk(ctx, messageChunk.String(), namespace, page)
		}
	}

	return nil
}

// formatSinglePod formats the information for a single pod.
func (sr *SlackReporter) formatSinglePod(pod accessmonitorv1alpha1.PodAccessInfo) string {
	var sb strings.Builder
	var clientIcon string
	switch pod.ClientType {
	case "Consumer":
		clientIcon = ":large_blue_circle:"
	case "Producer":
		clientIcon = ":large_orange_circle:"
	default:
		clientIcon = ":white_circle:"
	}
	sb.WriteString(fmt.Sprintf("\n%s *%s*\n", clientIcon, pod.PodName))
	sb.WriteString("```\n")
	sb.WriteString(formatPodDetailsAsTable(pod))
	sb.WriteString("```")
	return sb.String()
}

// sendChunk is a helper to send a message and pause.
func (sr *SlackReporter) sendChunk(ctx context.Context, message, namespace string, page int) {
	time.Sleep(1 * time.Second)
	if err := sr.sendToSlack(ctx, message); err != nil {
		fmt.Printf("Error sending report for namespace %s (page %d): %v\n", namespace, page, err)
	}
}

// sendToSlack sends the final payload to the Slack webhook URL.
func (sr *SlackReporter) sendToSlack(ctx context.Context, msg string) error {
	payload := SlackMessage{Text: msg}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal slack message payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", sr.webhookURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("failed to create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := sr.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send message to slack: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to send message, received status code: %d, body: %s", resp.StatusCode, string(body))
	}
	return nil
}

// --- Helper Functions ---

// formatPodDetailsAsTable formats a pod's details into an aligned key-value string.
func formatPodDetailsAsTable(pod accessmonitorv1alpha1.PodAccessInfo) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("%-22s: %s\n", "Type", formatWithNA(pod.ClientType)))
	b.WriteString(fmt.Sprintf("%-22s: %s\n", "Group/Client ID", formatWithNA(coalesce(pod.GroupID, pod.ClientID))))
	b.WriteString(fmt.Sprintf("%-22s: %s\n", "Topics", formatWithNA(truncate(getTopicsAsString(pod.Topics), 70))))
	b.WriteString(fmt.Sprintf("%-22s: %s\n", "Brokers", formatWithNA(truncate(strings.Join(pod.DetectedBrokerEndpoints, ","), 70))))
	b.WriteString(fmt.Sprintf("%-22s: %s\n", "Schema Registry URL", formatWithNA(pod.SchemaRegistryURL)))
	b.WriteString(fmt.Sprintf("%-22s: %s", "Source", formatWithNA(pod.SourceOfEndpoint))) // No newline on the last item

	return b.String()
}

func truncate(s string, length int) string {
	if len(s) > length {
		return s[:length-3] + "..."
	}
	return s
}

func formatWithNA(value string) string {
	if value == "" {
		return "N/A"
	}
	return value
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func getTopicsAsString(topics []accessmonitorv1alpha1.TopicInfo) string {
	if len(topics) == 0 {
		return ""
	}
	var topicSubjects []string
	for _, t := range topics {
		topicSubjects = append(topicSubjects, t.Subject)
	}
	return strings.Join(topicSubjects, ",")
}
