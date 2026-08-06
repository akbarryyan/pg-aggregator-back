package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/google/uuid"
)

func newCallbackTestService(t *testing.T) (*PaymentService, *fakeMerchantRepo, *fakeMerchantCallbackRepo) {
	t.Helper()
	paymentRepo := newFakePaymentRepo()
	configRepo := &fakeMerchantProviderConfigRepo{}
	webhookRepo := newFakeWebhookEventRepo()
	router := providerPkg.NewProviderRouter()

	merchantRepo := newFakeMerchantRepo()
	callbackRepo := newFakeMerchantCallbackRepo()
	svc := NewPaymentService(paymentRepo, configRepo, webhookRepo, router, "http://localhost:8080").
		WithMerchantCallbackDeps(merchantRepo, callbackRepo)
	return svc, merchantRepo, callbackRepo
}

func seedCallbackDelivery(repo *fakeMerchantCallbackRepo, status, targetURL string, attemptNumber int, nextRetryAt *time.Time) *merchant.CallbackDelivery {
	d := &merchant.CallbackDelivery{
		ID:             uuid.New(),
		PaymentID:      uuid.New(),
		MerchantID:     uuid.New(),
		EventType:      "payment.paid",
		TargetURL:      targetURL,
		RequestPayload: map[string]interface{}{"status": "paid"},
		AttemptNumber:  attemptNumber,
		Status:         status,
		NextRetryAt:    nextRetryAt,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	repo.byID[d.ID] = d
	return d
}

func TestRetryDueMerchantCallbacks_OnlyRetriesDueFailedDeliveries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc, _, callbackRepo := newCallbackTestService(t)

	past := time.Now().UTC().Add(-1 * time.Minute)
	future := time.Now().UTC().Add(10 * time.Minute)

	due := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &past)
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &future) // not due yet
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSuccess, server.URL, 1, nil)    // already succeeded
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 5, nil)     // capped, no schedule

	count, err := svc.RetryDueMerchantCallbacks(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 due delivery to be retried, got %d", count)
	}
	if len(callbackRepo.created) != 1 {
		t.Fatalf("expected exactly 1 new retry attempt to be created, got %d", len(callbackRepo.created))
	}
	if callbackRepo.created[0].AttemptNumber != due.AttemptNumber+1 {
		t.Errorf("expected new attempt number %d, got %d", due.AttemptNumber+1, callbackRepo.created[0].AttemptNumber)
	}
}

func TestRetryDueMerchantCallbacks_ClearsSourceRetrySchedule(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // retry itself also fails
	}))
	defer server.Close()

	svc, _, callbackRepo := newCallbackTestService(t)
	past := time.Now().UTC().Add(-1 * time.Minute)
	due := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &past)

	if _, err := svc.RetryDueMerchantCallbacks(context.Background(), 10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := callbackRepo.GetByID(context.Background(), due.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.NextRetryAt != nil {
		t.Fatalf("expected source delivery's NextRetryAt to be cleared after spawning a retry, still got %v", got.NextRetryAt)
	}

	// A second scheduler tick must not pick the same source delivery up again.
	count, err := svc.RetryDueMerchantCallbacks(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 due deliveries on the next tick (no duplicate retries), got %d", count)
	}
}

func TestRetryMerchantCallback_StopsSchedulingAfterMaxAttempts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	svc, _, callbackRepo := newCallbackTestService(t)
	past := time.Now().UTC().Add(-1 * time.Minute)
	// Already at the last allowed attempt before the cap.
	due := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, maxCallbackAttempts, &past)

	retried, err := svc.RetryMerchantCallback(context.Background(), due.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retried.NextRetryAt != nil {
		t.Errorf("expected no further retry to be scheduled once maxCallbackAttempts is reached, got %v", retried.NextRetryAt)
	}
	if retried.Status != merchant.CallbackStatusFailed {
		t.Errorf("expected retry attempt against a failing endpoint to be recorded as failed, got %q", retried.Status)
	}
}

func TestRetryDueMerchantCallbacks_NoopWithoutCallbackDeps(t *testing.T) {
	paymentRepo := newFakePaymentRepo()
	configRepo := &fakeMerchantProviderConfigRepo{}
	webhookRepo := newFakeWebhookEventRepo()
	router := providerPkg.NewProviderRouter()
	svc := NewPaymentService(paymentRepo, configRepo, webhookRepo, router, "http://localhost:8080")
	// Intentionally not calling WithMerchantCallbackDeps.

	count, err := svc.RetryDueMerchantCallbacks(context.Background(), 10)
	if err != nil {
		t.Fatalf("expected no error when callback deps are not wired, got %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0 when callback deps are not wired, got %d", count)
	}
}

