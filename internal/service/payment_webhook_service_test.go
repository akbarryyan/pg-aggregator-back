package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		CreatedAt:         time.Now(),
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

	found := lastEvent(webhookRepo)
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

	found := lastEvent(webhookRepo)
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
	seedPayment(t, paymentRepo, payment.StatusFailed, "REF-TERMINAL")
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

func TestProcessWebhook_NonSettlementIgnored(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seedPayment(t, paymentRepo, payment.StatusPending, "REF-DUP")
	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "REF-DUP",
		Status:            "pending", // same as current status
	}}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if err != payment.ErrWebhookIgnored {
		t.Fatalf("expected ErrWebhookIgnored, got %v", err)
	}
}

func TestProcessWebhook_UnknownStatusIgnored(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seedPayment(t, paymentRepo, payment.StatusPending, "REF-INVALID")
	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "REF-INVALID",
		Status:            "refunded", // not a valid target from pending
	}}
	router.RegisterProvider(prov)

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if err != payment.ErrWebhookIgnored {
		t.Fatalf("expected ErrWebhookIgnored, got %v", err)
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
	},
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	}
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

	found := lastEvent(webhookRepo)
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
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo).useLoopbackCallbacks()

	prov := &fakeProvider{name: "cashi", parseResp: &domainProvider.ProviderWebhookPayload{
		ProviderReference: "REF-CB",
		Status:            payment.StatusPaid,
	},
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	}
	router.RegisterProvider(prov)

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(callbackRepo.created) != 1 {
		t.Fatalf("expected exactly 1 callback delivery to be recorded, got %d", len(callbackRepo.created))
	}
	// The webhook only queues the callback; the delivery worker sends it.
	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error delivering callbacks: %v", err)
	}
	// The worker sends a claimed copy and UpdateResult replaces the stored
	// row, so read the outcome back by ID.
	delivery := callbackRepo.byID[callbackRepo.created[0].ID]
	if delivery.Status != merchant.CallbackStatusSuccess {
		t.Errorf("expected callback delivery to succeed, got status %q", delivery.Status)
	}
	if delivery.TargetURL != webhookURL {
		t.Errorf("expected callback sent to merchant webhook URL, got %q", delivery.TargetURL)
	}
	if delivery.EventType != "payment.paid" {
		t.Errorf("expected event type payment.paid, got %q", delivery.EventType)
	}
	if delivery.RequestPayload["late_payment"] != false {
		t.Errorf("expected late_payment=false, got %v", delivery.RequestPayload["late_payment"])
	}
	if len(receivedBody) == 0 {
		t.Errorf("expected non-empty callback request body to be delivered")
	}
}

func TestExpirePayments_OnlyExpiresPendingPastDeadline(t *testing.T) {
	svc, paymentRepo, _, _ := newWebhookTestService()

	expiredPending := seedPayment(t, paymentRepo, payment.StatusPending, "REF-EXPIRE-1")
	expiredPending.ExpiresAt = time.Now().Add(-10 * time.Minute) // beyond expireGracePeriod
	paymentRepo.byID[expiredPending.ID] = expiredPending

	stillPending := seedPayment(t, paymentRepo, payment.StatusPending, "REF-EXPIRE-2")
	stillPending.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[stillPending.ID] = stillPending

	alreadyPaid := seedPayment(t, paymentRepo, payment.StatusPaid, "REF-EXPIRE-3")
	alreadyPaid.ExpiresAt = time.Now().Add(-10 * time.Minute) // beyond expireGracePeriod
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

// A payment only just past its deadline is left alone for expireGracePeriod,
// so a last-second payment isn't reported expired and then paid.
func TestExpirePayments_GracePeriod(t *testing.T) {
	cases := []struct {
		name       string
		pastExpiry time.Duration
		want       string
	}{
		{"1 minute past expiry stays pending", time.Minute, payment.StatusPending},
		{"6 minutes past expiry is expired", 6 * time.Minute, payment.StatusExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, paymentRepo, _, _ := newWebhookTestService()
			p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-GRACE")
			p.ExpiresAt = time.Now().Add(-tc.pastExpiry)
			paymentRepo.byID[p.ID] = p

			if err := svc.ExpirePayments(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got, _ := paymentRepo.GetByID(context.Background(), p.ID); got.Status != tc.want {
				t.Errorf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

func paidWebhook(ref string, amount *int64) *domainProvider.ProviderWebhookPayload {
	return &domainProvider.ProviderWebhookPayload{ProviderReference: ref, Status: payment.StatusPaid, Amount: amount}
}

// lastEvent returns the most recently recorded webhook event.
func lastEvent(repo *fakeWebhookEventRepo) *domainProvider.WebhookEvent {
	return repo.last()
}

func TestProcessWebhook_AlreadyPaidIsDuplicate(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seedPayment(t, paymentRepo, payment.StatusPaid, "REF-PAID")
	router.RegisterProvider(&fakeProvider{name: "cashi", parseResp: paidWebhook("REF-PAID", nil)})

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != payment.ErrDuplicateWebhook {
		t.Fatalf("expected ErrDuplicateWebhook, got %v", err)
	}
}

func TestProcessWebhook_ProviderAmountMismatchLeavesPending(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-MM")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-MM", ptrInt64(1500)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(1500)},
	})

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("expected ErrAmountMismatch, got %v", err)
	}
	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	e := lastEvent(webhookRepo)
	if e == nil || e.ProcessingError == nil || !strings.HasPrefix(*e.ProcessingError, "amount_mismatch:") {
		t.Fatalf("expected event flagged amount_mismatch, got %+v", e)
	}
}

func TestProcessWebhook_WebhookAmountMismatchRejected(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-WMM")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-WMM", ptrInt64(1)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	})

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if !errors.Is(err, payment.ErrWebhookAmountMismatch) || errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("expected ErrWebhookAmountMismatch (not ErrAmountMismatch), got %v", err)
	}
	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	e := lastEvent(webhookRepo)
	if e == nil || e.Status != "rejected" || e.ProcessingError == nil || !strings.HasPrefix(*e.ProcessingError, "webhook_amount_mismatch:") {
		t.Fatalf("expected rejected event flagged webhook_amount_mismatch, got %+v", e)
	}
}

