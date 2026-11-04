package handler

import (
	"errors"
	"net/http"
)

// respondValidationOrInternal sends err.Error() (HTTP 400) only when err is one
// of the known domain validation errors; anything else is logged by the caller
// and answered with a generic 500 so DB/provider text never reaches the client.
func respondValidationOrInternal(w http.ResponseWriter, err error, known ...error) {
	for _, k := range known {
		if errors.Is(err, k) {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	respondError(w, http.StatusInternalServerError, "Internal server error")
}
