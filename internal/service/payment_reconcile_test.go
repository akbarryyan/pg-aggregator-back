package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
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
	p.ExpiresAt = time.Now().Add(-1 * time.Minute)
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
	p.ExpiresAt = time.Now().Add(-1 * time.Minute)
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

func TestReconcilePayment_ProviderCheckFails_NotExpired_ReportsError(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R5")
	p.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusErr: providerPkg.ErrProviderAPIError})

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
}

func TestReconcilePayment_ProviderCheckFails_PastExpiry_FallsBackToLocalExpire(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R6")
	p.ExpiresAt = time.Now().Add(-1 * time.Minute)
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusErr: providerPkg.ErrProviderAPIError})

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
}

func TestReconcilePayment_ProviderReportsPaid_UpdatesStatus(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R7")
	p.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid}})

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

func TestReconcilePayment_ProviderReportsInvalidTransition_ReportsError(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-R8")
	p.ExpiresAt = time.Now().Add(10 * time.Minute)
	paymentRepo.byID[p.ID] = p
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "refunded"}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "error" {
		t.Errorf("expected action error for invalid transition, got %q", result.Action)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("expected status untouched on invalid transition, got %q", got.Status)
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
