package discovery

import (
	"context"
	"time"
)

type BrokerMetrics struct {
	CPUUtilization  float64
	NetworkBytesIn  float64
	NetworkBytesOut float64
	UnderReplicated int64
}

type BrokerMetricsFinder interface {
	FetchBrokerMetrics(ctx context.Context, brokerPodName string, problemTime time.Time) (*BrokerMetrics, error)
	Close() error
}