func TestEnsureMerchantWebhookSecret_GeneratesOnceAndPersists(t *testing.T) {
	svc, merchantRepo, _ := newCallbackTestService(t)
	merchantID := uuid.New()
	merchantRepo.byID[merchantID] = &merchant.Merchant{ID: merchantID}

	first, err := svc.EnsureMerchantWebhookSecret(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first == "" {
		t.Fatalf("expected a non-empty generated secret")
	}
	if merchantRepo.byID[merchantID].WebhookSecret == nil || *merchantRepo.byID[merchantID].WebhookSecret != first {
		t.Fatalf("expected generated secret to be persisted on the merchant record")
	}

	second, err := svc.EnsureMerchantWebhookSecret(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second != first {
		t.Errorf("expected EnsureMerchantWebhookSecret to be idempotent, got %q then %q", first, second)
	}
}

func TestRegenerateMerchantWebhookSecret_ProducesDifferentSecret(t *testing.T) {
	svc, merchantRepo, _ := newCallbackTestService(t)
	merchantID := uuid.New()
	merchantRepo.byID[merchantID] = &merchant.Merchant{ID: merchantID}

	first, err := svc.EnsureMerchantWebhookSecret(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := svc.RegenerateMerchantWebhookSecret(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second == first {
		t.Errorf("expected regenerate to produce a different secret, got the same value twice")
	}
	if merchantRepo.byID[merchantID].WebhookSecret == nil || *merchantRepo.byID[merchantID].WebhookSecret != second {
		t.Errorf("expected regenerated secret to overwrite the persisted one")
	}
}

func TestExecuteCallbackDelivery_SignsPayloadWithHMAC(t *testing.T) {
	var gotSignature string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSignature = r.Header.Get("X-PG-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc, merchantRepo, _ := newCallbackTestService(t)
	merchantID := uuid.New()
	merchantRepo.byID[merchantID] = &merchant.Merchant{ID: merchantID}

	payload := &payment.Payment{
		ID:          uuid.New(),
		Reference:   "PAY-SIGN-1",
		MerchantID:  merchantID,
		Status:      payment.StatusPaid,
		Amount:      15000,
		Currency:    payment.CurrencyIDR,
		CallbackURL: strPtr(server.URL),
	}
	svc.NotifyMerchantPaymentEvent(context.Background(), payload, "payment.paid")

	if gotSignature == "" {
		t.Fatalf("expected X-PG-Signature header to be set")
	}

	secret, err := svc.EnsureMerchantWebhookSecret(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(gotBody)
	want := hex.EncodeToString(h.Sum(nil))

	if gotSignature != want {
		t.Errorf("signature mismatch: server received %q, recomputed %q from the merchant's own secret", gotSignature, want)
	}
}

func strPtr(s string) *string { return &s }

// TestRetryDueMerchantCallbacks_ReusesRowsAndCachesWebhookSecret covers
// project backlog item #6 (N+1 audit finding #4): RetryDueMerchantCallbacks
// used to call RetryMerchantCallback(ctx, id) per due row, which re-fetched
// each delivery by ID even though ListDueForRetry had already loaded it,
// and re-fetched the merchant (for its webhook secret) on every single
// delivery even when several due deliveries belonged to the same merchant.
// It now reuses the already-fetched rows and shares one secret cache
// across the batch.
func TestRetryDueMerchantCallbacks_ReusesRowsAndCachesWebhookSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc, merchantRepo, callbackRepo := newCallbackTestService(t)

	sameMerchant := uuid.New()
	merchantRepo.byID[sameMerchant] = &merchant.Merchant{ID: sameMerchant, WebhookSecret: strPtr("existing-secret")}

	past := time.Now().UTC().Add(-1 * time.Minute)
	d1 := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &past)
	d1.MerchantID = sameMerchant
	d2 := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &past)
	d2.MerchantID = sameMerchant

	callbackRepo.getByIDCalls = 0
	merchantRepo.getByIDCalls = 0

	count, err := svc.RetryDueMerchantCallbacks(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 due deliveries retried, got %d", count)
	}

	if callbackRepo.getByIDCalls != 0 {
		t.Errorf("expected 0 callbackRepo.GetByID calls (rows already fetched via ListDueForRetry), got %d", callbackRepo.getByIDCalls)
	}
	if merchantRepo.getByIDCalls != 1 {
		t.Errorf("expected exactly 1 merchantRepo.GetByID call (secret cached across both deliveries for the same merchant), got %d", merchantRepo.getByIDCalls)
	}
}
