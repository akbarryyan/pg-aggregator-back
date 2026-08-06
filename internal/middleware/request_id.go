package middleware

import (
	"net/http"

	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

// RequestIDHeader is both read (to respect an upstream-assigned ID, e.g.
// from a load balancer) and written (so the caller can correlate their
// request against server-side logs) on every response.
const RequestIDHeader = "X-Request-ID"

// RequestID assigns a correlation ID to every request — reused from the
// incoming X-Request-ID header when present, otherwise generated — and
// stores it in the request context via logger.WithRequestID. Handlers/
// services that log with the *Ctx logger functions (logger.InfofCtx etc.)
// automatically get this ID attached to every log line for that request,
// so a single request's log lines can be grepped/queried together (project
// backlog item #10). Mount this once at the top of the router so it
// applies to all routes.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = uuid.New().String()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(logger.WithRequestID(r.Context(), id)))
	})
}
