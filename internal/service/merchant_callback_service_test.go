package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
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

func TestRetryMerchantCallback_StopsSchedulingAfterMaxAttempts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
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
	svc.useLoopbackCallbacks()
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
	svc.NotifyMerchantPaymentEvent(context.Background(), payload, "payment.paid", false)
	// Notify only queues; the worker is what actually sends (and signs).
	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

// useLoopbackCallbacks lets a test deliver to an httptest server on
// 127.0.0.1: the production validator and dialer (correctly) refuse
// loopback, so tests that exercise delivery mechanics swap them out.
// Tests of the SSRF protection itself must NOT call this.
func (s *PaymentService) useLoopbackCallbacks() *PaymentService {
	s.callbackValidate = func(string, bool) error { return nil }
	s.callbackClient = func() *http.Client { return &http.Client{Timeout: 10 * time.Second} }
	return s
}

func counting(hits *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
}

func TestNotifyMerchantPaymentEvent_OnlyQueuesPendingDelivery(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, merchantRepo, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()

	p := &payment.Payment{ID: uuid.New(), Reference: "PAY-Q1", MerchantID: uuid.New(), Amount: 1000,
		Currency: payment.CurrencyIDR, Status: payment.StatusPaid, ProviderName: "cashi"}
	url := server.URL
	merchantRepo.byID[p.MerchantID] = &merchant.Merchant{ID: p.MerchantID, WebhookURL: &url}

	svc.NotifyMerchantPaymentEvent(context.Background(), p, "payment.paid", false)

	if len(callbackRepo.created) != 1 || callbackRepo.created[0].Status != merchant.CallbackStatusPending {
		t.Fatalf("expected exactly one pending delivery, got %+v", callbackRepo.created)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("Notify must not perform HTTP, hits=%d", hits)
	}
}

func TestDeliverMerchantCallbacks_SendsPendingAndMarksSuccess(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := callbackRepo.byID[d.ID]
	if got.Status != merchant.CallbackStatusSuccess || got.HTTPStatus == nil || *got.HTTPStatus != 200 {
		t.Fatalf("unexpected delivery state: %+v", got)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

func TestDeliverMerchantCallbacks_RequeuesDueFailedAsNewAttempt(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	past := time.Now().UTC().Add(-time.Minute)
	source := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &past)

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callbackRepo.byID[source.ID].NextRetryAt != nil {
		t.Error("source retry schedule must be cleared")
	}
	if len(callbackRepo.created) != 1 || callbackRepo.created[0].AttemptNumber != 2 {
		t.Fatalf("expected one new attempt #2, got %+v", callbackRepo.created)
	}
	// The worker sends a copy and UpdateResult replaces the stored row, so
	// read the outcome back by ID.
	if got := callbackRepo.byID[callbackRepo.created[0].ID]; got.Status != merchant.CallbackStatusSuccess || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("new attempt should have been delivered once: status=%s hits=%d", got.Status, hits)
	}

	// A second tick finds nothing: the source's schedule is gone and the new
	// attempt is final.
	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error on second tick: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 || len(callbackRepo.created) != 1 {
		t.Fatalf("second tick must send/requeue nothing: hits=%d created=%d", n, len(callbackRepo.created))
	}
}

func TestDeliverMerchantCallbacks_IgnoresNotDueOrFinal(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	future := time.Now().UTC().Add(10 * time.Minute)
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &future) // not due
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 5, nil)     // capped
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSuccess, server.URL, 1, nil)    // done

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 || len(callbackRepo.created) != 0 {
		t.Fatalf("nothing should be sent or requeued: hits=%d created=%d", hits, len(callbackRepo.created))
	}
}

func TestDeliverMerchantCallbacks_ReclaimsOnlyStaleSending(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	stale := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSending, server.URL, 1, nil)
	stale.UpdatedAt = time.Now().UTC().Add(-3 * time.Minute)
	fresh := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSending, server.URL, 1, nil)

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Read back from the repo: the worker works on copies and UpdateResult
	// replaces the stored row, so the seeded pointers can be stale.
	if got := callbackRepo.byID[stale.ID]; got.Status != merchant.CallbackStatusSuccess {
		t.Errorf("stale 'sending' row should have been re-sent, status=%s", got.Status)
	}
	if got := callbackRepo.byID[fresh.ID]; got.Status != merchant.CallbackStatusSending || atomic.LoadInt32(&hits) != 1 {
		t.Errorf("fresh 'sending' row must be left alone: status=%s hits=%d", got.Status, hits)
	}
}

