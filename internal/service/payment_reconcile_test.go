package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/google/uuid"
)

func TestReconcilePayment_TerminalIsSkipped(t *testing.T) {
	svc, paymentRepo, _, _ := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPaid, "REF-R1")

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "skipped" {
		t.Errorf("expected terminal payment to be skipped, got action %q", result.Action)
	}
}

func TestReconcilePayment_ExpiresLocallyWithoutProviderReference(t *testing.T) {
	svc, paymentRepo, _, _ := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "")
	p.ProviderReference = nil
	p.ExpiresAt = time.Now().Add(-10 * time.Minute) // beyond expireGracePeriod
	paymentRepo.byID[p.ID] = p

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "expired_local" {
		t.Fatalf("expected expired_local, got %q (%s)", result.Action, result.Message)
	}

	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusExpired {
		t.Errorf("expected payment to be expired, got %q", got.Status)
	}
}

func TestReconcilePayment_NoProviderReferenceNotYetExpired_Skipped(t *testing.T) {
	svc, paymentRepo, _, _ := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "")
	p.ProviderReference = nil
	p.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[p.ID] = p

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "skipped" {
		t.Errorf("expected skipped (cannot poll provider), got %q", result.Action)
	}

	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("expected status untouched, got %q", got.Status)
	}
}

func TestReconcilePayment_UnregisteredProvider_ReportsErrorWithoutFailing(t *testing.T) {
	svc, paymentRepo, _, _ := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R2")
	p.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[p.ID] = p
	// No provider registered on the router for "cashi".

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("ReconcilePayment should report provider errors via result, not return err: %v", err)
	}
	if result.Action != "error" {
		t.Errorf("expected action error for unregistered provider, got %q", result.Action)
	}
}

func TestReconcilePayment_ProviderStillPendingNotExpired_Unchanged(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R3")
	p.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "pending"}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "unchanged" {
		t.Errorf("expected unchanged when provider agrees still pending, got %q", result.Action)
	}
}

func TestReconcilePayment_ProviderStillPendingButLocallyExpired(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R4")
	p.ExpiresAt = time.Now().Add(-10 * time.Minute) // beyond expireGracePeriod
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "pending"}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "expired_local" {
		t.Errorf("expected local expiry despite provider still saying pending, got %q", result.Action)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusExpired {
		t.Errorf("expected status expired, got %q", got.Status)
	}
}

