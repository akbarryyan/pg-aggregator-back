package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider/sandbox"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// newTestPaymentService wires PaymentService with in-memory fakes and a real
// sandbox adapter — mirrors how main.go wires it, minus Postgres.
func newTestPaymentService() (*service.PaymentService, *fakePaymentRepo) {
	svc, repo, _ := newTestPaymentServiceWithIdem()
	return svc, repo
}

func newTestPaymentServiceWithIdem() (*service.PaymentService, *fakePaymentRepo, *fakeIdempotencyRepo) {
	idem := newFakeIdempotencyRepo()
	paymentRepo := newFakePaymentRepo()
	router := providerPkg.NewProviderRouter()
	sb := sandbox.NewAdapter()
	router.RegisterProvider(sb)

	svc := service.NewPaymentService(
		paymentRepo,
		&fakeMerchantProviderConfigRepo{},
		newFakeWebhookEventRepo(),
		router,
		"http://localhost:8080",
	).WithSandboxProvider(sb).WithIdempotency(idem)

	return svc, paymentRepo, idem
}

func TestPaymentHandler_CreatePayment_SandboxSuccess(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	body := map[string]interface{}{
		"merchant_id":    uuid.New().String(),
		"amount":         50000,
		"payment_method": "qris",
		"description":    "Test payment",
		"environment":    "sandbox",
	}
	raw, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != payment.StatusPending {
		t.Errorf("expected status pending, got %v", resp["status"])
	}
	if resp["reference"] == "" || resp["reference"] == nil {
		t.Errorf("expected non-empty reference")
	}
	if fee, ok := resp["fee_amount"]; !ok || fee != float64(0) {
		t.Errorf("fee_amount must be present and 0 for a merchant-bearing payment, got %v (present=%v)", fee, ok)
	}
	if total, ok := resp["total_amount"]; !ok || total != float64(50000) {
		t.Errorf("total_amount must be present and equal amount 50000, got %v (present=%v)", total, ok)
	}
}