func TestDeliverMerchantCallbacks_CachesWebhookSecretPerMerchant(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, merchantRepo, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	merchantID := uuid.New()
	secret := "s3cret"
	merchantRepo.byID[merchantID] = &merchant.Merchant{ID: merchantID, WebhookSecret: &secret}
	for i := 0; i < 3; i++ {
		d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)
		d.MerchantID = merchantID
	}
	merchantRepo.getByIDCalls = 0

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merchantRepo.getByIDCalls != 1 {
		t.Errorf("merchant lookups = %d, want 1 (secret cached per batch)", merchantRepo.getByIDCalls)
	}
	if atomic.LoadInt32(&hits) != 3 {
		t.Errorf("hits = %d, want 3", hits)
	}
}

func TestDeliverMerchantCallbacks_NoopWithoutCallbackDeps(t *testing.T) {
	svc := NewPaymentService(newFakePaymentRepo(), &fakeMerchantProviderConfigRepo{}, newFakeWebhookEventRepo(), providerPkg.NewProviderRouter(), "http://localhost:8080")
	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("expected nil without callback deps, got %v", err)
	}
}

// A delivery that panics the worker must not stay 'sending' (it would be
// re-claimed every callbackSendingStaleAfter and panic again forever), and it
// must not take the rest of the batch down with it.
func TestDeliverMerchantCallbacks_PanickingDeliveryFailsPermanentlyAndBatchContinues(t *testing.T) {
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil }
	svc.callbackClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "poison.example.com" {
				panic("boom")
			}
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		})}
	}
	poison := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "https://poison.example.com/h", 1, nil)
	healthy := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "https://ok.example.com/h", 1, nil)

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := callbackRepo.byID[poison.ID]
	if got.Status != merchant.CallbackStatusFailed || got.NextRetryAt != nil {
		t.Fatalf("panicking delivery must fail permanently: status=%s next_retry_at=%v", got.Status, got.NextRetryAt)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != "internal error while sending callback" {
		t.Errorf("unexpected error message: %v", got.ErrorMessage)
	}
	if got := callbackRepo.byID[healthy.ID]; got.Status != merchant.CallbackStatusSuccess {
		t.Errorf("the rest of the batch must still be delivered, status=%s", got.Status)
	}

	// The next tick must not pick the poisoned row up again.
	claimed, err := callbackRepo.ClaimDue(context.Background(), time.Now().UTC().Add(time.Hour), 0, callbackSendingStaleAfter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("nothing should be claimable after the batch, got %d rows", len(claimed))
	}
}

// --- SSRF protection: these use the REAL validator and dialer. ---

func seedPaymentFor(t *testing.T, svc *PaymentService, env string) *payment.Payment {
	t.Helper()
	p := &payment.Payment{ID: uuid.New(), Reference: "PAY-" + uuid.NewString()[:8], MerchantID: uuid.New(),
		Amount: 1000, Currency: payment.CurrencyIDR, Status: payment.StatusPaid, ProviderName: "cashi", Environment: env}
	if err := svc.paymentRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	return p
}

// https, so the scheme rule passes and the real validator's IP rule is what
// rejects it. The real client is kept too: if the validator let it through,
// the dialer would block it with a different stored message.
func TestExecuteCallbackDelivery_ProductionRejectsInternalIPTarget(t *testing.T) {
	svc, _, callbackRepo := newCallbackTestService(t)
	p := seedPaymentFor(t, svc, payment.EnvironmentProduction)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "https://10.0.0.5/x", 1, nil)
	d.PaymentID = p.ID

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt != nil || d.ErrorMessage == nil {
		t.Fatalf("expected permanent failure with message, got %+v", d)
	}
	if want := "invalid callback url: host is not a public address"; *d.ErrorMessage != want {
		t.Errorf("stored error = %q, want %q", *d.ErrorMessage, want)
	}
}

