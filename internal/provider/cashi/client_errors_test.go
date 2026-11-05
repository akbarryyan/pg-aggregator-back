package cashi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

var providerSentinels = []error{
	providerPkg.ErrProviderAPIError,
	providerPkg.ErrProviderTimeout,
	providerPkg.ErrProviderNotAvailable,
}

// wantExactlyOne fails unless err matches `want` and none of the other
// provider sentinels: the HTTP mapping depends on there being exactly one.
func wantExactlyOne(t *testing.T, err, want error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error matching %v, got nil", want)
	}
	for _, s := range providerSentinels {
		if got, should := errors.Is(err, s), s == want; got != should {
			t.Errorf("errors.Is(err, %v) = %v, want %v (err: %v)", s, got, should, err)
		}
	}
}

func createReq() *provider.ProviderPaymentRequest {
	return &provider.ProviderPaymentRequest{InternalReference: "PAY-1", Amount: 2000}
}

func TestCreatePayment_ErrorClassification(t *testing.T) {
	respond := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"cashi 500", respond(500, `{"message":"boom"}`)},
		{"cashi 400", respond(400, `{"message":"bad"}`)},
		{"cashi 401", respond(401, `{"message":"bad key"}`)},
		{"success false", respond(200, `{"success":false,"message":"nope"}`)},
		{"missing order id", respond(200, `{"success":true,"expires_at":"2030-01-01 10:00:00"}`)},
		{"malformed json", respond(200, `this is not json`)},
		{"unparseable expires_at", respond(200, `{"success":true,"order_id":"X1","expires_at":"garbage"}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			adapter := NewCashiAdapter(server.URL, "key", testSecret)

			_, err := adapter.CreatePayment(context.Background(), createReq())
			wantExactlyOne(t, err, providerPkg.ErrProviderAPIError)
		})
	}
}

func TestCreatePayment_TimeoutIsClassifiedAsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the server notices the client disconnecting and
		// cancels r.Context(); otherwise the handler would sit out the 3s.
		_, _ = io.Copy(io.Discard, r.Body)
		select { // return as soon as the client gives up, so server.Close() does not block
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	adapter := NewCashiAdapter(server.URL, "key", testSecret)
	adapter.httpClient = &http.Client{Timeout: 50 * time.Millisecond}

	_, err := adapter.CreatePayment(context.Background(), createReq())
	wantExactlyOne(t, err, providerPkg.ErrProviderTimeout)
}

func TestCreatePayment_ContextDeadlineIsClassifiedAsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	adapter := NewCashiAdapter(server.URL, "key", testSecret)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := adapter.CreatePayment(ctx, createReq())
	wantExactlyOne(t, err, providerPkg.ErrProviderTimeout)
}

func TestCreatePayment_ConnectionRefusedIsNotAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close() // nothing listens on this address any more
	adapter := NewCashiAdapter(url, "key", testSecret)

	_, err := adapter.CreatePayment(context.Background(), createReq())
	wantExactlyOne(t, err, providerPkg.ErrProviderNotAvailable)
}

func TestCreatePayment_CancelledContextIsNotATimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	adapter := NewCashiAdapter(server.URL, "key", testSecret)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := adapter.CreatePayment(ctx, createReq())
	wantExactlyOne(t, err, providerPkg.ErrProviderNotAvailable)
}

func TestGetPaymentStatus_ErrorClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	defer server.Close()
	adapter := NewCashiAdapter(server.URL, "key", testSecret)

	_, err := adapter.GetPaymentStatus(context.Background(), "PAY-1")
	wantExactlyOne(t, err, providerPkg.ErrProviderAPIError)
}
