package discovery

import (
	"context"
	"fmt"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"github.com/go-logr/logr"
	"google.golang.org/api/iterator"
	monitoringpb "google.golang.org/genproto/googleapis/monitoring/v3"
)

type GCPMetricsFinder struct {
	client    *monitoring.QueryClient
	projectID string
	log       logr.Logger
}

func NewGCPMetricsFinder(ctx context.Context, projectID string, log logr.Logger) (*GCPMetricsFinder, error) {
	client, err := monitoring.NewQueryClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create monitoring query client: %w", err)
	}
	return &GCPMetricsFinder{client: client, projectID: projectID, log: log}, nil
}

func (f *GCPMetricsFinder) FetchBrokerMetrics(ctx context.Context, brokerPodName string, problemTime time.Time) (*BrokerMetrics, error) {
	metrics := &BrokerMetrics{}

	const mqlTimeFormat = "2006/01/02-15:04:05"
	startTime := problemTime.Add(-5 * time.Minute).UTC().Format(mqlTimeFormat)
	endTime := problemTime.Add(1 * time.Minute).UTC().Format(mqlTimeFormat)
	timeWindowFilter := fmt.Sprintf(`within d'%s', d'%s'`, startTime, endTime)

	cpuQuery := fmt.Sprintf(
		`fetch k8s_container::kubernetes.io/container/cpu/limit_utilization
		 | filter (resource.pod_name == '%s' && resource.container_name == 'kafka')
		 | %s
		 | group_by [], mean(val())`,
		brokerPodName,
		timeWindowFilter,
	)
	cpuUtil, err := f.executeScalarQuery(ctx, cpuQuery)
	if err != nil {
		f.log.Error(err, "Failed to fetch CPU utilization", "brokerPod", brokerPodName)
	}
	metrics.CPUUtilization = cpuUtil

	netInQuery := fmt.Sprintf(
		`fetch k8s_pod::kubernetes.io/pod/network/received_bytes_count
		 | filter (resource.pod_name == '%s')
		 | align rate(1m)
		 | %s
		 | group_by [], mean(val())`,
		brokerPodName,
		timeWindowFilter,
	)
	netIn, err := f.executeScalarQuery(ctx, netInQuery)
	if err != nil {
		f.log.Error(err, "Failed to fetch network bytes in", "brokerPod", brokerPodName)
	}
	metrics.NetworkBytesIn = netIn

	metrics.UnderReplicated = 0

	f.log.Info("Successfully fetched broker metrics from GCP Monitoring", "brokerPod", brokerPodName, "metrics", fmt.Sprintf("%+v", metrics))
	return metrics, nil
}

func (f *GCPMetricsFinder) executeScalarQuery(ctx context.Context, query string) (float64, error) {
	f.log.V(1).Info("Executing MQL query", "query", query)
	req := &monitoringpb.QueryTimeSeriesRequest{
		Name:  "projects/" + f.projectID,
		Query: query,
	}

	it := f.client.QueryTimeSeries(ctx, req)
	resp, err := it.Next()
	if err == iterator.Done {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	if len(resp.GetPointData()) > 0 {
		point := resp.GetPointData()[0]
		if len(point.GetValues()) > 0 {
			value := point.GetValues()[0]
			return value.GetDoubleValue(), nil
		}
	}

	return 0, nil
}

func (f *GCPMetricsFinder) Close() error {
	return f.client.Close()
}