// Sandbox relaxes only the scheme: it still may not target internal
// addresses (a merchant's localhost is not reachable from our server anyway).
func TestExecuteCallbackDelivery_SandboxPaymentStillRejectsLocalTarget(t *testing.T) {
	var hits int32
	server := counting(&hits) // http://127.0.0.1:port
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	p := seedPaymentFor(t, svc, payment.EnvironmentSandbox)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)
	d.PaymentID = p.ID

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt != nil || atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("sandbox must not reach loopback: %+v hits=%d", d, hits)
	}
	if d.ErrorMessage == nil || !strings.HasPrefix(*d.ErrorMessage, "invalid callback url") {
		t.Errorf("stored error = %v, want the validator's rejection", d.ErrorMessage)
	}
}

// The environment only decides whether plain http is allowed.
func TestExecuteCallbackDelivery_AllowHTTPFollowsPaymentEnvironment(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{{payment.EnvironmentSandbox, true}, {payment.EnvironmentProduction, false}} {
		t.Run(tc.env, func(t *testing.T) {
			svc, _, callbackRepo := newCallbackTestService(t)
			var got bool
			svc.callbackValidate = func(_ string, allowHTTP bool) error { got = allowHTTP; return nil }
			svc.callbackClient = func() *http.Client {
				return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
				})}
			}
			p := seedPaymentFor(t, svc, tc.env)
			d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "https://example.com/h", 1, nil)
			d.PaymentID = p.ID

			svc.executeCallbackDelivery(context.Background(), d, nil)

			if got != tc.want {
				t.Errorf("allowHTTP = %v, want %v", got, tc.want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Defense in depth: even if a URL got past the storage-time check, the
// dialer refuses internal destinations, and that failure is not retried.
func TestExecuteCallbackDelivery_DialerBlocksInternalDestinationWithoutRetry(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil } // validator bypassed; real strict client kept
	p := seedPaymentFor(t, svc, payment.EnvironmentProduction)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)
	d.PaymentID = p.ID

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt != nil {
		t.Fatalf("blocked destination must fail without a retry schedule: %+v", d)
	}
	if d.ErrorMessage == nil || *d.ErrorMessage != "destination address not allowed" {
		t.Errorf("stored error = %v, want the dialer's block (no URL or IP)", d.ErrorMessage)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("no request may reach the server, hits=%d", hits)
	}
}

func TestExecuteCallbackDelivery_UnknownPaymentIsTreatedAsProduction(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	called, gotAllowHTTP := false, true
	svc.callbackValidate = func(raw string, allowHTTP bool) error {
		called, gotAllowHTTP = true, allowHTTP
		return netsafe.ValidateCallbackURL(raw, allowHTTP)
	}
	svc.callbackClient = okClient(0, &hits, nil)
	// Public http URL: accepted for sandbox, so only the strict rules reject it.
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "http://merchant.example.com/h", 1, nil) // random PaymentID

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if !called || gotAllowHTTP {
		t.Fatalf("unknown payment must be validated strictly: called=%v allowHTTP=%v", called, gotAllowHTTP)
	}
	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt != nil || atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("expected permanent failure without a send: status=%s next=%v hits=%d", d.Status, d.NextRetryAt, hits)
	}

}

// --- Manual admin retry vs. the delivery worker ---

// workerDuringManualSend returns a client whose first request runs a full
// worker tick from inside the manual send (the window where a duplicate can
// happen), then answers 200. onFirst, if set, runs first, before the tick.
// Later requests (the worker's own sends) only count.
func workerDuringManualSend(t *testing.T, svc *PaymentService, hits *int32, onFirst func()) func() *http.Client {
	t.Helper()
	return func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if atomic.AddInt32(hits, 1) == 1 {
				if onFirst != nil {
					onFirst()
				}
				if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
					t.Errorf("worker tick: %v", err)
				}
			}
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		})}
	}
}

