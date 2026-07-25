package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/google/uuid"
)

func newCallbackTestService(t *testing.T) (*PaymentService, *fakeMerchantCallbackRepo) {
	t.Helper()
	paymentRepo := newFakePaymentRepo()
	configRepo := &fakeMerchantProviderConfigRepo{}
	webhookRepo := newFakeWebhookEventRepo()
	router := providerPkg.NewProviderRouter()

	callbackRepo := newFakeMerchantCallbackRepo()
	svc := NewPaymentService(paymentRepo, configRepo, webhookRepo, router, "http://localhost:8080").
		WithMerchantCallbackDeps(newFakeMerchantRepo(), callbackRepo)
	return svc, callbackRepo
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

	svc, callbackRepo := newCallbackTestService(t)

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

	svc, callbackRepo := newCallbackTestService(t)
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

	svc, callbackRepo := newCallbackTestService(t)
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
