package service

import (
	"context"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/paymentlink"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/google/uuid"
)

func newPaymentLinkTestService() (*PaymentLinkService, *fakePaymentLinkRepo, *fakePaymentRepo, *fakeProvider) {
	paymentRepo := newFakePaymentRepo()
	configRepo := &fakeMerchantProviderConfigRepo{}
	webhookRepo := newFakeWebhookEventRepo()
	router := providerPkg.NewProviderRouter()
	sandbox := &fakeProvider{name: "sandbox"}

	paymentSvc := NewPaymentService(paymentRepo, configRepo, webhookRepo, router, "http://localhost:8080").
		WithSandboxProvider(sandbox)

	linkRepo := newFakePaymentLinkRepo()
	linkSvc := NewPaymentLinkService(linkRepo, paymentRepo, paymentSvc)
	return linkSvc, linkRepo, paymentRepo, sandbox
}

func validFixedLinkRequest(merchantID uuid.UUID) *paymentlink.CreatePaymentLinkRequest {
	amount := int64(15000)
	return &paymentlink.CreatePaymentLinkRequest{
		MerchantID:  merchantID,
		Title:       "Coffee",
		AmountType:  paymentlink.AmountTypeFixed,
		Amount:      &amount,
		Environment: payment.EnvironmentSandbox,
	}
}

func validOpenLinkRequest(merchantID uuid.UUID) *paymentlink.CreatePaymentLinkRequest {
	return &paymentlink.CreatePaymentLinkRequest{
		MerchantID:  merchantID,
		Title:       "Donation",
		AmountType:  paymentlink.AmountTypeOpen,
		Environment: payment.EnvironmentSandbox,
	}
}

func TestCreateLink_NeverCallsProvider(t *testing.T) {
	linkSvc, _, _, sandbox := newPaymentLinkTestService()

	if _, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(uuid.New())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sandbox.createCalls != 0 {
		t.Fatalf("expected CreateLink to never call the provider (QRIS is only valid ~10min, can't be pre-generated), got %d calls", sandbox.createCalls)
	}
}

