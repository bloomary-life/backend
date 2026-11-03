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

// CrosspostKeys reports sealed records per keyring key ID and the publish mode.
type CrosspostKeys struct{ records *prometheus.GaugeVec }

var _ crosspostDomain.KeyCounter = CrosspostKeys{}

func MustInitCrosspostKeysMetric() CrosspostKeys {
	return CrosspostKeys{records: initGaugeVec("crosspost_sealed_records", "Sealed credential records per keyring key ID", []string{"key_id"})}
}

func (c CrosspostKeys) RecordKeyCounts(counts map[string]int) {
	c.records.Reset()
	for keyID, n := range counts {
		c.records.WithLabelValues(keyID).Set(float64(n))
	}
}

// MustInitPublishModeMetric sets bloomary_publish_mode{mode} to 1 for the mode in force (logged at start too).
func MustInitPublishModeMetric(mode crosspostDomain.PublishMode) {
	initGaugeVec("publish_mode", "Cross-post publish mode in force (dry_run, live_allowlist, live)", []string{"mode"}).WithLabelValues(string(mode)).Set(1)
}
