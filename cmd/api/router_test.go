package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/handler"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
)

// This file is a router-level integration test: it builds the real
// setupRouter(...) from main.go (not a hand-rolled substitute), so it also
// catches route/method/path-var wiring bugs that per-handler unit tests
// (internal/handler/*_test.go) can't see. It exercises the highest-risk
// business path end-to-end over real HTTP request/response objects:
//
//   merchant API key auth → create payment (production, routed to a
//   provider standing in for Cashi) → provider webhook marks it paid →
//   public status read reflects "paid".
//
// Sandbox is deliberately NOT used for the webhook leg here: sandbox's own
// adapter (internal/provider/sandbox) rejects all webhooks by design (see
// its ValidateWebhook/ParseWebhook — sandbox never receives real provider
// callbacks), so a sandbox-webhook test would be exercising a code path
// that can't happen in production. The production-routing branch (with a
// stand-in "testprov" provider playing Cashi's role) is what actually
// receives webhooks in this codebase.

// ---- minimal in-memory fakes (payment side; API-key side uses sqlmock
// below since MerchantAPIKeyService/AuthService depend on concrete
// repository types, not interfaces — same reasoning as
// internal/repository's sqlmock-based tests) ----

type fakePaymentRepo struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]*payment.Payment
	byRef  map[string]uuid.UUID
	byProv map[string]uuid.UUID
}

func newFakePaymentRepo() *fakePaymentRepo {
	return &fakePaymentRepo{
		byID:   map[uuid.UUID]*payment.Payment{},
		byRef:  map[string]uuid.UUID{},
		byProv: map[string]uuid.UUID{},
	}
}

func (f *fakePaymentRepo) Create(ctx context.Context, p *payment.Payment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	cp := *p
	f.byID[p.ID] = &cp
	f.byRef[p.Reference] = p.ID
	if p.ProviderReference != nil && *p.ProviderReference != "" {
		f.byProv[*p.ProviderReference] = p.ID
	}
	return nil
}

func (f *fakePaymentRepo) GetByID(ctx context.Context, id uuid.UUID) (*payment.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok {
		return nil, payment.ErrPaymentNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakePaymentRepo) GetByReference(ctx context.Context, reference string) (*payment.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byRef[reference]
	if !ok {
		return nil, payment.ErrPaymentNotFound
	}
	cp := *f.byID[id]
	return &cp, nil
}

func (f *fakePaymentRepo) GetByProviderReference(ctx context.Context, providerReference string) (*payment.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byProv[providerReference]
	if !ok {
		return nil, payment.ErrPaymentNotFound
	}
	cp := *f.byID[id]
	return &cp, nil
}

func (f *fakePaymentRepo) UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok {
		return payment.ErrPaymentNotFound
	}
	p.Status = newStatus
	if paidAt != nil {
		p.PaidAt = paidAt
	}
	return nil
}

func (f *fakePaymentRepo) List(ctx context.Context, limit, offset int) ([]*payment.Payment, error) {
	return nil, nil
}
func (f *fakePaymentRepo) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]*payment.Payment, error) {
	return nil, nil
}
func (f *fakePaymentRepo) ListByPaymentLinkID(ctx context.Context, linkID uuid.UUID, limit, offset int) ([]*payment.Payment, error) {
	return nil, nil
}
func (f *fakePaymentRepo) CountByPaymentLinkID(ctx context.Context, linkID uuid.UUID) (int, error) {
	return 0, nil
}
func (f *fakePaymentRepo) ListAdmin(
	ctx context.Context,
	status string,
	search string,
	merchantID *uuid.UUID,
	dateFrom, dateTo *time.Time,
	environment string,
	limit, offset int,
) ([]repository.AdminPaymentRow, error) {
	return nil, nil
}

type fakeMerchantProviderConfigRepo struct{}

func (f *fakeMerchantProviderConfigRepo) ListEnabledByMerchantAndPaymentMethod(
	ctx context.Context, merchantID uuid.UUID, paymentMethod string,
) ([]*domainProvider.MerchantProviderConfig, error) {
	return nil, nil // empty → PaymentService falls back to ProviderRouter defaults
}