// A webhook-only mismatch rejects the webhook but must not take the payment
// out of reconcile: check-status (which agrees with the stored amount) can
// still settle it. The SQL exclusion is covered by the real-PG test.
func TestProcessWebhook_WebhookAmountMismatchStillReconciles(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-WMM-REC")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-WMM-REC", ptrInt64(1)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	})

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); !errors.Is(err, payment.ErrWebhookAmountMismatch) {
		t.Fatalf("expected ErrWebhookAmountMismatch, got %v", err)
	}
	if err := svc.ReconcileRecentPayments(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), seeded.ID); got.Status != payment.StatusPaid {
		t.Fatalf("status = %q, want paid via reconcile", got.Status)
	}
}

// statusCheckFailures are the failure kinds a provider status check can
// return. The webhook and reconcile paths must treat all of them identically:
// the distinction (timeout / unavailable / API error) matters only to the
// create-payment HTTP mapping.
func statusCheckFailures() map[string]error {
	return map[string]error{
		"plain error":        errors.New("boom"),
		"timeout":            fmt.Errorf("x: %w", providerPkg.ErrProviderTimeout),
		"not available":      fmt.Errorf("x: %w", providerPkg.ErrProviderNotAvailable),
		"provider API error": fmt.Errorf("x: %w", providerPkg.ErrProviderAPIError),
	}
}

func TestProcessWebhook_VerificationFailures(t *testing.T) {
	cases := map[string]*fakeProvider{
		"provider not settled": {name: "cashi", parseResp: paidWebhook("REF-VF", nil),
			statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending, Amount: ptrInt64(15000)}},
	}
	for kind, statusErr := range statusCheckFailures() {
		cases["provider call fails: "+kind] = &fakeProvider{name: "cashi", parseResp: paidWebhook("REF-VF", nil), statusErr: statusErr}
	}
	for name, prov := range cases {
		t.Run(name, func(t *testing.T) {
			svc, paymentRepo, webhookRepo, router := newWebhookTestService()
			seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-VF")
			router.RegisterProvider(prov)

			err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
			if !errors.Is(err, payment.ErrProviderVerificationFailed) {
				t.Fatalf("expected ErrProviderVerificationFailed, got %v", err)
			}
			got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
			if got.Status != payment.StatusPending {
				t.Errorf("status = %q, want pending", got.Status)
			}
			if e := lastEvent(webhookRepo); e == nil || e.Status != "verification_failed" {
				t.Errorf("expected event status verification_failed, got %+v", e)
			}
		})
	}
}

