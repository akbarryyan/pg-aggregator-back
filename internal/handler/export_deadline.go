package handler

import (
	"net/http"
	"time"
)

// exportWriteTimeout lifts the server-wide 15s WriteTimeout for CSV exports
// (up to thousands of rows), which would otherwise be cut off mid-stream.
const exportWriteTimeout = 60 * time.Second

// extendWriteDeadline is best-effort: a ResponseWriter that doesn't support
// deadlines (wrappers, test recorders) returns an error we deliberately ignore.
func extendWriteDeadline(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
}