func TestPaymentHandler_CreatePayment_InvalidBody(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader([]byte("not-json")))
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPaymentHandler_CreatePayment_ValidationError(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	// amount missing/zero → payment.ErrInvalidAmount
	body := map[string]interface{}{
		"merchant_id":    uuid.New().String(),
		"payment_method": "qris",
		"description":    "Test payment",
		"environment":    "sandbox",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentHandler_CreatePayment_UnsupportedProviderInProduction(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	// production env, no provider registered for "qris" (router in
	// newTestPaymentService only registers sandbox, and CreatePayment
	// explicitly skips the sandbox provider for production traffic) →
	// ErrUnsupportedPaymentMethod / ErrProviderNotAvailable, mapped by
	// respondCreatePaymentError.
	body := map[string]interface{}{
		"merchant_id":    uuid.New().String(),
		"amount":         50000,
		"payment_method": "qris",
		"description":    "Test payment",
		"environment":    "production",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 400 or 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPaymentHandler_CreatePayment_APIKeyContextOverridesBody verifies that
// merchant_id/environment injected into the request context by
// MerchantAPIAuthMiddleware (API key auth) take precedence over the request
// body — this is the mechanism that stops a caller forging another
// merchant's ID (see payment_handler.go CreatePayment).
func TestPaymentHandler_CreatePayment_APIKeyContextOverridesBody(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	forgedMerchantID := uuid.New()
	realMerchantID := uuid.New()

	body := map[string]interface{}{
		"merchant_id":    forgedMerchantID.String(),
		"amount":         50000,
		"payment_method": "qris",
		"description":    "Test payment",
		"environment":    "production", // will be overridden to sandbox by context
	}
	raw, _ := json.Marshal(body)

	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, realMerchantID)
	ctx = context.WithValue(ctx, middleware.MerchantEnvironmentContextKey, "sandbox")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(raw)).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	id, _ := uuid.Parse(resp["id"].(string))
	stored, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("payment not persisted: %v", err)
	}
	if stored.MerchantID != realMerchantID {
		t.Errorf("expected merchant_id from context (%s) to win over body (%s), got %s", realMerchantID, forgedMerchantID, stored.MerchantID)
	}
}

func TestPaymentHandler_GetPayment_NotFound(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments/"+uuid.New().String(), nil)
	req = mux.SetURLVars(req, map[string]string{"id": uuid.New().String()})
	rec := httptest.NewRecorder()

	h.GetPayment(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestPaymentHandler_GetPayment_InvalidID(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments/not-a-uuid", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "not-a-uuid"})
	rec := httptest.NewRecorder()

	h.GetPayment(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// TestPaymentHandler_GetPayment_OwnershipEnforced verifies a merchant
// (identified via JWT context) cannot fetch another merchant's payment —
// this is the ownership check every merchant-scoped read relies on.
func TestPaymentHandler_GetPayment_OwnershipEnforced(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	owner := uuid.New()
	other := uuid.New()
	p := &payment.Payment{
		ID:            uuid.New(),
		Reference:     "PAY-TEST-1",
		MerchantID:    owner,
		Amount:        1000,
		Currency:      payment.CurrencyIDR,
		PaymentMethod: payment.PaymentMethodQRIS,
		Status:        payment.StatusPending,
		Environment:   payment.EnvironmentSandbox,
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("failed to seed payment: %v", err)
	}

	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, other)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments/"+p.ID.String(), nil).WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"id": p.ID.String()})
	rec := httptest.NewRecorder()

	h.GetPayment(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (payment hidden from non-owner), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentHandler_GetPaymentByReference_NotFound(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/payments/by-reference/does-not-exist", nil)
	req = mux.SetURLVars(req, map[string]string{"reference": "does-not-exist"})
	rec := httptest.NewRecorder()

	h.GetPaymentByReference(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestPaymentHandler_GetPaymentByReference_MissingReference(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/payments/by-reference/", nil)
	req = mux.SetURLVars(req, map[string]string{"reference": ""})
	rec := httptest.NewRecorder()

	h.GetPaymentByReference(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// postCreate posts a sandbox payment body with an optional Idempotency-Key.
func postCreate(h *PaymentHandler, amount int, key string, setKey bool, merchantID string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]interface{}{
		"merchant_id": merchantID, "amount": amount, "payment_method": "qris",
		"description": "idem", "environment": "sandbox",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(raw))
	if setKey {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.CreatePayment(rec, req)
	return rec
}

func TestPaymentHandler_CreatePayment_IdempotencyKeyReplays(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	m := uuid.New().String()

	first := postCreate(h, 50000, "key-1", true, m)
	second := postCreate(h, 50000, "key-1", true, m)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("want 201/201, got %d/%d: %s", first.Code, second.Code, second.Body.String())
	}
	var a, b map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if a["reference"] == nil || a["reference"] != b["reference"] {
		t.Errorf("replay must return the same reference: %v vs %v", a["reference"], b["reference"])
	}
	if first.Header().Get("Idempotent-Replayed") != "" {
		t.Error("the original response must not carry Idempotent-Replayed")
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("the replay must carry Idempotent-Replayed: true")
	}
}

func TestPaymentHandler_CreatePayment_IdempotencyKeyReusedWithDifferentBody(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	m := uuid.New().String()

	postCreate(h, 50000, "key-1", true, m)
	rec := postCreate(h, 60000, "key-1", true, m)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentHandler_CreatePayment_InvalidIdempotencyKey(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	m := uuid.New().String()

	for name, key := range map[string]string{"space": "has space", "too long": strings.Repeat("a", 129)} {
		rec := postCreate(h, 50000, key, true, m)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d: %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestPaymentHandler_CreatePayment_NoKeyCreatesDistinctPayments(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	m := uuid.New().String()

	a := postCreate(h, 50000, "", false, m)
	b := postCreate(h, 50000, "", false, m)
	if a.Code != http.StatusCreated || b.Code != http.StatusCreated {
		t.Fatalf("want 201/201, got %d/%d", a.Code, b.Code)
	}
	if len(repo.byID) != 2 {
		t.Errorf("want 2 payments, got %d", len(repo.byID))
	}
	if b.Header().Get("Idempotent-Replayed") != "" {
		t.Error("no key means no replay header")
	}
}

// postCreateRawKeys posts a sandbox payment with the given raw Idempotency-Key
// header values (none, one empty, several...).
func postCreateRawKeys(h *PaymentHandler, merchantID string, keys ...string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]interface{}{
		"merchant_id": merchantID, "amount": 50000, "payment_method": "qris",
		"description": "idem", "environment": "sandbox",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(raw))
	for _, k := range keys {
		req.Header.Add("Idempotency-Key", k)
	}
	rec := httptest.NewRecorder()
	h.CreatePayment(rec, req)
	return rec
}

func TestPaymentHandler_CreatePayment_EmptyOrRepeatedIdempotencyKeyIs400(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	m := uuid.New().String()

	cases := map[string][]string{
		"empty value":     {""},
		"two values":      {"key-1", "key-2"},
		"two equal":       {"key-1", "key-1"},
		"empty plus real": {"", "key-1"},
	}
	for name, keys := range cases {
		if rec := postCreateRawKeys(h, m, keys...); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d: %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(repo.byID) != 0 {
		t.Errorf("a rejected key must not create payments, got %d", len(repo.byID))
	}
	if rec := postCreateRawKeys(h, m); rec.Code != http.StatusCreated {
		t.Errorf("absent header: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRespondCreatePaymentError_FeeNotSupported(t *testing.T) {
	rec := httptest.NewRecorder()
	respondCreatePaymentError(rec, fmt.Errorf("provider cashi: %w", providerPkg.ErrFeeNotSupported))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Fee calculation is not available for this payment provider") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestPaymentHandler_GetPaymentByReference_CarriesFeeAndTotal(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fee, total float64
	}{{"customer bears fee", 15, 2015}, {"merchant bears fee", 0, 2000}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newTestPaymentService()
			h := NewPaymentHandler(svc, "http://localhost:3000")
			_ = repo.Create(context.Background(), &payment.Payment{
				Reference: "PAY-LOOKUP", MerchantID: uuid.New(), Amount: 2000, FeeAmount: int64(tc.fee),
				Currency: payment.CurrencyIDR, Status: payment.StatusPending,
				ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
			})

			req := httptest.NewRequest(http.MethodGet, "/api/v1/public/payments/by-reference/PAY-LOOKUP", nil)
			req = mux.SetURLVars(req, map[string]string{"reference": "PAY-LOOKUP"})
			rec := httptest.NewRecorder()
			h.GetPaymentByReference(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp map[string]interface{}
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if resp["amount"] != float64(2000) {
				t.Errorf("amount must stay nominal 2000, got %v", resp["amount"])
			}
			if fee, ok := resp["fee_amount"]; !ok || fee != tc.fee {
				t.Errorf("fee_amount = %v (present=%v), want %v", fee, ok, tc.fee)
			}
			if total, ok := resp["total_amount"]; !ok || total != tc.total {
				t.Errorf("total_amount = %v (present=%v), want %v", total, ok, tc.total)
			}
		})
	}
}