func TestProcessWebhook_ExpiredThenSettledIsLatePayment(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	seeded := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-LATE")
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[seeded.MerchantID] = &merchant.Merchant{ID: seeded.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)
	settled := time.Date(2026, 10, 30, 6, 30, 14, 0, time.UTC)
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  &domainProvider.ProviderWebhookPayload{ProviderReference: "REF-LATE", Status: payment.StatusPaid, PaidAt: &settled},
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	})

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPaid || got.PaidAt == nil || !got.PaidAt.Equal(settled) {
		t.Fatalf("expected paid at %v, got %q at %v", settled, got.Status, got.PaidAt)
	}
	if e := lastEvent(webhookRepo); e == nil || e.EventType != "payment.paid.late" || !e.IsProcessed {
		t.Errorf("expected processed payment.paid.late event, got %+v", e)
	}
	if len(callbackRepo.created) != 1 || callbackRepo.created[0].EventType != "payment.paid" ||
		callbackRepo.created[0].RequestPayload["late_payment"] != true {
		t.Fatalf("expected one payment.paid callback with late_payment=true, got %+v", callbackRepo.created)
	}
}

// Two deliveries of the same settlement racing each other: exactly one
// wins the guarded update, so the merchant hears about it exactly once.
func TestProcessWebhook_ConcurrentDuplicatesNotifyOnce(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RACE")
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[seeded.MerchantID] = &merchant.Merchant{ID: seeded.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-RACE", nil),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if err != payment.ErrDuplicateWebhook {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one webhook to win, got %d", successes)
	}
	callbackRepo.mu.Lock()
	defer callbackRepo.mu.Unlock()
	if len(callbackRepo.created) != 1 {
		t.Fatalf("expected exactly one merchant callback, got %d", len(callbackRepo.created))
	}
}

// Deterministic version of the race: another caller settles the payment
// between our precheck and our guarded update, so the update loses.
func TestProcessWebhook_LostRaceToConcurrentSettlement(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-LOST")
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[seeded.MerchantID] = &merchant.Merchant{ID: seeded.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-LOST", nil),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
		onStatus: func() {
			now := time.Now()
			if _, err := paymentRepo.UpdateStatus(context.Background(), seeded.ID, payment.StatusPaid, &now); err != nil {
				t.Errorf("simulated winner failed: %v", err)
			}
		},
	})

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != payment.ErrDuplicateWebhook {
		t.Fatalf("expected ErrDuplicateWebhook, got %v", err)
	}
	if len(callbackRepo.created) != 0 {
		t.Errorf("loser must not notify the merchant, got %d callbacks", len(callbackRepo.created))
	}
	e := lastEvent(webhookRepo)
	if e == nil || e.IsProcessed || e.ProcessingError == nil || *e.ProcessingError != payment.ErrDuplicateWebhook.Error() {
		t.Fatalf("expected unprocessed event flagged duplicate, got %+v", e)
	}
}

func TestExpirePayments_NotifiesMerchant(t *testing.T) {
	svc, paymentRepo, _, _ := newWebhookTestService()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-EXP-CB")
	p.ExpiresAt = time.Now().Add(-10 * time.Minute) // beyond expireGracePeriod
	paymentRepo.byID[p.ID] = p
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[p.MerchantID] = &merchant.Merchant{ID: p.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)

	if err := svc.ExpirePayments(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(callbackRepo.created) != 1 || callbackRepo.created[0].EventType != "payment.expired" {
		t.Fatalf("expected one payment.expired callback, got %+v", callbackRepo.created)
	}
}

// staleExpiredListRepo returns a pending snapshot for a payment that has
// meanwhile been settled — the race the guarded UPDATE resolves.
type staleExpiredListRepo struct {
	*fakePaymentRepo
	stale []*payment.Payment
}

func (r *staleExpiredListRepo) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]*payment.Payment, error) {
	return r.stale, nil
}

