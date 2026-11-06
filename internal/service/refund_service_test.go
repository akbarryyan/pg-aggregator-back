package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

type fakeRefundRepo struct {
	full      bool
	remaining int64
	err       error
	p         *payment.Payment
	calls     int
}

func (f *fakeRefundRepo) RefundWithRemaining(ctx context.Context, paymentID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*ledger.Refund, bool, *payment.Payment, int64, error) {
	f.calls++
	if f.err != nil {
		return nil, false, nil, 0, f.err
	}
	p := *f.p
	if f.full {
		p.Status = payment.StatusRefunded
	}
	return &ledger.Refund{ID: uuid.New(), PaymentID: p.ID, MerchantID: p.MerchantID, Amount: amount, Reason: reason, CreatedBy: adminID},
		f.full, &p, f.remaining, nil
}

type failingCreateCallbackRepo struct{ *fakeMerchantCallbackRepo }

func (failingCreateCallbackRepo) Create(ctx context.Context, d *merchant.CallbackDelivery) error {
	return errors.New("queue down")
}

func refundTestPayment(merchantID uuid.UUID, callbackURL *string) *payment.Payment {
	paidAt := time.Now().Add(-time.Hour)
	return &payment.Payment{ID: uuid.New(), Reference: "PAY-RF1", MerchantID: merchantID, Amount: 10000,
		Currency: payment.CurrencyIDR, Status: payment.StatusPaid, ProviderName: "cashi",
		Environment: payment.EnvironmentProduction, PaidAt: &paidAt, CallbackURL: callbackURL, MerchantNetAmount: 9930}
}

func TestRefundService_FullRefundQueuesOneSignedRefundedCallback(t *testing.T) {
	var gotEvent, gotSig string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEvent, gotSig = r.Header.Get("X-PG-Event"), r.Header.Get("X-PG-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	pay, merchantRepo, callbackRepo := newCallbackTestService(t)
	pay.useLoopbackCallbacks()
	mid := uuid.New()
	merchantRepo.byID[mid] = &merchant.Merchant{ID: mid}
	url := server.URL
	repo := &fakeRefundRepo{full: true, p: refundTestPayment(mid, &url)}
	svc := NewRefundService(repo, nil, pay)

	res, err := svc.Refund(context.Background(), repo.p.ID, 9930, "reason", nil)
	if err != nil || !res.Full || res.PaymentStatus != payment.StatusRefunded || res.RemainingRefundable != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(callbackRepo.created) != 1 {
		t.Fatalf("callbacks queued = %d, want 1", len(callbackRepo.created))
	}
	d := callbackRepo.created[0]
	if d.EventType != "payment.refunded" || d.Status != merchant.CallbackStatusPending || d.PaymentID != repo.p.ID {
		t.Fatalf("delivery = %+v", d)
	}
	if d.RequestPayload["status"] != payment.StatusRefunded || d.RequestPayload["event"] != "payment.refunded" ||
		d.RequestPayload["reference"] != "PAY-RF1" || d.RequestPayload["paid_at"] == nil {
		t.Fatalf("payload = %+v", d.RequestPayload)
	}
	// Same queue and signature as every other event.
	if err := pay.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotEvent != "payment.refunded" || gotSig == "" {
		t.Fatalf("event=%q sig=%q", gotEvent, gotSig)
	}
	secret := *merchantRepo.byID[mid].WebhookSecret
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	if gotSig != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("signature mismatch")
	}
	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil || body["status"] != "refunded" {
		t.Fatalf("body = %s (%v)", gotBody, err)
	}
}

func TestRefundService_PartialRefundSendsNoCallback(t *testing.T) {
	pay, merchantRepo, callbackRepo := newCallbackTestService(t)
	mid := uuid.New()
	merchantRepo.byID[mid] = &merchant.Merchant{ID: mid}
	url := "https://merchant.example/hook"
	repo := &fakeRefundRepo{full: false, remaining: 6930, p: refundTestPayment(mid, &url)}
	res, err := NewRefundService(repo, nil, pay).Refund(context.Background(), repo.p.ID, 3000, "reason", nil)
	if err != nil || res.Full || res.PaymentStatus != payment.StatusPaid || res.RemainingRefundable != 6930 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(callbackRepo.created) != 0 {
		t.Fatalf("partial refund queued %d callbacks", len(callbackRepo.created))
	}
}

func TestRefundService_NoWebhookTargetQueuesNothing(t *testing.T) {
	pay, merchantRepo, callbackRepo := newCallbackTestService(t)
	mid := uuid.New()
	merchantRepo.byID[mid] = &merchant.Merchant{ID: mid} // no webhook_url, payment has no callback_url
	repo := &fakeRefundRepo{full: true, p: refundTestPayment(mid, nil)}
	if _, err := NewRefundService(repo, nil, pay).Refund(context.Background(), repo.p.ID, 9930, "r", nil); err != nil {
		t.Fatal(err)
	}
	if len(callbackRepo.created) != 0 {
		t.Fatalf("queued %d", len(callbackRepo.created))
	}
}

func TestRefundService_EnqueueFailureDoesNotFailRefund(t *testing.T) {
	pay, merchantRepo, _ := newCallbackTestService(t)
	pay.callbackRepo = failingCreateCallbackRepo{newFakeMerchantCallbackRepo()}
	mid := uuid.New()
	merchantRepo.byID[mid] = &merchant.Merchant{ID: mid}
	url := "https://merchant.example/hook"
	repo := &fakeRefundRepo{full: true, p: refundTestPayment(mid, &url)}
	res, err := NewRefundService(repo, nil, pay).Refund(context.Background(), repo.p.ID, 9930, "r", nil)
	if err != nil || !res.Full {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestRefundService_ErrorsSendNothing(t *testing.T) {
	pay, merchantRepo, callbackRepo := newCallbackTestService(t)
	mid := uuid.New()
	merchantRepo.byID[mid] = &merchant.Merchant{ID: mid}
	url := "https://merchant.example/hook"
	repo := &fakeRefundRepo{err: ledger.ErrRefundExceedsNet, p: refundTestPayment(mid, &url)}
	if _, err := NewRefundService(repo, nil, pay).Refund(context.Background(), repo.p.ID, 1, "r", nil); !errors.Is(err, ledger.ErrRefundExceedsNet) {
		t.Fatalf("err = %v", err)
	}
	if len(callbackRepo.created) != 0 {
		t.Fatalf("queued %d", len(callbackRepo.created))
	}
}

func TestRefundService_CancelledRequestStillQueuesCallback(t *testing.T) {
	pay, merchantRepo, callbackRepo := newCallbackTestService(t)
	callbackRepo.failOnCancelledCtx = true
	mid := uuid.New()
	merchantRepo.byID[mid] = &merchant.Merchant{ID: mid}
	url := "https://merchant.example/hook"
	repo := &fakeRefundRepo{full: true, p: refundTestPayment(mid, &url)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewRefundService(repo, nil, pay).Refund(ctx, repo.p.ID, 9930, "r", nil); err != nil {
		t.Fatal(err)
	}
	if len(callbackRepo.created) != 1 {
		t.Fatalf("queued %d", len(callbackRepo.created))
	}
}

func TestEventTypeForStatus_Refunded(t *testing.T) {
	if got := eventTypeForStatus(payment.StatusRefunded); got != "payment.refunded" {
		t.Fatalf("got %q", got)
	}
}
