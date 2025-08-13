package discovery

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/logging/logadmin"
	"github.com/go-logr/logr"
	"google.golang.org/api/iterator"
)

// BrokerLogFinder is responsible for finding relevant logs from Kafka broker pods.
type BrokerLogFinder struct {
	client *logadmin.Client
	log    logr.Logger
}

// NewBrokerLogFinder creates a new instance of BrokerLogFinder using the logadmin client.
func NewBrokerLogFinder(ctx context.Context, projectID string, log logr.Logger) (*BrokerLogFinder, error) {
	client, err := logadmin.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create logadmin client: %w", err)
	}
	log.Info("Successfully created Google Cloud LogAdmin client", "projectID", projectID)
	return &BrokerLogFinder{client: client, log: log}, nil
}

// FindRelevantBrokerLogs queries Cloud Logging for entries related to a specific client identifier.
func (f *BrokerLogFinder) FindRelevantBrokerLogs(ctx context.Context, clientIdentifier string, timeWindow time.Duration) ([]string, error) {
	if clientIdentifier == "" {
		return nil, nil // No identifier to search for.
	}

	const maxLogsToFetch = 20
	filter := fmt.Sprintf(
		`resource.type="k8s_container" AND resource.labels.container_name="kafka" AND timestamp >= "%s" AND (textPayload:%q OR jsonPayload.message:%q) AND (severity >= WARNING) ORDER BY timestamp desc LIMIT %d`,
		time.Now().UTC().Add(-timeWindow).Format(time.RFC3339),
		clientIdentifier,
		clientIdentifier,
		maxLogsToFetch,
	)

	f.log.V(1).Info("Querying for broker logs with corrected filter", "filter", filter)

	var snippets []string
	it := f.client.Entries(ctx, logadmin.Filter(filter))

	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate log entries: %w", err)
		}

		var payloadStr string
		if textPayload, ok := entry.Payload.(string); ok {
			payloadStr = textPayload
		} else if jsonPayload, ok := entry.Payload.(map[string]interface{}); ok {
			if msg, ok := jsonPayload["message"].(string); ok {
				payloadStr = msg
			}
		}

		if payloadStr != "" {
			logLine := fmt.Sprintf("[%s] %s: %s", entry.Timestamp.Format(time.RFC3339), entry.Severity, payloadStr)
			snippets = append(snippets, logLine)
		}
	}

	for i, j := 0, len(snippets)-1; i < j; i, j = i+1, j-1 {
		snippets[i], snippets[j] = snippets[j], snippets[i]
	}

	// If no logs were found, return a message indicating that.
	if len(snippets) == 0 {
		f.log.Info("No relevant broker logs found after query", "clientIdentifier", clientIdentifier)
		return []string{"No relevant broker logs were found for the specified client identifier and time window."}, nil
	}

	f.log.Info("Found relevant broker logs", "count", len(snippets), "clientIdentifier", clientIdentifier)
	return snippets, nil
}

// Close must be called to release resources when the operator shuts down.
func (f *BrokerLogFinder) Close() error {
	f.log.Info("Closing Cloud LogAdmin client")
	return f.client.Close()
}
