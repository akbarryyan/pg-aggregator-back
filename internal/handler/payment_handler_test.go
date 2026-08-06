package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	).WithSandboxProvider(sb)

	return svc, paymentRepo
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
