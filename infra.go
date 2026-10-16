package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	healthPath  = "/healthcheck"
	metricsPath = "/metrics"
	versionPath = "/version"
)

type healthResponse struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Checks  map[string]string `json:"checks"`
	Error   string            `json:"error,omitempty"`
}

func newInfraMux(db *sql.DB, version string, beforeMetrics ...func(context.Context)) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc(healthPath, healthHandler(db, version))
	mux.HandleFunc(versionPath, versionHandler(version))
	mux.Handle(metricsPath, metricsHandler(beforeMetrics...))
	return mux
}

func metricsHandler(beforeMetrics ...func(context.Context)) http.Handler {
	prometheusHandler := promhttp.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, callback := range beforeMetrics {
			if callback != nil {
				callback(r.Context())
			}
		}

		prometheusHandler.ServeHTTP(w, r)
	})
}

func healthHandler(db *sql.DB, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status := http.StatusOK
		response := healthResponse{
			Status:  "ok",
			Version: version,
			Checks:  map[string]string{"sqlite": "ok"},
		}
		if err := db.PingContext(ctx); err != nil {
			status = http.StatusServiceUnavailable
			response.Status = "error"
			response.Checks["sqlite"] = "error"
			response.Error = "sqlite unavailable"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response)
	}
}

func versionHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(version))
	}
}