func TestRetryMerchantCallback_FailedSourceIsSentExactlyOnce(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil }
	past := time.Now().UTC().Add(-time.Minute)
	source := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, "https://m.example.com/h", 1, &past)
	svc.callbackClient = workerDuringManualSend(t, svc, &hits, func() {
		// The schedule must already be gone, or the worker tick below
		// requeues the same failure as a second attempt.
		if callbackRepo.byID[source.ID].NextRetryAt != nil {
			t.Error("source next_retry_at must be cleared before the manual send starts")
		}
	})

	retried, err := svc.RetryMerchantCallback(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("sends = %d, want exactly 1 (the manual one)", got)
	}
	if got := callbackRepo.byID[retried.ID]; got.Status != merchant.CallbackStatusSuccess {
		t.Errorf("manual attempt status = %s, want success", got.Status)
	}
	if len(callbackRepo.created) != 1 {
		t.Errorf("attempt rows created = %d, want 1", len(callbackRepo.created))
	}
	if got := callbackRepo.byID[source.ID]; got.Status != merchant.CallbackStatusFailed || got.NextRetryAt != nil {
		t.Errorf("source must stay failed with no schedule: status=%s next=%v", got.Status, got.NextRetryAt)
	}
}

// Re-sending a delivery that already succeeded is allowed on purpose; it
// must send once and leave the source exactly as it was.
func TestRetryMerchantCallback_SuccessSourceResendsOnceAndStaysSuccess(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil }
	svc.callbackClient = workerDuringManualSend(t, svc, &hits, nil)
	source := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSuccess, "https://m.example.com/h", 1, nil)

	if _, err := svc.RetryMerchantCallback(context.Background(), source.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("sends = %d, want exactly 1", got)
	}
	if got := callbackRepo.byID[source.ID]; got.Status != merchant.CallbackStatusSuccess {
		t.Errorf("source status = %s, want success untouched", got.Status)
	}
}

// A pending source is the worker's to send; the manual retry must not write
// its stale snapshot back over whatever the worker recorded.
func TestRetryMerchantCallback_PendingSourceIsNotReverted(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil }
	svc.callbackClient = workerDuringManualSend(t, svc, &hits, nil)
	source := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "https://m.example.com/h", 1, nil)

	if _, err := svc.RetryMerchantCallback(context.Background(), source.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := callbackRepo.byID[source.ID]
	if got.Status != merchant.CallbackStatusSuccess || got.HTTPStatus == nil {
		t.Errorf("source must keep the worker's result (success), got status=%s http=%v", got.Status, got.HTTPStatus)
	}
	// The manual send plus the worker's send of the pending source: 2, and
	// nothing more on a further tick.
	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("sends = %d, want 2", n)
	}
}

// --- Recording failures, batch budget, shutdown ---

func okClient(delay time.Duration, hits *int32, onSend func()) func() *http.Client {
	return func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(hits, 1)
			if onSend != nil {
				onSend()
			}
			time.Sleep(delay)
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		})}
	}
}

func seedPending(repo *fakeMerchantCallbackRepo, n int) []*merchant.CallbackDelivery {
	out := make([]*merchant.CallbackDelivery, n)
	for i := range out {
		out[i] = seedCallbackDelivery(repo, merchant.CallbackStatusPending, "https://m.example.com/h", 1, nil)
	}
	return out
}

// A permanently failing UpdateResult: each row gets the full write plus one
// detail-less fallback, then the run moves on (no loop, no panic).
func TestDeliverMerchantCallbacks_RecordFailureIsAttemptedAndContained(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil }
	svc.callbackClient = okClient(0, &hits, nil)
	seedPending(callbackRepo, 2)
	callbackRepo.updateResultErr = errors.New("db down")

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callbackRepo.updateResultCalls != 4 || atomic.LoadInt32(&hits) != 2 {
		t.Errorf("record attempts = %d, sends = %d; want 4 (2 per row) and 2", callbackRepo.updateResultCalls, hits)
	}
}

// The permanent-failure path records through the same helper.
func TestExecuteCallbackDelivery_PermanentFailureRecordErrorIsContained(t *testing.T) {
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return errors.New("invalid callback url: host is not public") }
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSending, "https://m.example.com/h", 1, nil)
	callbackRepo.updateResultErr = errors.New("db down")

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if callbackRepo.updateResultCalls != 2 {
		t.Errorf("record attempts = %d, want 2 (full write + fallback)", callbackRepo.updateResultCalls)
	}
}

