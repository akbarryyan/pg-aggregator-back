package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/paymentlink"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider/sandbox"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// inactiveMerchantRepo satisfies the service's merchant lookup with a single
// deactivated merchant.
type inactiveMerchantRepo struct{ m *merchant.Merchant }

func (r inactiveMerchantRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error) {
	if id != r.m.ID {
		return nil, merchant.ErrMerchantNotFound
	}
	return r.m, nil
}

func (r inactiveMerchantRepo) SetWebhookSecret(ctx context.Context, id uuid.UUID, secret string) error {
	return nil
}

// slugLinkRepo serves one payment link by slug; everything else is unused by
// the checkout path under test.
type slugLinkRepo struct{ link *paymentlink.PaymentLink }

func (r slugLinkRepo) Create(ctx context.Context, l *paymentlink.PaymentLink) error { return nil }
func (r slugLinkRepo) GetByID(ctx context.Context, id uuid.UUID) (*paymentlink.PaymentLink, error) {
	return r.link, nil
}
func (r slugLinkRepo) GetBySlug(ctx context.Context, slug string) (*paymentlink.PaymentLink, error) {
	if slug != r.link.Slug {
		return nil, paymentlink.ErrPaymentLinkNotFound
	}
	return r.link, nil
}
func (r slugLinkRepo) List(ctx context.Context, merchantID uuid.UUID, environment string, isActive *bool, limit, offset int) ([]*paymentlink.PaymentLink, error) {
	return nil, nil
}
func (r slugLinkRepo) Count(ctx context.Context, merchantID uuid.UUID, environment string, isActive *bool) (int, error) {
	return 0, nil
}
func (r slugLinkRepo) Update(ctx context.Context, l *paymentlink.PaymentLink) error { return nil }
func (r slugLinkRepo) SetActive(ctx context.Context, id uuid.UUID, isActive bool) error {
	return nil
}

// newInactiveMerchantService wires a real PaymentService whose merchant
// lookup reports the merchant as deactivated, plus the payment repo so tests
// can assert nothing was stored.
func newInactiveMerchantService(m *merchant.Merchant) (*service.PaymentService, *fakePaymentRepo) {
	repo := newFakePaymentRepo()
	router := providerPkg.NewProviderRouter()
	sb := sandbox.NewAdapter()
	router.RegisterProvider(sb)
	svc := service.NewPaymentService(
		repo,
		&fakeMerchantProviderConfigRepo{},
		newFakeWebhookEventRepo(),
		router,
		"http://localhost:8080",
	).WithSandboxProvider(sb).WithMerchantCallbackDeps(inactiveMerchantRepo{m}, nil)
	return svc, repo
}

func assertInactiveForbidden(t *testing.T, rec *httptest.ResponseRecorder, repo *fakePaymentRepo) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Body.Len() == 0 || !bytes.Contains(rec.Body.Bytes(), []byte("Merchant account is inactive")) {
		t.Fatalf("expected body to say %q, got %s", "Merchant account is inactive", rec.Body.String())
	}
	if len(repo.byID) != 0 {
		t.Errorf("no payment may be stored for an inactive merchant")
	}
}

func TestRespondCreatePaymentError_MerchantInactive(t *testing.T) {
	rec := httptest.NewRecorder()
	respondCreatePaymentError(rec, merchant.ErrMerchantInactive)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("Merchant account is inactive")) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestPaymentHandler_CreatePayment_InactiveMerchantIs403(t *testing.T) {
	m := &merchant.Merchant{ID: uuid.New(), IsActive: false}
	svc, repo := newInactiveMerchantService(m)
	h := NewPaymentHandler(svc, "http://localhost:3000")

	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, m.ID)
	ctx = context.WithValue(ctx, middleware.MerchantEnvironmentContextKey, "sandbox")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", paymentBody(t, m.ID)).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	assertInactiveForbidden(t, rec, repo)
}

func TestMerchantHandler_CreatePayment_InactiveMerchantIs403(t *testing.T) {
	m := &merchant.Merchant{ID: uuid.New(), IsActive: false}
	svc, repo := newInactiveMerchantService(m)
	h := NewMerchantHandler(svc, nil, nil, "http://localhost:3000")

	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, m.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/merchant/payments", paymentBody(t, m.ID)).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	assertInactiveForbidden(t, rec, repo)
}

func TestAdminHandler_CreatePayment_InactiveMerchantIs403(t *testing.T) {
	m := &merchant.Merchant{ID: uuid.New(), IsActive: false}
	svc, repo := newInactiveMerchantService(m)
	h := NewAdminHandler(svc, "http://localhost:3000")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/payments", paymentBody(t, m.ID))
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	assertInactiveForbidden(t, rec, repo)
}

func TestPaymentLinkHandler_Checkout_InactiveMerchantIs403(t *testing.T) {
	m := &merchant.Merchant{ID: uuid.New(), IsActive: false}
	svc, repo := newInactiveMerchantService(m)
	amount := int64(50000)
	link := &paymentlink.PaymentLink{
		ID:          uuid.New(),
		MerchantID:  m.ID,
		Slug:        "abc123",
		Title:       "Test link",
		AmountType:  paymentlink.AmountTypeFixed,
		Amount:      &amount,
		Currency:    "IDR",
		IsActive:    true, // the link itself is fine; only the merchant is deactivated
		Environment: "sandbox",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	linkSvc := service.NewPaymentLinkService(slugLinkRepo{link}, repo, svc)
	h := NewPaymentLinkHandler(linkSvc, "http://localhost:3000")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/public/payment-links/abc123/checkout", bytes.NewReader([]byte(`{}`)))
	req = mux.SetURLVars(req, map[string]string{"slug": "abc123"})
	rec := httptest.NewRecorder()

	h.InitiatePaymentLinkCheckout(rec, req)

	assertInactiveForbidden(t, rec, repo)
}