// Within expireGracePeriod reconcile must not expire locally: the payment
// may still be settling.
func TestReconcilePayment_ProviderStillPendingWithinGrace_Unchanged(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R4G")
	p.ExpiresAt = time.Now().Add(-1 * time.Minute)
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "pending"}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "unchanged" {
		t.Errorf("expected unchanged within the grace period, got %q (%s)", result.Action, result.Message)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), p.ID); got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestReconcilePayment_ProviderCheckFails_NotExpired_ReportsError(t *testing.T) {
	for kind, statusErr := range statusCheckFailures() {
		t.Run(kind, func(t *testing.T) {
			svc, paymentRepo, _, router := newWebhookTestService()
			p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R5")
			p.ExpiresAt = time.Now().Add(10 * time.Minute)
			paymentRepo.byID[p.ID] = p
			router.RegisterProvider(&fakeProvider{name: "cashi", statusErr: statusErr})

			result, err := svc.ReconcilePayment(context.Background(), p.ID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Action != "error" {
				t.Errorf("expected action error when provider check fails, got %q", result.Action)
			}
			got, _ := paymentRepo.GetByID(context.Background(), p.ID)
			if got.Status != payment.StatusPending {
				t.Errorf("expected status untouched when not past expiry, got %q", got.Status)
			}
		})
	}
}

func TestReconcilePayment_ProviderCheckFails_PastExpiry_FallsBackToLocalExpire(t *testing.T) {
	for kind, statusErr := range statusCheckFailures() {
		t.Run(kind, func(t *testing.T) {
			svc, paymentRepo, _, router := newWebhookTestService()
			p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R6")
			p.ExpiresAt = time.Now().Add(-10 * time.Minute) // beyond expireGracePeriod
			paymentRepo.byID[p.ID] = p
			router.RegisterProvider(&fakeProvider{name: "cashi", statusErr: statusErr})

			result, err := svc.ReconcilePayment(context.Background(), p.ID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Action != "expired_local" {
				t.Errorf("expected fallback local expiry when provider check fails past deadline, got %q", result.Action)
			}
			got, _ := paymentRepo.GetByID(context.Background(), p.ID)
			if got.Status != payment.StatusExpired {
				t.Errorf("expected status expired, got %q", got.Status)
			}
		})
	}
}

func TestReconcilePayment_ProviderReportsPaid_UpdatesStatus(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R7")
	p.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "updated" {
		t.Fatalf("expected action updated, got %q (%s)", result.Action, result.Message)
	}
	if !result.MerchantNotified {
		t.Errorf("expected MerchantNotified flag to be set on status change")
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPaid {
		t.Errorf("expected status paid, got %q", got.Status)
	}
}

func TestReconcilePayment_ProviderReportsUnknownStatus_Unchanged(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R8")
	p.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "refunded"}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "unchanged" {
		t.Errorf("expected unchanged for a non-paid provider status, got %q", result.Action)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("expected status untouched on unknown provider status, got %q", got.Status)
	}
}

func TestReconcilePendingPayments_ChecksOnlyPending(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	pending := seedPayment(t, paymentRepo, payment.StatusPending, "REF-BATCH-1")
	pending.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[pending.ID] = pending
	seedPayment(t, paymentRepo, payment.StatusPaid, "REF-BATCH-2") // must be excluded by ListAdmin(status=pending)

	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "pending"}})

	results, err := svc.ReconcilePendingPayments(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 pending payment reconciled, got %d", len(results))
	}
	if results[0].PaymentID != pending.ID {
		t.Errorf("expected reconciled payment to be the pending one, got %v", results[0].PaymentID)
	}
}

// TestReconcilePendingPayments_DoesNotRefetchByID covers project backlog
// item #6 (N+1 audit finding #3): ReconcilePendingPayments used to call
// ReconcilePayment(ctx, id) per row, which re-fetched each payment by ID
// even though ListAdmin had already loaded it — a redundant SELECT per
// payment in the batch. It now reconciles the already-fetched row directly.
func TestReconcilePendingPayments_DoesNotRefetchByID(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	for i := 0; i < 3; i++ {
		p := seedPayment(t, paymentRepo, payment.StatusPending, fmt.Sprintf("REF-BATCH-N1-%d", i))
		p.ExpiresAt = time.Now().Add(10 * time.Minute)
		paymentRepo.byID[p.ID] = p
	}
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "pending"}})

	paymentRepo.getByIDCalls = 0

	results, err := svc.ReconcilePendingPayments(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 payments reconciled, got %d", len(results))
	}
	if paymentRepo.getByIDCalls != 0 {
		t.Errorf("expected 0 GetByID calls (rows already fetched via ListAdmin), got %d", paymentRepo.getByIDCalls)
	}
}

