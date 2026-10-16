package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func InstrumentHTTP(
	next http.Handler,
	requests *prometheus.CounterVec,
	duration *prometheus.HistogramVec,
) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "handler not configured", http.StatusInternalServerError)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()

		next.ServeHTTP(recorder, r)

		labels := []string{resolveRouteLabel(r), r.Method, strconv.Itoa(recorder.status)}
		if requests != nil {
			requests.WithLabelValues(labels...).Inc()
		}

		if duration != nil {
			duration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func resolveRouteLabel(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}

	return "unknown"
}
