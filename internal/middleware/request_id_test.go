package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

func TestRequestID_GeneratesWhenAbsent(t *testing.T) {
	var sawInContext string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := logger.RequestIDFromContext(r.Context())
		sawInContext = id
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	RequestID(next).ServeHTTP(rec, req)

	headerID := rec.Header().Get(RequestIDHeader)
	if headerID == "" {
		t.Fatal("expected X-Request-ID response header to be set")
	}
	if sawInContext == "" {
		t.Fatal("expected request ID to be present in the handler's context")
	}
	if sawInContext != headerID {
		t.Errorf("expected context ID (%q) to match response header ID (%q)", sawInContext, headerID)
	}
}

func TestRequestID_ReusesIncomingHeader(t *testing.T) {
	var sawInContext string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawInContext, _ = logger.RequestIDFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "upstream-provided-id")
	rec := httptest.NewRecorder()
	RequestID(next).ServeHTTP(rec, req)

	if sawInContext != "upstream-provided-id" {
		t.Errorf("expected upstream-provided-id to be reused, got %q", sawInContext)
	}
	if got := rec.Header().Get(RequestIDHeader); got != "upstream-provided-id" {
		t.Errorf("expected response header to echo upstream-provided-id, got %q", got)
	}
}

func TestRequestID_DifferentRequestsGetDifferentIDs(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	handler := RequestID(next)

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/", nil))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))

	id1 := rec1.Header().Get(RequestIDHeader)
	id2 := rec2.Header().Get(RequestIDHeader)
	if id1 == "" || id2 == "" {
		t.Fatal("expected both requests to get an ID")
	}
	if id1 == id2 {
		t.Errorf("expected distinct IDs for distinct requests, got the same value %q twice", id1)
	}
}