func TestExpirePayments_LostRaceSendsNoCallback(t *testing.T) {
	base := newFakePaymentRepo()
	p := seedPayment(t, base, payment.StatusPaid, "REF-EXP-RACE")
	stale := *p
	stale.Status = payment.StatusPending
	repo := &staleExpiredListRepo{fakePaymentRepo: base, stale: []*payment.Payment{&stale}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	svc := NewPaymentService(repo, &fakeMerchantProviderConfigRepo{}, newFakeWebhookEventRepo(), providerPkg.NewProviderRouter(), "http://localhost:8080")
	// The merchant has a webhook URL, so a callback WOULD be recorded if
	// ExpirePayments notified after losing the race — keeps the test honest.
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[p.MerchantID] = &merchant.Merchant{ID: p.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)

	if err := svc.ExpirePayments(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(callbackRepo.created) != 0 {
		t.Fatalf("expected no callback for a payment another caller settled, got %d", len(callbackRepo.created))
	}
	if got, _ := base.GetByID(context.Background(), p.ID); got.Status != payment.StatusPaid {
		t.Errorf("status = %q, want paid", got.Status)
	}
}

// newCancelAwareMerchant wires a merchant webhook plus callback/event fakes
// that fail on a cancelled context, as the real repositories would.
func newCancelAwareMerchant(t *testing.T, svc *PaymentService, webhookRepo *fakeWebhookEventRepo, merchantID uuid.UUID) *fakeMerchantCallbackRepo {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(server.Close)
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[merchantID] = &merchant.Merchant{ID: merchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	callbackRepo.failOnCancelledCtx = true
	webhookRepo.failOnCancelledCtx = true
	svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)
	return callbackRepo
}

// The provider disconnects while we verify. Once the status change is
// committed the event bookkeeping and the merchant callback must still
// happen: reconcile never revisits a paid payment, so a callback lost here
// would be lost for good.
func TestProcessWebhook_CancelledRequestStillCompletesPostCommit(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-CANCEL")
	callbackRepo := newCancelAwareMerchant(t, svc, webhookRepo, seeded.MerchantID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-CANCEL", nil),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
		onStatus:   cancel, // client goes away during verification
	})

	if err := svc.ProcessWebhook(ctx, "cashi", []byte(`{}`), "sig"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), seeded.ID); got.Status != payment.StatusPaid {
		t.Fatalf("status = %q, want paid", got.Status)
	}
	callbackRepo.mu.Lock()
	created := len(callbackRepo.created)
	callbackRepo.mu.Unlock()
	if created != 1 {
		t.Fatalf("expected exactly 1 merchant callback delivery, got %d", created)
	}
	if e := lastEvent(webhookRepo); e == nil || !e.IsProcessed || e.Status != payment.StatusPaid {
		t.Fatalf("expected event processed/paid, got %+v", e)
	}
}

// cancellingExpiredListRepo cancels the job context right after listing,
// as a shutdown arriving mid-run would.
type cancellingExpiredListRepo struct {
	*fakePaymentRepo
	cancel context.CancelFunc
}

func (r *cancellingExpiredListRepo) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]*payment.Payment, error) {
	out, err := r.fakePaymentRepo.ListExpiredPending(ctx, before, limit)
	r.cancel()
	return out, err
}

func TestExpirePayments_CancelledJobStillNotifies(t *testing.T) {
	base := newFakePaymentRepo()
	p := seedPayment(t, base, payment.StatusPending, "REF-EXP-CANCEL")
	p.ExpiresAt = time.Now().Add(-10 * time.Minute)
	base.byID[p.ID] = p

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	webhookRepo := newFakeWebhookEventRepo()
	svc := NewPaymentService(&cancellingExpiredListRepo{fakePaymentRepo: base, cancel: cancel},
		&fakeMerchantProviderConfigRepo{}, webhookRepo, providerPkg.NewProviderRouter(), "http://localhost:8080")
	callbackRepo := newCancelAwareMerchant(t, svc, webhookRepo, p.MerchantID)

	if err := svc.ExpirePayments(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := base.GetByID(context.Background(), p.ID); got.Status != payment.StatusExpired {
		t.Fatalf("status = %q, want expired", got.Status)
	}
	if len(callbackRepo.created) != 1 || callbackRepo.created[0].EventType != "payment.expired" {
		t.Fatalf("expected one payment.expired callback, got %+v", callbackRepo.created)
	}
}

// A webhook arriving through one provider must not settle a payment owned by
// another, even if that provider's ParseWebhook yields the payment's ref.
func TestProcessWebhook_ProviderDoesNotOwnPayment(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-OWNER") // ProviderName "cashi"
	owner := &fakeProvider{name: "cashi",
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}}
	other := &fakeProvider{name: "other",
		parseResp:  paidWebhook("REF-OWNER", ptrInt64(15000)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}}
	router.RegisterProvider(owner)
	router.RegisterProvider(other)

	err := svc.ProcessWebhook(context.Background(), "other", []byte(`{}`), "sig")
	if !errors.Is(err, payment.ErrProviderMismatch) {
		t.Fatalf("expected ErrProviderMismatch, got %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), seeded.ID); got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if n := len(other.refs()) + len(owner.refs()); n != 0 {
		t.Errorf("provider status must not be checked, got %d calls", n)
	}
	if e := lastEvent(webhookRepo); e == nil || e.Status != "rejected" || e.PaymentID == nil || *e.PaymentID != seeded.ID {
		t.Errorf("expected rejected event linked to the payment, got %+v", e)
	}
}