func TestReconcilePayment_ExpiredSettledLate_UpdatesToPaid(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-RL1")
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "updated" || !strings.Contains(result.Message, "late payment") {
		t.Fatalf("expected late-payment update, got %q (%s)", result.Action, result.Message)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPaid {
		t.Errorf("status = %q, want paid", got.Status)
	}
}

func TestReconcilePayment_ExpiredNotSettled_Unchanged(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-RL2")
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "pending"}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "unchanged" {
		t.Fatalf("expected unchanged, got %q", result.Action)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusExpired {
		t.Errorf("status = %q, want expired", got.Status)
	}
}

func TestReconcilePayment_AmountMismatch_FlagsAndLeavesPending(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RMM")
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(1)}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "error" || !strings.HasPrefix(result.Message, "amount_mismatch:") {
		t.Fatalf("expected amount_mismatch error, got %q (%s)", result.Action, result.Message)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	e := lastEvent(webhookRepo)
	if e == nil || e.EventType != "reconcile.amount_mismatch" || e.PaymentID == nil || *e.PaymentID != p.ID ||
		e.ProcessingError == nil || !strings.HasPrefix(*e.ProcessingError, "amount_mismatch:") {
		t.Fatalf("expected reconcile.amount_mismatch event for the payment, got %+v", e)
	}
}

func TestReconcileRecentPayments_OnlyProductionCandidates(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	prod := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RR1")
	sandbox := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RR2")
	sandbox.Environment = payment.EnvironmentSandbox
	paymentRepo.byID[sandbox.ID] = sandbox
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}})

	if err := svc.ReconcileRecentPayments(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), prod.ID); got.Status != payment.StatusPaid {
		t.Errorf("production payment status = %q, want paid", got.Status)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), sandbox.ID); got.Status != payment.StatusPending {
		t.Errorf("sandbox payment must be untouched, got %q", got.Status)
	}
}

// With more candidates than one batch, the cursor must rotate through the
// whole window instead of re-checking the newest batch forever.
func TestReconcileRecentPayments_CursorCoversWholeWindow(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	svc.reconcileBatch = 2
	prov := &fakeProvider{name: "cashi"} // reports pending: all stay candidates
	router.RegisterProvider(prov)

	now := time.Now()
	refs := make([]string, 5) // refs[0] is the newest payment
	for i := range refs {
		refs[i] = fmt.Sprintf("REF-CUR%d", i+1)
		p := seedPayment(t, paymentRepo, payment.StatusPending, refs[i])
		p.CreatedAt = now.Add(-time.Duration(i+1) * time.Minute)
		paymentRepo.byID[p.ID] = p
	}

	run := func() []string {
		prov.resetRefs()
		if err := svc.ReconcileRecentPayments(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return prov.refs()
	}
	want := [][]string{
		{refs[0], refs[1]},
		{refs[2], refs[3]},
		{refs[4]}, // end of window: cursor wraps
		{refs[0], refs[1]},
	}
	for i, w := range want {
		got := run()
		if strings.Join(got, ",") != strings.Join(w, ",") {
			t.Fatalf("run %d checked %v, want %v", i+1, got, w)
		}
	}
}

// A reconcile run cancelled (e.g. shutdown) while the provider answers must
// still finish the status change it is about to commit and its callback.
func TestReconcilePayment_CancelledContextStillCompletesPostCommit(t *testing.T) {
	cases := []struct {
		name         string
		status       string
		expiresAt    time.Duration
		providerResp *domainProvider.NormalizedPaymentStatus
		wantStatus   string
		wantAction   string
		wantEvent    string
	}{
		{"settled", payment.StatusPending, 10 * time.Minute,
			&domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
			payment.StatusPaid, "updated", "payment.paid"},
		{"expired locally", payment.StatusPending, -10 * time.Minute,
			&domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending},
			payment.StatusExpired, "expired_local", "payment.expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, paymentRepo, webhookRepo, router := newWebhookTestService()
			p := seedPayment(t, paymentRepo, tc.status, "REF-RC-CANCEL")
			p.ExpiresAt = time.Now().Add(tc.expiresAt)
			paymentRepo.byID[p.ID] = p
			callbackRepo := newCancelAwareMerchant(t, svc, webhookRepo, p.MerchantID)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: tc.providerResp, onStatus: cancel})

			result, err := svc.ReconcilePayment(ctx, p.ID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Action != tc.wantAction {
				t.Fatalf("action = %q (%s), want %q", result.Action, result.Message, tc.wantAction)
			}
			if got, _ := paymentRepo.GetByID(context.Background(), p.ID); got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if len(callbackRepo.created) != 1 || callbackRepo.created[0].EventType != tc.wantEvent {
				t.Fatalf("expected one %s callback, got %+v", tc.wantEvent, callbackRepo.created)
			}
		})
	}
}