// If the full result can't be stored for this row, the outcome still is:
// status, HTTP status and delivery time survive, only the detail is dropped.
func TestDeliverMerchantCallbacks_FallbackStoresOutcomeWithoutDetail(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil }
	svc.callbackClient = bodyClient(&hits, 200, "fine")
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "https://m.example.com/h", 1, nil)
	callbackRepo.updateResultHook = func(d *merchant.CallbackDelivery) error {
		if d.ResponseBody != nil {
			return errors.New("row-specific rejection")
		}
		return nil
	}

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := callbackRepo.byID[d.ID]
	if got.Status != merchant.CallbackStatusSuccess || got.HTTPStatus == nil || *got.HTTPStatus != 200 || got.DeliveredAt == nil {
		t.Fatalf("outcome must survive the fallback: %+v", got)
	}
	if got.ResponseBody != nil || got.ErrorMessage == nil || *got.ErrorMessage != callbackResultDetailLost {
		t.Errorf("fallback must drop the detail: body=%v msg=%v", got.ResponseBody, got.ErrorMessage)
	}
}

// Rows claimed but not sent before the budget ran out go straight back to
// 'pending' (not left for the 2-minute stale window), and a row that is no
// longer 'sending' is not touched by that release.
func TestDeliverMerchantCallbacks_BudgetBreakReleasesUnsentRowsToPending(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil }
	svc.callbackBatchBudget = time.Millisecond
	rows := make([]*merchant.CallbackDelivery, 5)
	for i := range rows {
		rows[i] = seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "", 1, nil)
		rows[i].TargetURL = "https://m.example.com/h/" + rows[i].ID.String()
	}
	finishedElsewhere := uuid.Nil
	svc.callbackClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if atomic.AddInt32(&hits, 1) == 1 {
				// Something else finishes another claimed row meanwhile.
				for _, row := range rows {
					if !strings.HasSuffix(r.URL.Path, row.ID.String()) {
						finishedElsewhere = row.ID
						callbackRepo.byID[row.ID].Status = merchant.CallbackStatusSuccess
						break
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		})}
	}

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sent := int(atomic.LoadInt32(&hits))
	if sent == 0 || sent >= 4 {
		t.Fatalf("sends = %d, want at least 1 and fewer than 4", sent)
	}
	var pending int
	for _, r := range rows {
		got := callbackRepo.byID[r.ID]
		switch {
		case r.ID == finishedElsewhere:
			if got.Status != merchant.CallbackStatusSuccess {
				t.Errorf("row finished elsewhere was changed to %s by the release", got.Status)
			}
		case got.Status == merchant.CallbackStatusPending:
			pending++
		case got.Status != merchant.CallbackStatusSuccess:
			t.Errorf("row %s status = %s, want success or released to pending", r.ID, got.Status)
		}
	}
	if pending != 5-1-sent {
		t.Fatalf("released 'pending' rows = %d, want %d", pending, 5-1-sent)
	}

	// The very next tick sends them; nothing is sent twice.
	svc.callbackBatchBudget = time.Minute
	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range rows {
		if got := callbackRepo.byID[r.ID]; got.Status != merchant.CallbackStatusSuccess {
			t.Errorf("row %s status = %s after the next tick, want success", r.ID, got.Status)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 4 {
		t.Errorf("total sends = %d, want 4 (each row not finished elsewhere, once)", n)
	}
}

// Cancelling mid-batch: the delivery in flight still records its result
// (detached ctx), and rows not reached are released back to 'pending'.
func TestDeliverMerchantCallbacks_ShutdownMidBatchRecordsInFlightAndReleasesRest(t *testing.T) {
	var hits int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil }
	svc.callbackClient = okClient(0, &hits, cancel) // first send triggers shutdown
	rows := seedPending(callbackRepo, 3)

	if err := svc.DeliverMerchantCallbacks(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("sends = %d, want 1 (only the one in flight)", n)
	}
	var success, pending int
	for _, r := range rows {
		switch callbackRepo.byID[r.ID].Status {
		case merchant.CallbackStatusSuccess:
			success++
		case merchant.CallbackStatusPending:
			pending++
		}
	}
	if success != 1 || pending != 2 {
		t.Errorf("success=%d pending=%d, want 1 recorded in-flight and 2 released to pending", success, pending)
	}
}

// --- Storable results (PostgreSQL TEXT rejects invalid UTF-8 and NUL) ---

func bodyClient(hits *int32, status int, body string) func() *http.Client {
	return func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(hits, 1)
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
	}
}

