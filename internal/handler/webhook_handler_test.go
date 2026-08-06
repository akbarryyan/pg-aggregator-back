package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// newTestWebhookService wires PaymentService + a fake provider registered
// under "testprov" so webhook validation/parsing behavior is fully
// controllable per test case.
func newTestWebhookService(fp *fakeProvider) (*service.PaymentService, *fakePaymentRepo) {
	paymentRepo := newFakePaymentRepo()
	router := providerPkg.NewProviderRouter()
	router.RegisterProvider(fp)

	svc := service.NewPaymentService(
		paymentRepo,
		&fakeMerchantProviderConfigRepo{},
		newFakeWebhookEventRepo(),
		router,
		"http://localhost:8080",
	)
	return svc, paymentRepo
}

func TestWebhookHandler_HandleProviderWebhook_Success(t *testing.T) {
	providerRef := "PROV-REF-1"
	fp := &fakeProvider{
		name: "testprov",
		parsePayload: &domainProvider.ProviderWebhookPayload{
			ProviderName:      "testprov",
			ProviderReference: providerRef,
			Status:            "paid",
		},
	}
	svc, paymentRepo := newTestWebhookService(fp)

	p := seedPendingPayment(t, paymentRepo, "testprov", providerRef)

	h := NewWebhookHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/testprov", strings.NewReader(`{"event":"paid"}`))
	req = mux.SetURLVars(req, map[string]string{"providerName": "testprov"})
	rec := httptest.NewRecorder()

	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := paymentRepo.GetByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("payment lookup failed: %v", err)
	}
	if updated.Status != "paid" {
		t.Errorf("expected status paid, got %s", updated.Status)
	}
}

func TestWebhookHandler_HandleProviderWebhook_MissingProviderName(t *testing.T) {
	svc, _ := newTestWebhookService(&fakeProvider{name: "testprov"})
	h := NewWebhookHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/", strings.NewReader(`{}`))
	req = mux.SetURLVars(req, map[string]string{"providerName": ""})
	rec := httptest.NewRecorder()

	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestWebhookHandler_HandleProviderWebhook_InvalidSignatureRejected(t *testing.T) {
	fp := &fakeProvider{
		name:        "testprov",
		validateErr: errors.New("invalid signature"),
	}
	svc, _ := newTestWebhookService(fp)
	h := NewWebhookHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/testprov", strings.NewReader(`{}`))
	req.Header.Set("x-gateway-signature", "bad-signature")
	req = mux.SetURLVars(req, map[string]string{"providerName": "testprov"})
	rec := httptest.NewRecorder()

	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid signature, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestWebhookHandler_HandleProviderWebhook_DuplicateIsIgnoredNot400 verifies
// the idempotency behavior explicitly carved out in webhook_handler.go: a
// duplicate/already-terminal webhook must respond 200 "ignored", not an
// error, since providers retry webhooks and a 4xx/5xx would trigger more
// retries.
func TestWebhookHandler_HandleProviderWebhook_DuplicateIsIgnoredNot400(t *testing.T) {
	providerRef := "PROV-REF-DUP"
	fp := &fakeProvider{
		name: "testprov",
		parsePayload: &domainProvider.ProviderWebhookPayload{
			ProviderName:      "testprov",
			ProviderReference: providerRef,
			Status:            "pending", // same as seeded status → duplicate webhook
		},
	}
	svc, paymentRepo := newTestWebhookService(fp)
	seedPendingPayment(t, paymentRepo, "testprov", providerRef)

	h := NewWebhookHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/testprov", strings.NewReader(`{}`))
	req = mux.SetURLVars(req, map[string]string{"providerName": "testprov"})
	rec := httptest.NewRecorder()

	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (ignored), got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["status"] != "ignored" {
		t.Errorf("expected status=ignored, got %v", resp)
	}
}

func TestWebhookHandler_HandleProviderWebhook_UnknownProvider(t *testing.T) {
	svc, _ := newTestWebhookService(&fakeProvider{name: "testprov"})
	h := NewWebhookHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/unknown", strings.NewReader(`{}`))
	req = mux.SetURLVars(req, map[string]string{"providerName": "unknown"})
	rec := httptest.NewRecorder()

	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unregistered provider, got %d: %s", rec.Code, rec.Body.String())
	}
}

// seedPendingPayment inserts a pending payment linked to providerRef so a
// webhook can be matched against it via GetByProviderReference.
func seedPendingPayment(t *testing.T, repo *fakePaymentRepo, providerName, providerRef string) *payment.Payment {
	t.Helper()
	p := &payment.Payment{
		ID:                uuid.New(),
		Reference:         "PAY-" + providerRef,
		MerchantID:        uuid.New(),
		Amount:            50000,
		Currency:          payment.CurrencyIDR,
		PaymentMethod:     payment.PaymentMethodQRIS,
		ProviderName:      providerName,
		ProviderReference: &providerRef,
		Status:            payment.StatusPending,
		Environment:       payment.EnvironmentProduction,
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("failed to seed payment: %v", err)
	}
	return p
}
