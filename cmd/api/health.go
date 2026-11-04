package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

const (
	healthPingTimeout        = 2 * time.Second
	healthcheckClientTimeout = 3 * time.Second
)

// newHealthHandler reports readiness: 200 only when the database answers a
// ping. Failures are logged (with the request id) but never echoed, so the
// public endpoint cannot leak connection details. A nil checker fails closed.
func newHealthHandler(check func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if check == nil {
			logger.ErrorCtx(r.Context(), "health: no readiness checker configured")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()
		if err := check(ctx); err != nil {
			logger.ErrorfCtx(r.Context(), "health: database check failed: %v", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// runHealthcheck backs the `api -healthcheck` flag used by the Docker
// healthcheck: it asks the local server for /api/v1/health and returns the
// process exit code (0 healthy, 1 otherwise).
func runHealthcheck(port int, client *http.Client) int {
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port))
	if err != nil {
		logger.Errorf("healthcheck: %v", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Errorf("healthcheck: status %d", resp.StatusCode)
		return 1
	}
	return 0
}
