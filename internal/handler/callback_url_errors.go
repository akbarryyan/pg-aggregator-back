package handler

import (
	"errors"
	"net/http"

	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
)

// respondInvalidCallbackURL writes a 400 and returns true when err is a
// callback/webhook URL validation failure. The error text names the broken
// rule but never echoes the URL.
func respondInvalidCallbackURL(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, netsafe.ErrInvalidCallbackURL) {
		return false
	}
	respondError(w, http.StatusBadRequest, err.Error())
	return true
}