// newCallbackMerchant registers a merchant webhook URL so any callback the
// service sends is recorded — a wrong one would show up in the count.
func newCallbackMerchant(t *testing.T, svc *PaymentService, merchantID uuid.UUID) *fakeMerchantCallbackRepo {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(server.Close)
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[merchantID] = &merchant.Merchant{ID: merchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)
	return callbackRepo
}

// Deterministic lost races: another caller settles the payment between
// reconcile's provider check and its guarded update. Reconcile must report
// "skipped" and leave the merchant callback to the winner.
func TestReconcilePayment_LosesRaceToConcurrentSettlement(t *testing.T) {
	cases := []struct {
		name         string
		expiresIn    time.Duration
		providerResp *domainProvider.NormalizedPaymentStatus
	}{
		{"paid path", 10 * time.Minute,
			&domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}},
		{"expireLocally path", -10 * time.Minute, // past expiry + grace, provider pending
			&domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, paymentRepo, _, router := newWebhookTestService()
			p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RC-RACE")
			p.ExpiresAt = time.Now().Add(tc.expiresIn)
			paymentRepo.byID[p.ID] = p
			callbackRepo := newCallbackMerchant(t, svc, p.MerchantID)
			router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: tc.providerResp,
				onStatus: func() {
					now := time.Now()
					if _, err := paymentRepo.UpdateStatus(context.Background(), p.ID, payment.StatusPaid, &now); err != nil {
						t.Errorf("simulated winner failed: %v", err)
					}
				}})

			result, err := svc.ReconcilePayment(context.Background(), p.ID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Action != "skipped" || result.Message != "Payment changed concurrently" {
				t.Fatalf("got %q (%s), want skipped / Payment changed concurrently", result.Action, result.Message)
			}
			if len(callbackRepo.created) != 0 {
				t.Errorf("loser must not notify the merchant, got %d callbacks", len(callbackRepo.created))
			}
			if got, _ := paymentRepo.GetByID(context.Background(), p.ID); got.Status != payment.StatusPaid {
				t.Errorf("status = %q, want paid (the winner's write)", got.Status)
			}
		})
	}
}

func TestReconcilePayment_LateSettlementNotifiesOnceAsLate(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-RC-LATE")
	callbackRepo := newCallbackMerchant(t, svc, p.MerchantID)
	router.RegisterProvider(&fakeProvider{name: "cashi",
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "updated" {
		t.Fatalf("action = %q (%s), want updated", result.Action, result.Message)
	}
	if len(callbackRepo.created) != 1 {
		t.Fatalf("expected exactly 1 callback, got %d", len(callbackRepo.created))
	}
	cb := callbackRepo.created[0]
	if cb.EventType != "payment.paid" || cb.RequestPayload["late_payment"] != true {
		t.Fatalf("expected payment.paid with late_payment=true, got %s %v", cb.EventType, cb.RequestPayload["late_payment"])
	}
}

// The service must pass the 6h expired window, not the 24h pending one.
func TestReconcileRecentPayments_ExpiredWindow(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	tooOld := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-EW-8H")
	tooOld.ExpiresAt = time.Now().Add(-8 * time.Hour)
	paymentRepo.byID[tooOld.ID] = tooOld
	recent := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-EW-3H")
	recent.ExpiresAt = time.Now().Add(-3 * time.Hour)
	paymentRepo.byID[recent.ID] = recent
	router.RegisterProvider(&fakeProvider{name: "cashi",
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}})

	if err := svc.ReconcileRecentPayments(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), tooOld.ID); got.Status != payment.StatusExpired {
		t.Errorf("expired 8h ago: status = %q, want expired (outside the 6h window)", got.Status)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), recent.ID); got.Status != payment.StatusPaid {
		t.Errorf("expired 3h ago: status = %q, want paid", got.Status)
	}
}
