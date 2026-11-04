package middleware

import "net/http"

// LimitBody caps every request body at max bytes. Reads past the cap fail with
// *http.MaxBytesError, so a flood of large bodies (notably on the
// unauthenticated provider webhook, which reads the whole body before the
// signature is checked) cannot exhaust memory.
func LimitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
