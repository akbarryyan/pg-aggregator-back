package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
)

// Production regression: for a settled order Cashi's check-status answers
// {"success":true,"status":"SETTLED"} WITHOUT an amount. The webhook (signed,
// and stating the amount) or reconcile must still be able to settle the payment
// instead of rejecting it as "amount_mismatch: ... provider 0".

func settledWithoutAmountProvider(webhookAmount *int64) *fakeProvider {
	paidAt := time.Now()
	return &fakeProvider{
		name: "cashi",
		parseResp: &domainProvider.ProviderWebhookPayload{
			ProviderReference: "REF-NOAMT",
			Status:            payment.StatusPaid,
			PaidAt:            &paidAt,
			Amount:            webhookAmount,
		},
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid}, // Amount nil
	}
}

func TestProcessWebhook_ProviderOmitsAmount_WebhookAmountMatches_Settles(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-NOAMT") // amount 15000
	router.RegisterProvider(settledWithoutAmountProvider(ptrInt64(15000)))

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPaid {
		t.Fatalf("status = %q, want paid", got.Status)
	}
	if ev := lastEvent(webhookRepo); ev == nil || !ev.IsProcessed {
		t.Fatalf("event should be processed, got %+v", ev)
	}
	if len(paymentRepo.markPaidNets) != 1 {
		t.Fatalf("MarkPaid calls = %d, want exactly 1 (ledger credit)", len(paymentRepo.markPaidNets))
	}
}

func TestProcessWebhook_ProviderOmitsAmount_WebhookAmountWrong_Rejected(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-NOAMT")
	router.RegisterProvider(settledWithoutAmountProvider(ptrInt64(1)))

	_ = svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")

	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPending {
		t.Fatalf("a webhook stating the wrong amount must not settle the payment, status = %q", got.Status)
	}
	ev := lastEvent(webhookRepo)
	if ev == nil || ev.ProcessingError == nil || !strings.HasPrefix(*ev.ProcessingError, "webhook_amount_mismatch:") {
		t.Fatalf("event should record webhook_amount_mismatch (so reconcile may still settle it), got %+v", ev)
	}
	if len(paymentRepo.markPaidNets) != 0 {
		t.Fatalf("MarkPaid must not run, got %d calls", len(paymentRepo.markPaidNets))
	}
}

func TestProcessWebhook_NeitherSideStatesAnAmount_FailsClosed(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-NOAMT")
	router.RegisterProvider(settledWithoutAmountProvider(nil))

	_ = svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")

	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPending {
		t.Fatalf("no amount anywhere: must not settle, status = %q", got.Status)
	}
	ev := lastEvent(webhookRepo)
	if ev == nil || ev.ProcessingError == nil || !strings.HasPrefix(*ev.ProcessingError, "amount_mismatch:") {
		t.Fatalf("event should record amount_mismatch, got %+v", ev)
	}
}

func TestReconcilePayment_ProviderOmitsAmount_SettlesFromStatus(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-NOAMT")
	router.RegisterProvider(settledWithoutAmountProvider(nil))

	result, err := svc.ReconcilePayment(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "updated" || result.CurrentStatus != payment.StatusPaid {
		t.Fatalf("reconcile result = %+v, want updated/paid", result)
	}
	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPaid || len(paymentRepo.markPaidNets) != 1 {
		t.Fatalf("status %q, MarkPaid calls %d; want paid and exactly 1 credit", got.Status, len(paymentRepo.markPaidNets))
	}
}

func TestReconcilePayment_ProviderStatesWrongAmount_StillRejected(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-NOAMT")
	prov := settledWithoutAmountProvider(nil)
	prov.statusResp = &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(1)}
	router.RegisterProvider(prov)

	_, _ = svc.ReconcilePayment(context.Background(), seeded.ID)

	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPending {
		t.Fatalf("a provider-stated wrong amount must still block settlement, status = %q", got.Status)
	}
}
