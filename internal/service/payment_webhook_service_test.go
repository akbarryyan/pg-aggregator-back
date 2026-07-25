package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/google/uuid"
)

func newWebhookTestService() (*PaymentService, *fakePaymentRepo, *fakeWebhookEventRepo, *providerPkg.ProviderRouter) {
	paymentRepo := newFakePaymentRepo()
	configRepo := &fakeMerchantProviderConfigRepo{}
	webhookRepo := newFakeWebhookEventRepo()
	router := providerPkg.NewProviderRouter()

	svc := NewPaymentService(paymentRepo, configRepo, webhookRepo, router, "http://localhost:8080")
	return svc, paymentRepo, webhookRepo, router
}

// seedPayment inserts a payment directly into the fake repo, bypassing CreatePayment,
// so webhook tests can control status/provider_reference precisely.
func seedPayment(t *testing.T, repo *fakePaymentRepo, status, providerReference string) *payment.Payment {
	t.Helper()
	p := &payment.Payment{
		ID:                uuid.New(),
		Reference:         "PAY-" + providerReference,
		MerchantID:        uuid.New(),
		Amount:            15000,
		Currency:          payment.CurrencyIDR,
		PaymentMethod:     payment.PaymentMethodQRIS,
		ProviderName:      "cashi",
		ProviderReference: &providerReference,
		Status:            status,
		Description:       "seeded",
		Environment:       payment.EnvironmentProduction,
		ExpiresAt:         time.Now().Add(10 * time.Minute),
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("failed to seed payment: %v", err)
	}
	return p
}

func TestProcessWebhook_UnregisteredProvider(t *testing.T) {
	svc, _, webhookRepo, _ := newWebhookTestService()

	err := svc.ProcessWebhook(context.Background(), "unknown-provider", []byte(`{}`), "sig")
	if err == nil {
		t.Fatalf("expected error for unregistered provider")
	}
	if len(webhookRepo.events) != 1 {
		t.Fatalf("expected raw webhook event to be recorded regardless of outcome, got %d", len(webhookRepo.events))
	}
}

func TestProcessWebhook_InvalidSignatureRejected(t *testing.T) {
	svc, _, webhookRepo, router := newWebhookTestService()
	prov := &fakeProvider{name: "cashi", validateErr: providerPkg.ErrInvalidWebhookSignature}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "bad-sig")
	if err != payment.ErrWebhookValidationFailed {
		t.Fatalf("expected ErrWebhookValidationFailed, got %v", err)
	}

	var found *domainProvider.WebhookEvent
	for _, e := range webhookRepo.events {
		found = e
	}
	if found == nil || found.Status != "rejected" || found.IsProcessed {
		t.Fatalf("expected event recorded as rejected/unprocessed, got %+v", found)
	}
}

func TestProcessWebhook_TestEventIgnoredSilently(t *testing.T) {
	svc, _, webhookRepo, router := newWebhookTestService()
	prov := &fakeProvider{name: "cashi", parseErr: providerPkg.ErrTestWebhookEvent}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if err != nil {
		t.Fatalf("expected TEST- prefixed webhook to be ignored without error, got %v", err)
	}

	var found *domainProvider.WebhookEvent
	for _, e := range webhookRepo.events {
		found = e
	}
	if found == nil || found.Status != "ignored" {
		t.Fatalf("expected event recorded as ignored, got %+v", found)
	}
}

func TestProcessWebhook_MalformedPayloadRejected(t *testing.T) {
	svc, _, _, router := newWebhookTestService()
	prov := &fakeProvider{name: "cashi", parseErr: errors.New("malformed")}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`not-json`), "sig")
	if err != payment.ErrInvalidProviderReference {
		t.Fatalf("expected ErrInvalidProviderReference, got %v", err)
	}
}

func TestProcessWebhook_PaymentNotFoundForProviderReference(t *testing.T) {
	svc, _, _, router := newWebhookTestService()
	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "does-not-exist",
		Status:            "paid",
	}}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if err != payment.ErrPaymentNotFound {
		t.Fatalf("expected ErrPaymentNotFound, got %v", err)
	}
}