// A body that can't be stored must not leave the row 'sending' (it would be
// reclaimed and re-sent every stale window, forever).
func TestDeliverMerchantCallbacks_UnstorableResponseBodyStillRecordsOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantStatus string
		wantBody   string
	}{
		{"invalid UTF-8", 200, "ok\xff\xfe", merchant.CallbackStatusSuccess, "ok"},
		{"NUL byte", 500, "a\x00b", merchant.CallbackStatusFailed, "ab"},
		// The 4096-byte read limit cuts inside the 2-byte "é".
		{"read cap splits a rune", 200, strings.Repeat("x", 4095) + "é" + "rest", merchant.CallbackStatusSuccess, strings.Repeat("x", 4095)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits int32
			svc, _, callbackRepo := newCallbackTestService(t)
			svc.callbackValidate = func(string, bool) error { return nil }
			svc.callbackClient = bodyClient(&hits, tc.status, tc.body)
			d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "https://m.example.com/h", 1, nil)

			if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := callbackRepo.byID[d.ID]
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", got.Status, tc.wantStatus)
			}
			if got.ResponseBody == nil || *got.ResponseBody != tc.wantBody {
				t.Errorf("stored body = %v, want the sanitised %q", got.ResponseBody, tc.wantBody)
			}

			// Even once any claim would be stale, nothing is sent again.
			for _, row := range callbackRepo.byID {
				row.UpdatedAt = time.Now().UTC().Add(-callbackSendingStaleAfter - time.Second)
			}
			if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n := atomic.LoadInt32(&hits); n != 1 {
				t.Errorf("sends = %d, want 1", n)
			}
		})
	}
}

// --- Transient payment lookup errors ---

type flakyPaymentRepo struct{ *fakePaymentRepo }

func (flakyPaymentRepo) GetByID(context.Context, uuid.UUID) (*payment.Payment, error) {
	return nil, errors.New("pq: connection reset by peer")
}

// A DB hiccup while loading the payment says nothing about the URL: it must
// neither send (we don't know which rules apply) nor fail permanently (a
// sandbox http callback would be lost for good). It is retried normally.
func TestExecuteCallbackDelivery_PaymentLookupErrorIsRetriedNotFailedPermanently(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.paymentRepo = flakyPaymentRepo{newFakePaymentRepo()}
	svc.callbackClient = okClient(0, &hits, nil)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSending, "http://merchant.example.com/h", 1, nil)

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt == nil {
		t.Fatalf("want failed with a retry schedule, got status=%s next=%v", d.Status, d.NextRetryAt)
	}
	if d.ErrorMessage == nil || *d.ErrorMessage != "temporary error, will retry" {
		t.Errorf("stored error = %v, want the temporary-error text", d.ErrorMessage)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("sends = %d, want 0", n)
	}
}

// The cap case: nextRetryAt returns nil, so claiming "will retry" would lie.
func TestExecuteCallbackDelivery_PaymentLookupErrorAtAttemptCapSaysGivingUp(t *testing.T) {
	var hits int32
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.paymentRepo = flakyPaymentRepo{newFakePaymentRepo()}
	svc.callbackClient = okClient(0, &hits, nil)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSending, "https://merchant.example.com/h", maxCallbackAttempts, nil)

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt != nil {
		t.Fatalf("want failed with no retry schedule at the cap, got status=%s next=%v", d.Status, d.NextRetryAt)
	}
	if d.ErrorMessage == nil || *d.ErrorMessage != "temporary error, giving up after the last attempt" {
		t.Errorf("stored error = %v, want the giving-up text", d.ErrorMessage)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("sends = %d, want 0", n)
	}
}

// --- Manual retry uses the current URL ---

// retryURLClient records the host of every request so a test can tell which
// URL a manual retry really contacted.
func retryURLClient(hosts *[]string) func() *http.Client {
	return func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			*hosts = append(*hosts, r.URL.Host)
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		})}
	}
}

