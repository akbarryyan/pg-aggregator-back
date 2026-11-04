package handler

import (
	"errors"
	"net/http"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
)

const idempotencyKeyHeader = "Idempotency-Key"

// respondIdempotencyError maps idempotency errors to their HTTP status and
// reports whether it handled err.
func respondIdempotencyError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, idempotency.ErrKeyInvalid):
		respondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, idempotency.ErrKeyReused):
		respondError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, idempotency.ErrInProgress):
		respondError(w, http.StatusConflict, err.Error())
	default:
		return false
	}
	return true
}

// idempotencyKeyFromRequest reads the Idempotency-Key header. An absent header
// means "no key" (""), but a present-and-empty header or a repeated one is a
// client mistake that must not silently disable idempotency (spec 3.1), so it
// yields ErrKeyInvalid.
func idempotencyKeyFromRequest(r *http.Request) (string, error) {
	vals := r.Header.Values(idempotencyKeyHeader)
	switch {
	case len(vals) == 0:
		return "", nil
	case len(vals) > 1 || vals[0] == "":
		return "", idempotency.ErrKeyInvalid
	}
	return vals[0], nil
}
