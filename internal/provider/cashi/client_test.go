package cashi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

func newProviderRequest(internalRef string, amount int64) *provider.ProviderPaymentRequest {
	return &provider.ProviderPaymentRequest{
		InternalReference: internalRef,
		Amount:            amount,
		Currency:          "IDR",
		Description:       "test payment",
		ExpiresAt:         time.Now().Add(10 * time.Minute),
	}
}

const testSecret = "test-secret-key"

func sign(t *testing.T, payload []byte) string {
	t.Helper()
	h := hmac.New(sha256.New, []byte(testSecret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

func TestValidateWebhook(t *testing.T) {
	adapter := NewCashiAdapter("https://cashi.id", "api-key", testSecret)
	payload := []byte(`{"event":"PAYMENT_SETTLED","data":{"order_id":"INV-1","status":"SETTLED"}}`)

	t.Run("valid signature accepted", func(t *testing.T) {
		if err := adapter.ValidateWebhook(payload, sign(t, payload)); err != nil {
			t.Fatalf("expected valid signature to pass, got %v", err)
		}
	})

	t.Run("invalid signature rejected", func(t *testing.T) {
		err := adapter.ValidateWebhook(payload, "deadbeef")
		if !errors.Is(err, providerPkg.ErrInvalidWebhookSignature) {
			t.Fatalf("expected ErrInvalidWebhookSignature, got %v", err)
		}
	})

	t.Run("empty signature rejected", func(t *testing.T) {
		err := adapter.ValidateWebhook(payload, "")
		if !errors.Is(err, providerPkg.ErrInvalidWebhookSignature) {
			t.Fatalf("expected ErrInvalidWebhookSignature, got %v", err)
		}
	})

	t.Run("signature computed with wrong secret rejected", func(t *testing.T) {
		h := hmac.New(sha256.New, []byte("wrong-secret"))
		h.Write(payload)
		wrongSig := hex.EncodeToString(h.Sum(nil))
		err := adapter.ValidateWebhook(payload, wrongSig)
		if !errors.Is(err, providerPkg.ErrInvalidWebhookSignature) {
			t.Fatalf("expected ErrInvalidWebhookSignature, got %v", err)
		}
	})
}

func TestParseWebhook(t *testing.T) {
	adapter := NewCashiAdapter("https://cashi.id", "api-key", testSecret)

	t.Run("settled payment maps to paid with PaidAt set", func(t *testing.T) {
		payload := []byte(`{"event":"PAYMENT_SETTLED","data":{"order_id":"INV-1","status":"SETTLED","amount":15023}}`)
		got, err := adapter.ParseWebhook(payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Status != "paid" {
			t.Errorf("expected status paid, got %q", got.Status)
		}
		if got.PaidAt == nil {
			t.Errorf("expected PaidAt to be set")
		}
		if got.ProviderReference != "INV-1" {
			t.Errorf("expected provider reference INV-1, got %q", got.ProviderReference)
		}
		if got.Amount == nil || *got.Amount != 15023 {
			t.Errorf("expected amount 15023, got %v", got.Amount)
		}
	})

	t.Run("non-settled event stays pending", func(t *testing.T) {
		payload := []byte(`{"event":"PAYMENT_CREATED","data":{"order_id":"INV-2","status":"PENDING"}}`)
		got, err := adapter.ParseWebhook(payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Status != "pending" {
			t.Errorf("expected status pending, got %q", got.Status)
		}
		if got.PaidAt != nil {
			t.Errorf("expected PaidAt to be nil, got %v", got.PaidAt)
		}
	})

	t.Run("event with settled status but wrong event name stays pending", func(t *testing.T) {
		payload := []byte(`{"event":"SOMETHING_ELSE","data":{"order_id":"INV-3","status":"SETTLED"}}`)
		got, err := adapter.ParseWebhook(payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Status != "pending" {
			t.Errorf("expected status pending when event name doesn't match, got %q", got.Status)
		}
	})

	t.Run("TEST- prefixed order is ignored as test event", func(t *testing.T) {
		payload := []byte(`{"event":"PAYMENT_SETTLED","data":{"order_id":"TEST-123","status":"SETTLED"}}`)
		_, err := adapter.ParseWebhook(payload)
		if !errors.Is(err, providerPkg.ErrTestWebhookEvent) {
			t.Fatalf("expected ErrTestWebhookEvent, got %v", err)
		}
	})

	t.Run("malformed json returns error", func(t *testing.T) {
		_, err := adapter.ParseWebhook([]byte(`not json`))
		if err == nil {
			t.Fatalf("expected error for malformed payload")
		}
	})
}

func TestNormalizeStatus(t *testing.T) {
	adapter := NewCashiAdapter("https://cashi.id", "api-key", testSecret)

	if got := adapter.NormalizeStatus("SETTLED"); got != "paid" {
		t.Errorf("expected SETTLED to normalize to paid, got %q", got)
	}
	for _, s := range []string{"PENDING", "EXPIRED", "FAILED", "unknown"} {
		if got := adapter.NormalizeStatus(s); got != "pending" {
			t.Errorf("expected %q to normalize to pending, got %q", s, got)
		}
	}
}

func TestCreatePayment(t *testing.T) {
	t.Run("success maps response and sends api key header", func(t *testing.T) {
		var gotPath, gotAPIKey string
		var gotBody CreateOrderRequest

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAPIKey = r.Header.Get("x-api-key")
			_ = json.NewDecoder(r.Body).Decode(&gotBody)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(CreateOrderResponse{
				Success:     true,
				OrderID:     "INV-9921",
				Amount:      15023,
				CheckoutURL: "https://cashi.id/pay/INV-9921",
				QRUrl:       "data:image/png;base64,xxx",
				ExpiresAt:   "2024-01-01 10:00:00",
			})
		}))
		defer server.Close()

		adapter := NewCashiAdapter(server.URL, "my-api-key", testSecret)
		req := newProviderRequest("INV-9921", 15000)

		resp, err := adapter.CreatePayment(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if gotPath != "/api/create-order" {
			t.Errorf("expected path /api/create-order, got %q", gotPath)
		}
		if gotAPIKey != "my-api-key" {
			t.Errorf("expected x-api-key header to be sent, got %q", gotAPIKey)
		}
		if gotBody.OrderID != "INV-9921" || gotBody.Amount != 15000 {
			t.Errorf("unexpected request body: %+v", gotBody)
		}

		if resp.ProviderReference != "INV-9921" {
			t.Errorf("expected provider reference INV-9921, got %q", resp.ProviderReference)
		}
		if resp.Amount != 15023 {
			t.Errorf("expected final amount 15023 (with unique suffix), got %d", resp.Amount)
		}
		if resp.QRISData == nil || *resp.QRISData == "" {
			t.Errorf("expected QRISData to be set")
		}
		if resp.ExpiresAt.IsZero() {
			t.Errorf("expected ExpiresAt to be parsed")
		}
	})

	t.Run("provider success=false returns CashiError", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(CreateOrderResponse{
				Success: false,
				Message: "amount out of range",
			})
		}))
		defer server.Close()

		adapter := NewCashiAdapter(server.URL, "my-api-key", testSecret)
		req := newProviderRequest("INV-1", 100)

		_, err := adapter.CreatePayment(context.Background(), req)
		if err == nil {
			t.Fatalf("expected error when success=false")
		}
	})

	t.Run("http error status returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`invalid api key`))
		}))
		defer server.Close()

		adapter := NewCashiAdapter(server.URL, "bad-key", testSecret)
		req := newProviderRequest("INV-1", 15000)

		_, err := adapter.CreatePayment(context.Background(), req)
		if err == nil {
			t.Fatalf("expected error on HTTP 401")
		}
	})
}

func TestGetPaymentStatus(t *testing.T) {
	t.Run("success maps normalized status", func(t *testing.T) {
		var gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(CheckStatusResponse{
				Success: true,
				Status:  "SETTLED",
				Amount:  50078,
				OrderID: "INV-123",
			})
		}))
		defer server.Close()

		adapter := NewCashiAdapter(server.URL, "my-api-key", testSecret)
		status, err := adapter.GetPaymentStatus(context.Background(), "INV-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotPath != "/api/check-status/INV-123" {
			t.Errorf("expected path /api/check-status/INV-123, got %q", gotPath)
		}
		if status.Status != "paid" {
			t.Errorf("expected normalized status paid, got %q", status.Status)
		}
	})

	t.Run("provider success=false returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(CheckStatusResponse{Success: false, Message: "not found"})
		}))
		defer server.Close()

		adapter := NewCashiAdapter(server.URL, "my-api-key", testSecret)
		_, err := adapter.GetPaymentStatus(context.Background(), "unknown")
		if err == nil {
			t.Fatalf("expected error when success=false")
		}
	})
}