type fakeWebhookEventRepo struct{}

func (f *fakeWebhookEventRepo) Create(ctx context.Context, e *domainProvider.WebhookEvent) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return nil
}

func (f *fakeWebhookEventRepo) Finalize(
	ctx context.Context,
	id uuid.UUID,
	paymentID *uuid.UUID,
	providerReference, eventType, status string,
	isProcessed bool,
	processingError *string,
) error {
	return nil
}

// stubProvider plays Cashi's role in the production routing branch: real
// production traffic in main.go is routed to the Cashi adapter the same
// way it's routed to this stub here (via ProviderRouter + payment-method
// registration), so exercising this path exercises the real routing logic.
type stubProvider struct {
	name             string
	fixedProviderRef string
	webhookStatus    string
}

func (p *stubProvider) GetName() string { return p.name }

func (p *stubProvider) CreatePayment(ctx context.Context, req *domainProvider.ProviderPaymentRequest) (*domainProvider.ProviderPaymentResponse, error) {
	return &domainProvider.ProviderPaymentResponse{
		ProviderReference: p.fixedProviderRef,
		ProviderName:      p.name,
		Status:            payment.StatusPending,
		Amount:            req.Amount,
		ExpiresAt:         req.ExpiresAt,
	}, nil
}

func (p *stubProvider) GetPaymentStatus(ctx context.Context, providerReference string) (*domainProvider.NormalizedPaymentStatus, error) {
	return &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending, ProviderReference: providerReference}, nil
}

func (p *stubProvider) ValidateWebhook(rawPayload []byte, signature string) error { return nil }

func (p *stubProvider) ParseWebhook(rawPayload []byte) (*domainProvider.ProviderWebhookPayload, error) {
	return &domainProvider.ProviderWebhookPayload{
		ProviderName:      p.name,
		ProviderReference: p.fixedProviderRef,
		Status:            p.webhookStatus,
	}, nil
}

func (p *stubProvider) NormalizeStatus(providerStatus string) string { return providerStatus }

