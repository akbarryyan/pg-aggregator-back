package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
)

func TestRespondInvalidCallbackURL(t *testing.T) {
	rec := httptest.NewRecorder()
	err := fmt.Errorf("create merchant: %w", netsafe.ErrInvalidCallbackURL)
	if !respondInvalidCallbackURL(rec, err) {
		t.Fatal("expected the error to be handled")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	if respondInvalidCallbackURL(rec, fmt.Errorf("other")) {
		t.Fatal("unrelated errors must not be handled")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("nothing should be written for unrelated errors, got %q", rec.Body.String())
	}
}
