package metrics

import (
	crosspostDomain "bloomary-api/internal/domain/crosspost"

	"github.com/prometheus/client_golang/prometheus"
)

// CrosspostDeliveries counts finished cross-post deliveries by network and status (security.md §5).
type CrosspostDeliveries struct{ counter *prometheus.CounterVec }

var _ crosspostDomain.DeliveryCounter = CrosspostDeliveries{}

func MustInitCrosspostDeliveriesMetric() CrosspostDeliveries {
	return CrosspostDeliveries{counter: initCounter("crosspost_deliveries_total", "Cross-post deliveries finished, by network and status", []string{"network", "status"})}
}

func (c CrosspostDeliveries) RecordDelivery(network crosspostDomain.Network, status crosspostDomain.DeliveryStatus) {
	c.counter.WithLabelValues(string(network), string(status)).Inc()
}
