package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	amount := int64(50000)
	fp := &fakeProvider{
		name: "testprov",
		parsePayload: &domainProvider.ProviderWebhookPayload{
			ProviderName:      "testprov",
			ProviderReference: providerRef,
			Status:            "paid",
		},
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: "paid", Amount: &amount},
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

// TestWebhookHandler_HandleProviderWebhook_AcknowledgedAsIgnored covers the
// outcomes webhook_handler.go deliberately answers 200 "ignored" instead of
// an error: providers retry on 4xx/5xx, and none of these is fixed by a
// retry. The payment must stay pending in every case.
func TestWebhookHandler_HandleProviderWebhook_AcknowledgedAsIgnored(t *testing.T) {
	cases := []struct {
		name          string
		webhookStatus string
		webhookAmount *int64
		providerAmt   int64
	}{
		// ErrWebhookIgnored: only a settlement can move a payment.
		{"non-settlement event", "pending", nil, 50000},
		// ErrWebhookAmountMismatch: provider confirms 50000, webhook claims 1.
		{"webhook-only amount mismatch", "paid", int64Ptr(1), 50000},
		// ErrAmountMismatch: provider itself reports another amount.
		{"provider amount mismatch", "paid", nil, 49000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providerRef := "PROV-REF-IGN"
			fp := &fakeProvider{
				name: "testprov",
				parsePayload: &domainProvider.ProviderWebhookPayload{
					ProviderName:      "testprov",
					ProviderReference: providerRef,
					Status:            tc.webhookStatus,
					Amount:            tc.webhookAmount,
				},
				statusResp: &domainProvider.NormalizedPaymentStatus{Status: "paid", Amount: int64Ptr(tc.providerAmt)},
			}
			svc, paymentRepo := newTestWebhookService(fp)
			p := seedPendingPayment(t, paymentRepo, "testprov", providerRef)

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
			if got, _ := paymentRepo.GetByID(context.Background(), p.ID); got.Status != payment.StatusPending {
				t.Errorf("status = %q, want pending", got.Status)
			}
		})
	}
}

func int64Ptr(v int64) *int64 { return &v }

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

func TestWebhookHandler_HandleProviderWebhook_VerificationFailedIs502(t *testing.T) {
	providerRef := "PROV-REF-502"
	fp := &fakeProvider{
		name:         "testprov",
		parsePayload: &domainProvider.ProviderWebhookPayload{ProviderName: "testprov", ProviderReference: providerRef, Status: "paid"},
		statusErr:    errors.New("provider down"),
	}
	svc, paymentRepo := newTestWebhookService(fp)
	p := seedPendingPayment(t, paymentRepo, "testprov", providerRef)

	h := NewWebhookHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/testprov", strings.NewReader(`{}`))
	req = mux.SetURLVars(req, map[string]string{"providerName": "testprov"})
	rec := httptest.NewRecorder()
	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

// A webhook for a payment owned by another provider falls into the default
// 400 (not "ignored"): it is not a normal provider retry.
func TestWebhookHandler_HandleProviderWebhook_ProviderMismatchIs400(t *testing.T) {
	providerRef := "PROV-REF-OWNER"
	fp := &fakeProvider{
		name:         "testprov",
		parsePayload: &domainProvider.ProviderWebhookPayload{ProviderName: "testprov", ProviderReference: providerRef, Status: "paid"},
		statusResp:   &domainProvider.NormalizedPaymentStatus{Status: "paid", Amount: int64Ptr(50000)},
	}
	svc, paymentRepo := newTestWebhookService(fp)
	p := seedPendingPayment(t, paymentRepo, "cashi", providerRef) // owned by another provider

	h := NewWebhookHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/testprov", strings.NewReader(`{}`))
	req = mux.SetURLVars(req, map[string]string{"providerName": "testprov"})
	rec := httptest.NewRecorder()
	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := paymentRepo.GetByID(context.Background(), p.ID); got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

// TestWebhookHandler_HandleProviderWebhook_BodyTooLargeIs413 covers the body
// cap: the webhook is unauthenticated and reads the whole body before the
// signature check, so an oversized body must be refused with 413 and no
// internal error text.
func TestWebhookHandler_HandleProviderWebhook_BodyTooLargeIs413(t *testing.T) {
	svc, _ := newTestWebhookService(&fakeProvider{name: "testprov"})
	h := NewWebhookHandler(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/testprov", nil)
	req.Body = http.MaxBytesReader(rec, io.NopCloser(strings.NewReader(strings.Repeat("a", 64))), 16)
	req = mux.SetURLVars(req, map[string]string{"providerName": "testprov"})

	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "http: request body too large") {
		t.Errorf("response leaks internal error text: %s", rec.Body.String())
	}
}
