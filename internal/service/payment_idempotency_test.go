package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

func newIdemService() (*PaymentService, *fakePaymentRepo, *fakeProvider, *fakeIdempotencyRepo) {
	svc, paymentRepo, _, _ := newTestPaymentService()
	sb := &fakeProvider{name: "sandbox"}
	idem := newFakeIdempotencyRepo()
	svc = svc.WithSandboxProvider(sb).WithIdempotency(idem)
	return svc, paymentRepo, sb, idem
}

func sandboxReq(merchantID uuid.UUID) *payment.CreatePaymentRequest {
	r := baseCreateRequest(merchantID)
	r.Environment = payment.EnvironmentSandbox
	return r
}

func TestCreatePaymentIdempotent_NoKeyCreatesTwoPayments(t *testing.T) {
	svc, _, sb, idem := newIdemService()
	m := uuid.New()
	a, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Payment.ID == b.Payment.ID || a.Replayed || b.Replayed {
		t.Error("without a key every call must create a fresh payment")
	}
	if sb.createCalls != 2 || idem.claimCalls != 0 {
		t.Errorf("createCalls=%d claimCalls=%d, want 2/0", sb.createCalls, idem.claimCalls)
	}
}

func TestCreatePaymentIdempotent_SameKeySameRequestReplays(t *testing.T) {
	svc, _, sb, _ := newIdemService()
	m := uuid.New()
	a, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Replayed || !b.Replayed {
		t.Errorf("replayed flags a=%v b=%v, want false/true", a.Replayed, b.Replayed)
	}
	if a.Payment.ID != b.Payment.ID {
		t.Error("replay must return the original payment")
	}
	if sb.createCalls != 1 {
		t.Errorf("provider called %d times, want 1", sb.createCalls)
	}
}

func TestCreatePaymentIdempotent_SameKeyDifferentRequestRejected(t *testing.T) {
	svc, _, sb, _ := newIdemService()
	m := uuid.New()
	if _, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "key-1"); err != nil {
		t.Fatal(err)
	}
	other := sandboxReq(m)
	other.Amount = 99999
	_, err := svc.CreatePaymentIdempotent(context.Background(), other, "key-1")
	if !errors.Is(err, idempotency.ErrKeyReused) {
		t.Fatalf("err = %v, want ErrKeyReused", err)
	}
	if sb.createCalls != 1 {
		t.Errorf("provider called %d times, want 1", sb.createCalls)
	}
}

func TestCreatePaymentIdempotent_InFlightClaimIsInProgress(t *testing.T) {
	svc, _, sb, idem := newIdemService()
	m := uuid.New()
	req := sandboxReq(m)
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	hash := hashOf(req)
	idem.seed(m, req.Environment, "key-1", hash, time.Now().UTC())

	_, err := svc.CreatePaymentIdempotent(context.Background(), req, "key-1")
	if !errors.Is(err, idempotency.ErrInProgress) {
		t.Fatalf("err = %v, want ErrInProgress", err)
	}
	if sb.createCalls != 0 {
		t.Error("provider must not be called while another request holds the claim")
	}
}

func TestCreatePaymentIdempotent_AbandonedClaimTakenOver(t *testing.T) {
	svc, _, sb, idem := newIdemService()
	m := uuid.New()
	req := sandboxReq(m)
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	idem.seed(m, req.Environment, "key-1", hashOf(req),
		time.Now().UTC().Add(-idempotency.AbandonedAfter-time.Minute))

	res, err := svc.CreatePaymentIdempotent(context.Background(), req, "key-1")
	if err != nil {
		t.Fatalf("abandoned claim must be taken over: %v", err)
	}
	if res.Replayed || sb.createCalls != 1 {
		t.Errorf("replayed=%v createCalls=%d, want false/1", res.Replayed, sb.createCalls)
	}
}

func TestCreatePaymentIdempotent_ProviderFailureReleasesClaim(t *testing.T) {
	svc, _, sb, _ := newIdemService()
	m := uuid.New()
	sb.createErr = errors.New("provider down")
	if _, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "key-1"); err == nil {
		t.Fatal("want provider error")
	}
	sb.createErr = nil
	res, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "key-1")
	if err != nil {
		t.Fatalf("retry after a failed attempt must succeed: %v", err)
	}
	if res.Replayed || sb.createCalls != 2 {
		t.Errorf("replayed=%v createCalls=%d, want false/2", res.Replayed, sb.createCalls)
	}
}

func TestCreatePaymentIdempotent_InvalidKeyDoesNotTouchRepo(t *testing.T) {
	svc, _, sb, idem := newIdemService()
	_, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(uuid.New()), "has space")
	if !errors.Is(err, idempotency.ErrKeyInvalid) {
		t.Fatalf("err = %v, want ErrKeyInvalid", err)
	}
	if idem.claimCalls != 0 || sb.createCalls != 0 {
		t.Errorf("claimCalls=%d createCalls=%d, want 0/0", idem.claimCalls, sb.createCalls)
	}
}

