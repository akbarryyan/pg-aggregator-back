package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLimitBody(t *testing.T) {
	var readErr error
	h := LimitBody(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	t.Run("within limit is read fully", func(t *testing.T) {
		readErr = nil
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 1024))))
		if readErr != nil {
			t.Fatalf("unexpected error: %v", readErr)
		}
	})
	t.Run("over limit fails with MaxBytesError", func(t *testing.T) {
		readErr = nil
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 1025))))
		var mbe *http.MaxBytesError
		if !errors.As(readErr, &mbe) {
			t.Fatalf("want *http.MaxBytesError, got %v", readErr)
		}
	})
	t.Run("request without body is untouched", func(t *testing.T) {
		readErr = nil
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		if readErr != nil {
			t.Fatalf("unexpected error: %v", readErr)
		}
	})
}