func hashAPIKeyForTest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func TestRouter_CreatePayment_Webhook_StatusPaid_EndToEnd(t *testing.T) {
	const rawAPIKey = "test-live-secret-key"
	merchantID := uuid.New()
	apiKeyID := uuid.New()
	keyHash := hashAPIKeyForTest(rawAPIKey)

	// ---- API key auth: real repos backed by sqlmock (see file comment) ----
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	apiKeyCols := []string{
		"id", "merchant_id", "name", "key_prefix", "key_hash", "is_active",
		"last_used_at", "revoked_at", "created_at", "updated_at",
	}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM merchant_api_keys\s+WHERE key_hash = \$1`).
		WithArgs(keyHash).
		WillReturnRows(sqlmock.NewRows(apiKeyCols).AddRow(
			apiKeyID, merchantID, "production", rawAPIKey[:8], keyHash, true,
			nil, nil, now, now,
		))

	merchantCols := []string{
		"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "created_at", "updated_at",
	}
	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).
		WithArgs(merchantID).
		WillReturnRows(sqlmock.NewRows(merchantCols).AddRow(
			merchantID, "Acme", "acme@example.com", "", "Acme Business", nil, nil, true, now, now,
		))
	mock.ExpectExec(`UPDATE merchant_api_keys`).WillReturnResult(sqlmock.NewResult(0, 1))

	apiKeyRepo := repository.NewMerchantAPIKeyRepository(db)
	merchantRepo := repository.NewMerchantRepository(db)
	apiKeyService := service.NewMerchantAPIKeyService(apiKeyRepo, merchantRepo)

	// ---- payment side: in-memory fakes + a stand-in "production" provider ----
	paymentRepo := newFakePaymentRepo()
	router := providerPkg.NewProviderRouter()
	stub := &stubProvider{name: "testprov", fixedProviderRef: "TESTPROV-REF-1", webhookStatus: payment.StatusPaid}
	router.RegisterProvider(stub)
	router.RegisterPaymentMethodProvider("qris", "testprov")

	paymentService := service.NewPaymentService(
		paymentRepo,
		&fakeMerchantProviderConfigRepo{},
		&fakeWebhookEventRepo{},
		router,
		"http://localhost:8080",
	)

	paymentHandler := handler.NewPaymentHandler(paymentService, "http://localhost:3000")
	webhookHandler := handler.NewWebhookHandler(paymentService)

	authService := service.NewAuthService(nil, "test-secret")
	authMiddleware := middleware.NewAuthMiddleware(authService)
	merchantAPIAuth := middleware.NewMerchantAPIAuthMiddleware(apiKeyService)
	authRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	publicRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	sensitiveRateLimiter := middleware.NewIPRateLimiter(1000, 1000)

	mux := setupRouter(
		paymentHandler, webhookHandler,
		nil, nil, nil, nil, // authHandler, adminHandler, merchantHandler, paymentLinkHandler — unused routes
		authMiddleware, merchantAPIAuth, authRateLimiter, publicRateLimiter, sensitiveRateLimiter,
	)

	// ---- 1. create payment via the real merchant-API-key-authed route ----
	createBody, _ := json.Marshal(map[string]interface{}{
		"amount":         75000,
		"payment_method": "qris",
		"description":    "Integration test order",
		"environment":    "production",
	})
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(createBody))
	createReq.Header.Set("Authorization", "Bearer "+rawAPIKey)
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("create payment: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created map[string]interface{}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}
	if created["status"] != payment.StatusPending {
		t.Fatalf("expected newly created payment to be pending, got %v", created["status"])
	}
	reference, _ := created["reference"].(string)
	if reference == "" {
		t.Fatalf("expected non-empty reference in create response")
	}

	// ---- 2. public status read shows pending ----
	statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/public/payments/by-reference/"+reference, nil)
	statusRec := httptest.NewRecorder()
	mux.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("public status (pre-webhook): expected 200, got %d: %s", statusRec.Code, statusRec.Body.String())
	}
	var beforeWebhook map[string]interface{}
	_ = json.Unmarshal(statusRec.Body.Bytes(), &beforeWebhook)
	if beforeWebhook["status"] != payment.StatusPending {
		t.Fatalf("expected pending before webhook, got %v", beforeWebhook["status"])
	}

	// ---- 3. provider webhook marks the payment paid ----
	webhookReq := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/testprov", bytes.NewReader([]byte(`{"event":"payment.paid"}`)))
	webhookRec := httptest.NewRecorder()
	mux.ServeHTTP(webhookRec, webhookReq)
	if webhookRec.Code != http.StatusOK {
		t.Fatalf("webhook: expected 200, got %d: %s", webhookRec.Code, webhookRec.Body.String())
	}

	// ---- 4. public status read now reflects paid ----
	statusReq2 := httptest.NewRequest(http.MethodGet, "/api/v1/public/payments/by-reference/"+reference, nil)
	statusRec2 := httptest.NewRecorder()
	mux.ServeHTTP(statusRec2, statusReq2)
	if statusRec2.Code != http.StatusOK {
		t.Fatalf("public status (post-webhook): expected 200, got %d: %s", statusRec2.Code, statusRec2.Body.String())
	}
	var afterWebhook map[string]interface{}
	_ = json.Unmarshal(statusRec2.Body.Bytes(), &afterWebhook)
	if afterWebhook["status"] != payment.StatusPaid {
		t.Fatalf("expected paid after webhook, got %v", afterWebhook["status"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestRouter_CreatePayment_MissingAPIKey_Rejected pins the negative case for
// the same route: no Authorization/X-API-Key header must never reach
// PaymentService at all.
func TestRouter_CreatePayment_MissingAPIKey_Rejected(t *testing.T) {
	paymentRepo := newFakePaymentRepo()
	providerRouter := providerPkg.NewProviderRouter()
	paymentService := service.NewPaymentService(
		paymentRepo, &fakeMerchantProviderConfigRepo{}, &fakeWebhookEventRepo{}, providerRouter, "http://localhost:8080",
	)
	paymentHandler := handler.NewPaymentHandler(paymentService, "http://localhost:3000")
	webhookHandler := handler.NewWebhookHandler(paymentService)

	authService := service.NewAuthService(nil, "test-secret")
	authMiddleware := middleware.NewAuthMiddleware(authService)
	apiKeyService := service.NewMerchantAPIKeyService(nil, nil)
	merchantAPIAuth := middleware.NewMerchantAPIAuthMiddleware(apiKeyService)
	authRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	publicRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	sensitiveRateLimiter := middleware.NewIPRateLimiter(1000, 1000)

	mux := setupRouter(
		paymentHandler, webhookHandler,
		nil, nil, nil, nil,
		authMiddleware, merchantAPIAuth, authRateLimiter, publicRateLimiter, sensitiveRateLimiter,
	)

	body, _ := json.Marshal(map[string]interface{}{
		"amount": 10000, "payment_method": "qris", "description": "no auth", "environment": "production",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without API key, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRouter_LegacyUnauthenticatedProviderRoutingEndpoints_Removed guards
// against reintroducing a critical vulnerability: these exact paths used to
// be wired to ProviderRoutingHandler directly on the bare /api/v1 router,
// with no auth middleware and no auth check inside the service either —
// anyone could rewrite any merchant's payment-provider routing config, or
// mark a provider "unhealthy" to silently halt production payment creation.
// They were dead code from the frontend's perspective (it only ever called
// the properly authenticated /api/v1/admin/... equivalents), so they were
// deleted rather than protected. This test fails loudly (404, not some
// other status) if anyone reintroduces them without auth.
func TestRouter_LegacyUnauthenticatedProviderRoutingEndpoints_Removed(t *testing.T) {
	paymentRepo := newFakePaymentRepo()
	providerRouter := providerPkg.NewProviderRouter()
	paymentService := service.NewPaymentService(
		paymentRepo, &fakeMerchantProviderConfigRepo{}, &fakeWebhookEventRepo{}, providerRouter, "http://localhost:8080",
	)
	paymentHandler := handler.NewPaymentHandler(paymentService, "http://localhost:3000")
	webhookHandler := handler.NewWebhookHandler(paymentService)

	authService := service.NewAuthService(nil, "test-secret")
	authMiddleware := middleware.NewAuthMiddleware(authService)
	apiKeyService := service.NewMerchantAPIKeyService(nil, nil)
	merchantAPIAuth := middleware.NewMerchantAPIAuthMiddleware(apiKeyService)
	authRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	publicRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	sensitiveRateLimiter := middleware.NewIPRateLimiter(1000, 1000)

	mux := setupRouter(
		paymentHandler, webhookHandler,
		nil, nil, nil, nil,
		authMiddleware, merchantAPIAuth, authRateLimiter, publicRateLimiter, sensitiveRateLimiter,
	)

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/merchants/" + uuid.New().String() + "/provider-configs"},
		{http.MethodPost, "/api/v1/merchants/" + uuid.New().String() + "/provider-configs"},
		{http.MethodDelete, "/api/v1/merchants/" + uuid.New().String() + "/provider-configs"},
		{http.MethodGet, "/api/v1/provider-healths"},
		{http.MethodPut, "/api/v1/provider-healths/cashi"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: expected 404 (route must not exist), got %d — this endpoint used to be unauthenticated and security-critical", c.method, c.path, rec.Code)
		}
	}
}

// TestRouter_ChangePassword_RateLimited covers project backlog item #8:
// account-mutation endpoints like change-password were reachable with no
// rate limiting at all once past JWT auth — a leaked/stolen token could
// hammer them indefinitely. This exercises the real setupRouter wiring
// (not a hand-rolled limiter) to prove sensitiveRateLimiter actually sits
// in front of POST /api/v1/auth/change-password: requests within burst get
// past the limiter (and then 401 on the handler's own auth check, since no
// token is sent), and the request beyond burst gets 429 without ever
// reaching the handler.
func TestRouter_ChangePassword_RateLimited(t *testing.T) {
	paymentRepo := newFakePaymentRepo()
	providerRouter := providerPkg.NewProviderRouter()
	paymentService := service.NewPaymentService(
		paymentRepo, &fakeMerchantProviderConfigRepo{}, &fakeWebhookEventRepo{}, providerRouter, "http://localhost:8080",
	)
	paymentHandler := handler.NewPaymentHandler(paymentService, "http://localhost:3000")
	webhookHandler := handler.NewWebhookHandler(paymentService)

	authService := service.NewAuthService(nil, "test-secret")
	authHandler := handler.NewAuthHandler(authService)
	authMiddleware := middleware.NewAuthMiddleware(authService)
	apiKeyService := service.NewMerchantAPIKeyService(nil, nil)
	merchantAPIAuth := middleware.NewMerchantAPIAuthMiddleware(apiKeyService)
	authRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	publicRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	const burst = 3
	sensitiveRateLimiter := middleware.NewIPRateLimiter(1, burst) // 1 req/min, burst 3

	mux := setupRouter(
		paymentHandler, webhookHandler,
		authHandler, nil, nil, nil,
		authMiddleware, merchantAPIAuth, authRateLimiter, publicRateLimiter, sensitiveRateLimiter,
	)

	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", bytes.NewReader([]byte(`{}`)))
		req.RemoteAddr = "203.0.113.7:5555" // fixed IP so every request shares one bucket
		return req
	}

	for i := 0; i < burst; i++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, newReq())
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: expected 401 (past limiter, rejected by handler's own auth check — no token sent), got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newReq())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d (past burst): expected 429, got %d: %s", burst+1, rec.Code, rec.Body.String())
	}
}

// TestRouter_RequestID_WiredForEveryResponse covers project backlog item
// #10: middleware.RequestID must be mounted on the real router (not just
// unit-tested in isolation) so every response — including a 404 for an
// unknown route — carries a correlation ID, and an upstream-supplied ID is
// preserved rather than overwritten.
func TestRouter_RequestID_WiredForEveryResponse(t *testing.T) {
	paymentRepo := newFakePaymentRepo()
	providerRouter := providerPkg.NewProviderRouter()
	paymentService := service.NewPaymentService(
		paymentRepo, &fakeMerchantProviderConfigRepo{}, &fakeWebhookEventRepo{}, providerRouter, "http://localhost:8080",
	)
	paymentHandler := handler.NewPaymentHandler(paymentService, "http://localhost:3000")
	webhookHandler := handler.NewWebhookHandler(paymentService)

	authService := service.NewAuthService(nil, "test-secret")
	authMiddleware := middleware.NewAuthMiddleware(authService)
	apiKeyService := service.NewMerchantAPIKeyService(nil, nil)
	merchantAPIAuth := middleware.NewMerchantAPIAuthMiddleware(apiKeyService)
	authRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	publicRateLimiter := middleware.NewIPRateLimiter(1000, 1000)
	sensitiveRateLimiter := middleware.NewIPRateLimiter(1000, 1000)

	mux := setupRouter(
		paymentHandler, webhookHandler,
		nil, nil, nil, nil,
		authMiddleware, merchantAPIAuth, authRateLimiter, publicRateLimiter, sensitiveRateLimiter,
	)

	// No incoming ID → one must be generated, even for a route that 404s.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/this-route-does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	generated := rec.Header().Get(middleware.RequestIDHeader)
	if generated == "" {
		t.Fatal("expected X-Request-ID to be set even on a 404")
	}

	// Incoming ID → must be echoed back unchanged, not replaced.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set(middleware.RequestIDHeader, "client-supplied-id")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 from /health, got %d", rec2.Code)
	}
	if got := rec2.Header().Get(middleware.RequestIDHeader); got != "client-supplied-id" {
		t.Errorf("expected client-supplied-id to be echoed back, got %q", got)
	}
}