func TestProcessWebhook_TerminalPaymentRejected(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seedPayment(t, paymentRepo, payment.StatusPaid, "REF-TERMINAL")
	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "REF-TERMINAL",
		Status:            "paid",
	}}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if err != payment.ErrPaymentAlreadyTerminal {
		t.Fatalf("expected ErrPaymentAlreadyTerminal, got %v", err)
	}
}

func TestProcessWebhook_DuplicateWebhookRejected(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seedPayment(t, paymentRepo, payment.StatusPending, "REF-DUP")
	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "REF-DUP",
		Status:            "pending", // same as current status
	}}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if err != payment.ErrDuplicateWebhook {
		t.Fatalf("expected ErrDuplicateWebhook, got %v", err)
	}
}

func TestProcessWebhook_InvalidStatusTransitionRejected(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seedPayment(t, paymentRepo, payment.StatusPending, "REF-INVALID")
	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "REF-INVALID",
		Status:            "refunded", // not a valid target from pending
	}}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if err != payment.ErrInvalidStatusTransition {
		t.Fatalf("expected ErrInvalidStatusTransition, got %v", err)
	}
}

func TestProcessWebhook_SuccessUpdatesStatus(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-OK")
	paidAt := time.Now()
	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "REF-OK",
		Status:            payment.StatusPaid,
		PaidAt:            &paidAt,
	}}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := paymentRepo.GetByID(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("unexpected error fetching updated payment: %v", err)
	}
	if updated.Status != payment.StatusPaid {
		t.Errorf("expected payment status to become paid, got %q", updated.Status)
	}

	var found *domainProvider.WebhookEvent
	for _, e := range webhookRepo.events {
		found = e
	}
	if found == nil || !found.IsProcessed || found.Status != payment.StatusPaid {
		t.Fatalf("expected webhook event finalized as processed/paid, got %+v", found)
	}
}

func TestProcessWebhook_SuccessTriggersMerchantCallback(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()

	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-CB")

	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[seeded.MerchantID] = &merchant.Merchant{ID: seeded.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)

	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "REF-CB",
		Status:            payment.StatusPaid,
	}}
	router.RegisterProvider(prov)

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(callbackRepo.created) != 1 {
		t.Fatalf("expected exactly 1 callback delivery to be recorded, got %d", len(callbackRepo.created))
	}
	delivery := callbackRepo.created[0]
	if delivery.Status != merchant.CallbackStatusSuccess {
		t.Errorf("expected callback delivery to succeed, got status %q", delivery.Status)
	}
	if delivery.TargetURL != webhookURL {
		t.Errorf("expected callback sent to merchant webhook URL, got %q", delivery.TargetURL)
	}
	if delivery.EventType != "payment.paid" {
		t.Errorf("expected event type payment.paid, got %q", delivery.EventType)
	}
	if len(receivedBody) == 0 {
		t.Errorf("expected non-empty callback request body to be delivered")
	}
}

func TestExpirePayments_OnlyExpiresPendingPastDeadline(t *testing.T) {
	svc, paymentRepo, _, _ := newWebhookTestService()

	expiredPending := seedPayment(t, paymentRepo, payment.StatusPending, "REF-EXPIRE-1")
	expiredPending.ExpiresAt = time.Now().Add(-1 * time.Minute)
	paymentRepo.byID[expiredPending.ID] = expiredPending

	stillPending := seedPayment(t, paymentRepo, payment.StatusPending, "REF-EXPIRE-2")
	stillPending.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[stillPending.ID] = stillPending

	alreadyPaid := seedPayment(t, paymentRepo, payment.StatusPaid, "REF-EXPIRE-3")
	alreadyPaid.ExpiresAt = time.Now().Add(-1 * time.Minute)
	paymentRepo.byID[alreadyPaid.ID] = alreadyPaid

	if err := svc.ExpirePayments(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := paymentRepo.GetByID(context.Background(), expiredPending.ID)
	if got.Status != payment.StatusExpired {
		t.Errorf("expected past-deadline pending payment to expire, got %q", got.Status)
	}

	got, _ = paymentRepo.GetByID(context.Background(), stillPending.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("expected not-yet-expired payment to remain pending, got %q", got.Status)
	}

	got, _ = paymentRepo.GetByID(context.Background(), alreadyPaid.ID)
	if got.Status != payment.StatusPaid {
		t.Errorf("expected already-terminal payment to be left untouched, got %q", got.Status)
	}
}