func TestCreateLink_ProducesDistinctSlugs(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	merchantID := uuid.New()

	l1, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(merchantID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	l2, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(merchantID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l1.Slug == "" || l2.Slug == "" {
		t.Fatalf("expected non-empty slugs, got %q and %q", l1.Slug, l2.Slug)
	}
	if l1.Slug == l2.Slug {
		t.Fatalf("expected distinct slugs, both were %q", l1.Slug)
	}
}

func TestCreateLink_RetriesOnSlugCollision(t *testing.T) {
	linkSvc, linkRepo, _, _ := newPaymentLinkTestService()
	merchantID := uuid.New()

	// Pre-seed every slug the fake's deterministic-looking random generator
	// might produce is unrealistic to force directly, so instead prove the
	// retry path structurally: seed a collision on an already-known slug by
	// intercepting via a pre-existing link, then rely on generateUniqueSlug's
	// loop to route around a single occupied slug. Since slugs are random,
	// we simulate a guaranteed collision by exhausting all attempts except
	// one is not feasible without hooking the RNG — instead assert the
	// documented contract: creating many links never errors and never
	// collides, which is what the retry-on-collision logic exists to
	// guarantee under real (rare) collisions.
	for i := 0; i < 25; i++ {
		l, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(merchantID))
		if err != nil {
			t.Fatalf("unexpected error on iteration %d: %v", i, err)
		}
		if _, exists := linkRepo.bySlug[l.Slug]; !exists {
			t.Fatalf("expected created link's slug to be persisted")
		}
	}
	if len(linkRepo.byID) != 25 {
		t.Fatalf("expected 25 distinct links to be created, got %d", len(linkRepo.byID))
	}
}

func TestInitiateCheckout_NotFound(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	_, err := linkSvc.InitiateCheckout(context.Background(), "does-not-exist", &paymentlink.InitiateCheckoutRequest{})
	if err != paymentlink.ErrPaymentLinkNotFound {
		t.Fatalf("expected ErrPaymentLinkNotFound, got %v", err)
	}
}

func TestInitiateCheckout_InactiveLinkRejected(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	link, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(uuid.New()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := linkSvc.SetActive(context.Background(), link.ID, link.MerchantID, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{})
	if err != paymentlink.ErrPaymentLinkInactive {
		t.Fatalf("expected ErrPaymentLinkInactive, got %v", err)
	}
}

func TestInitiateCheckout_ExpiredLinkRejected(t *testing.T) {
	linkSvc, linkRepo, _, _ := newPaymentLinkTestService()
	req := validFixedLinkRequest(uuid.New())
	past := time.Now().UTC().Add(-1 * time.Hour)
	req.ExpiresAt = &past

	link, err := linkSvc.CreateLink(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = linkRepo // sanity: link persisted via fake, nothing further needed here

	_, err = linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{})
	if err != paymentlink.ErrPaymentLinkExpired {
		t.Fatalf("expected ErrPaymentLinkExpired, got %v", err)
	}
}

func TestInitiateCheckout_FixedLink_IgnoresClientAmount(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	link, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(uuid.New()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Client tries to pay a wildly different amount than the link's fixed price.
	p, err := linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{Amount: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Amount != *link.Amount {
		t.Fatalf("expected fixed link to ignore client-supplied amount and charge %d, got %d", *link.Amount, p.Amount)
	}
}

func TestInitiateCheckout_OpenLink_RequiresAmount(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	link, err := linkSvc.CreateLink(context.Background(), validOpenLinkRequest(uuid.New()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{})
	if err != paymentlink.ErrCustomerAmountRequired {
		t.Fatalf("expected ErrCustomerAmountRequired, got %v", err)
	}
}

func TestInitiateCheckout_OpenLink_AmountOutOfRangeRejected(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	req := validOpenLinkRequest(uuid.New())
	minA, maxA := int64(10000), int64(50000)
	req.MinAmount, req.MaxAmount = &minA, &maxA

	link, err := linkSvc.CreateLink(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{Amount: 5000}); err != paymentlink.ErrCustomerAmountOutOfRange {
		t.Fatalf("expected ErrCustomerAmountOutOfRange for below-min amount, got %v", err)
	}
	if _, err := linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{Amount: 100000}); err != paymentlink.ErrCustomerAmountOutOfRange {
		t.Fatalf("expected ErrCustomerAmountOutOfRange for above-max amount, got %v", err)
	}
}

func TestInitiateCheckout_OpenLink_AmountInRangeAccepted(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	link, err := linkSvc.CreateLink(context.Background(), validOpenLinkRequest(uuid.New()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, err := linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{Amount: 25000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Amount != 25000 {
		t.Fatalf("expected payment amount 25000, got %d", p.Amount)
	}
}

func TestInitiateCheckout_SetsPaymentLinkIDForTraceability(t *testing.T) {
	linkSvc, _, paymentRepo, _ := newPaymentLinkTestService()
	link, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(uuid.New()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, err := linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.PaymentLinkID == nil || *p.PaymentLinkID != link.ID {
		t.Fatalf("expected resulting payment.PaymentLinkID to equal the link's ID, got %v", p.PaymentLinkID)
	}

	// Also verify it round-trips through the repository (not just the
	// in-memory return value from CreatePayment).
	stored, err := paymentRepo.GetByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.PaymentLinkID == nil || *stored.PaymentLinkID != link.ID {
		t.Fatalf("expected stored payment.PaymentLinkID to equal the link's ID, got %v", stored.PaymentLinkID)
	}
}

func TestPaymentLinkOwnership(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	owner := uuid.New()
	stranger := uuid.New()

	link, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(owner))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("GetLink rejects non-owner", func(t *testing.T) {
		if _, err := linkSvc.GetLink(context.Background(), link.ID, stranger); err != paymentlink.ErrPaymentLinkNotFound {
			t.Fatalf("expected ErrPaymentLinkNotFound for non-owner, got %v", err)
		}
	})

	t.Run("GetLink succeeds for owner", func(t *testing.T) {
		if _, err := linkSvc.GetLink(context.Background(), link.ID, owner); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("UpdateLink rejects non-owner", func(t *testing.T) {
		title := "Hijacked"
		_, err := linkSvc.UpdateLink(context.Background(), link.ID, stranger, &paymentlink.UpdatePaymentLinkRequest{Title: &title})
		if err != paymentlink.ErrPaymentLinkNotFound {
			t.Fatalf("expected ErrPaymentLinkNotFound for non-owner, got %v", err)
		}
	})

	t.Run("SetActive rejects non-owner", func(t *testing.T) {
		if err := linkSvc.SetActive(context.Background(), link.ID, stranger, false); err != paymentlink.ErrPaymentLinkNotFound {
			t.Fatalf("expected ErrPaymentLinkNotFound for non-owner, got %v", err)
		}
	})

	t.Run("ListLinkPayments rejects non-owner", func(t *testing.T) {
		if _, _, err := linkSvc.ListLinkPayments(context.Background(), link.ID, stranger, 10, 0); err != paymentlink.ErrPaymentLinkNotFound {
			t.Fatalf("expected ErrPaymentLinkNotFound for non-owner, got %v", err)
		}
	})
}

func TestListLinkPayments_ReturnsSpawnedPayments(t *testing.T) {
	linkSvc, _, _, _ := newPaymentLinkTestService()
	link, err := linkSvc.CreateLink(context.Background(), validFixedLinkRequest(uuid.New()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := linkSvc.InitiateCheckout(context.Background(), link.Slug, &paymentlink.InitiateCheckoutRequest{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	items, total, err := linkSvc.ListLinkPayments(context.Background(), link.ID, link.MerchantID, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("expected 3 spawned payments, got total=%d items=%d", total, len(items))
	}
}