func TestCreatePaymentIdempotent_ConcurrentSameKeyCreatesOnce(t *testing.T) {
	svc, paymentRepo, sb, _ := newIdemService()
	m := uuid.New()

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, n)
	got := make([]*IdempotentPayment, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i], results[i] = svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "race")
		}(i)
	}
	close(start)
	wg.Wait()

	if sb.createCalls != 1 {
		t.Fatalf("provider called %d times, want exactly 1", sb.createCalls)
	}
	if len(paymentRepo.byID) != 1 {
		t.Fatalf("%d payments stored, want 1", len(paymentRepo.byID))
	}
	created := 0
	for i := range results {
		switch {
		case results[i] == nil && !got[i].Replayed:
			created++
		case results[i] == nil && got[i].Replayed:
		case errors.Is(results[i], idempotency.ErrInProgress):
		default:
			t.Errorf("unexpected result %d: %v", i, results[i])
		}
	}
	if created != 1 {
		t.Errorf("%d callers created a payment, want 1", created)
	}
}

func TestCreatePaymentIdempotent_CompleteFailureStillReturnsPayment(t *testing.T) {
	svc, _, _, idem := newIdemService()
	idem.completeErr = errors.New("db down")
	res, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(uuid.New()), "key-1")
	if err != nil || res == nil || res.Payment == nil {
		t.Fatalf("payment exists, so it must be returned: res=%v err=%v", res, err)
	}
}

func TestCreatePaymentIdempotent_ScopedPerMerchantAndEnvironment(t *testing.T) {
	svc, _, _, router := newTestPaymentService()
	sb := &fakeProvider{name: "sandbox"}
	cashi := &fakeProvider{name: "cashi"}
	router.RegisterProvider(cashi)
	router.RegisterPaymentMethodProvider("qris", "cashi")
	svc = svc.WithSandboxProvider(sb).WithIdempotency(newFakeIdempotencyRepo())
	m1, m2 := uuid.New(), uuid.New()
	ctx := context.Background()

	a, err := svc.CreatePaymentIdempotent(ctx, sandboxReq(m1), "same")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreatePaymentIdempotent(ctx, sandboxReq(m2), "same")
	if err != nil {
		t.Fatalf("other merchant, same key: %v", err)
	}
	if a.Payment.ID == b.Payment.ID || b.Replayed {
		t.Error("another merchant must get its own payment")
	}

	prod := baseCreateRequest(m1)
	prod.Environment = payment.EnvironmentProduction
	c, err := svc.CreatePaymentIdempotent(ctx, prod, "same")
	if err != nil {
		t.Fatalf("other environment, same key: %v", err)
	}
	if c.Replayed || c.Payment.ID == a.Payment.ID {
		t.Error("another environment must get its own payment")
	}
	if sb.createCalls != 2 || cashi.createCalls != 1 {
		t.Errorf("sandbox=%d cashi=%d, want 2/1", sb.createCalls, cashi.createCalls)
	}
}

func TestPurgeIdempotencyKeys_UsesTTLCutoff(t *testing.T) {
	svc, _, _, idem := newIdemService()
	before := time.Now().UTC()
	if err := svc.PurgeIdempotencyKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if idem.purgeCalls != 1 {
		t.Fatalf("purgeCalls=%d, want 1", idem.purgeCalls)
	}
	want := before.Add(-idempotency.TTL)
	if d := idem.purgeCutoff.Sub(want); d < 0 || d > 5*time.Second {
		t.Errorf("cutoff %v not ~now-24h (%v)", idem.purgeCutoff, want)
	}
}

func hashOf(req *payment.CreatePaymentRequest) string {
	return idempotency.RequestHash(idempotency.RequestFields{
		Amount: req.Amount, Currency: req.Currency, PaymentMethod: req.PaymentMethod,
		Description: req.Description, CustomerName: req.CustomerName, CustomerEmail: req.CustomerEmail,
		CallbackURL: req.CallbackURL, ExpiresInMinutes: req.ExpiresInMinutes,
		UseCustomMerchantName: req.UseCustomMerchantName,
	})
}

func TestCreatePaymentIdempotent_CancelledRequestContextStillCreatesAndCompletes(t *testing.T) {
	svc, _, sb, _ := newIdemService()
	m := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the merchant's client already disconnected

	res, err := svc.CreatePaymentIdempotent(ctx, sandboxReq(m), "key-1")
	if err != nil || res == nil || res.Payment == nil {
		t.Fatalf("claimed creation must survive a cancelled request ctx: res=%v err=%v", res, err)
	}
	again, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(m), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.Payment.ID != res.Payment.ID {
		t.Error("retry with the same key must replay the payment created under the cancelled ctx")
	}
	if sb.createCalls != 1 {
		t.Errorf("provider called %d times, want 1", sb.createCalls)
	}
}

func TestClaimedCreateTimeoutBelowAbandonWindow(t *testing.T) {
	if claimedCreateTimeout >= idempotency.AbandonedAfter {
		t.Fatalf("claimedCreateTimeout %v must stay below AbandonedAfter %v", claimedCreateTimeout, idempotency.AbandonedAfter)
	}
}

func TestCreatePaymentIdempotent_ClaimLostOnCompleteStillReturnsPayment(t *testing.T) {
	svc, _, _, idem := newIdemService()
	idem.completeErr = idempotency.ErrClaimLost
	res, err := svc.CreatePaymentIdempotent(context.Background(), sandboxReq(uuid.New()), "key-1")
	if err != nil || res == nil || res.Payment == nil {
		t.Fatalf("payment exists, so it must be returned: res=%v err=%v", res, err)
	}
}

func TestIdempotencyFake_CompleteUnknownClaimIsLost(t *testing.T) {
	idem := newFakeIdempotencyRepo()
	if err := idem.Complete(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, idempotency.ErrClaimLost) {
		t.Fatalf("err = %v, want ErrClaimLost", err)
	}
}