func TestRetryMerchantCallback_UsesCurrentURL(t *testing.T) {
	const stored = "https://old.example.com/h"
	merchantURL := "https://new-merchant.example.com/h"
	paymentURL := "https://payment-specific.example.com/h"
	blank := "   "

	tests := []struct {
		name          string
		paymentExists bool
		paymentCB     *string
		merchantURL   *string
		wantHost      string
	}{
		{"merchant URL changed", true, nil, &merchantURL, "new-merchant.example.com"},
		{"blank payment callback falls to merchant URL", true, &blank, &merchantURL, "new-merchant.example.com"},
		{"payment callback wins over merchant URL", true, &paymentURL, &merchantURL, "payment-specific.example.com"},
		{"payment not found keeps stored URL", false, nil, &merchantURL, "old.example.com"},
		{"nothing configured keeps stored URL", true, nil, nil, "old.example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, merchantRepo, callbackRepo := newCallbackTestService(t)
			var hosts []string
			svc.callbackClient = retryURLClient(&hosts)
			svc.callbackValidate = func(string, bool) error { return nil }

			past := time.Now().UTC().Add(-time.Minute)
			source := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, stored, 1, &past)
			if tc.paymentExists {
				source.PaymentID = uuid.New()
				p := &payment.Payment{ID: source.PaymentID, Reference: "PAY-X", MerchantID: source.MerchantID,
					Status: payment.StatusPaid, Environment: payment.EnvironmentProduction, CallbackURL: tc.paymentCB}
				if err := svc.paymentRepo.Create(context.Background(), p); err != nil {
					t.Fatal(err)
				}
				merchantRepo.byID[source.MerchantID] = &merchant.Merchant{ID: source.MerchantID, WebhookURL: tc.merchantURL}
			} else {
				merchantRepo.byID[source.MerchantID] = &merchant.Merchant{ID: source.MerchantID, WebhookURL: tc.merchantURL}
			}

			retried, err := svc.RetryMerchantCallback(context.Background(), source.ID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(hosts) != 1 || hosts[0] != tc.wantHost {
				t.Errorf("contacted %v, want exactly [%s]", hosts, tc.wantHost)
			}
			if !strings.Contains(retried.TargetURL, tc.wantHost) {
				t.Errorf("new attempt TargetURL = %q, want host %s", retried.TargetURL, tc.wantHost)
			}
			if callbackRepo.byID[source.ID].TargetURL != stored {
				t.Errorf("source row must keep its stored URL")
			}
		})
	}
}

// The new URL still goes through the validator: a corrected-but-invalid URL
// (or one that points at us) is rejected, never sent.
func TestRetryMerchantCallback_NewURLStillValidated(t *testing.T) {
	svc, merchantRepo, callbackRepo := newCallbackTestService(t)
	var hosts []string
	svc.callbackClient = retryURLClient(&hosts)
	past := time.Now().UTC().Add(-time.Minute)
	source := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, "https://old.example.com/h", 1, &past)
	source.PaymentID = uuid.New()
	bad := "https://10.0.0.5/h"
	p := &payment.Payment{ID: source.PaymentID, Reference: "PAY-Y", MerchantID: source.MerchantID,
		Status: payment.StatusPaid, Environment: payment.EnvironmentProduction, CallbackURL: &bad}
	if err := svc.paymentRepo.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	merchantRepo.byID[source.MerchantID] = &merchant.Merchant{ID: source.MerchantID}

	retried, err := svc.RetryMerchantCallback(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("an invalid URL must not be contacted, got %v", hosts)
	}
	if retried.Status != merchant.CallbackStatusFailed || retried.NextRetryAt != nil {
		t.Errorf("want permanent failure, got status=%s next=%v", retried.Status, retried.NextRetryAt)
	}
}

// A transient lookup failure must leave everything untouched, so the admin
// can simply press retry again.
func TestRetryMerchantCallback_PaymentLookupErrorChangesNothing(t *testing.T) {
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.paymentRepo = flakyPaymentRepo{newFakePaymentRepo()}
	var hosts []string
	svc.callbackClient = retryURLClient(&hosts)
	past := time.Now().UTC().Add(-time.Minute)
	source := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, "https://old.example.com/h", 1, &past)

	if _, err := svc.RetryMerchantCallback(context.Background(), source.ID); err == nil {
		t.Fatal("want the lookup error returned")
	}
	if got := callbackRepo.byID[source.ID]; got.NextRetryAt == nil {
		t.Error("source schedule must stay untouched")
	}
	if len(callbackRepo.created) != 0 || len(hosts) != 0 {
		t.Errorf("nothing may be created or sent: created=%d hosts=%v", len(callbackRepo.created), hosts)
	}
}
