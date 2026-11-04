package handler

import (
	"context"
	"sync"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// Fakes below satisfy the unexported repository interfaces declared in
// package service (e.g. paymentRepository, authAdminRepository) purely by
// structural typing — Go interfaces don't need to be named to be
// implemented, so these can live in package handler even though the
// interfaces themselves are private to package service. This mirrors the
// existing fake style in internal/service/payment_fakes_test.go.

// ---- payment repository ----

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

// UpdateStatus mirrors the real repository's guarded update: only allowed
// source statuses move, and a refused move is explained the same way.
func (f *fakePaymentRepo) UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok {
		return "", payment.ErrPaymentNotFound
	}
	if !payment.CanTransitionTo(p.Status, newStatus) {
		if payment.IsTerminalStatus(p.Status) {
			return "", payment.ErrPaymentAlreadyTerminal
		}
		return "", payment.ErrInvalidStatusTransition
	}
	previous := p.Status
	p.Status = newStatus
	p.PaidAt = paidAt
	return previous, nil
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

// ---- merchant provider config repository ----

type fakeMerchantProviderConfigRepo struct{}

func (f *fakeMerchantProviderConfigRepo) ListEnabledByMerchantAndPaymentMethod(
	ctx context.Context, merchantID uuid.UUID, paymentMethod string,
) ([]*domainProvider.MerchantProviderConfig, error) {
	// Empty → PaymentService falls back to ProviderRouter's default providers.
	return nil, nil
}

// ---- webhook event repository ----

type fakeWebhookEventRepo struct {
	mu     sync.Mutex
	events []*domainProvider.WebhookEvent
}

func newFakeWebhookEventRepo() *fakeWebhookEventRepo {
	return &fakeWebhookEventRepo{}
}

func (f *fakeWebhookEventRepo) Create(ctx context.Context, e *domainProvider.WebhookEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	f.events = append(f.events, e)
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

// ---- provider (Cashi/sandbox stand-in) ----

// fakeProvider is a configurable provider.PaymentProvider for handler tests
// that need to control webhook validation/parsing without a real adapter.
type fakeProvider struct {
	name string

	createResp *domainProvider.ProviderPaymentResponse
	createErr  error

	validateErr  error
	parsePayload *domainProvider.ProviderWebhookPayload
	parseErr     error

	statusResp *domainProvider.NormalizedPaymentStatus
	statusErr  error
}

func (f *fakeProvider) GetName() string { return f.name }

func (f *fakeProvider) CreatePayment(ctx context.Context, req *domainProvider.ProviderPaymentRequest) (*domainProvider.ProviderPaymentResponse, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createResp != nil {
		return f.createResp, nil
	}
	ref := "FAKE-" + uuid.New().String()[:8]
	return &domainProvider.ProviderPaymentResponse{
		ProviderReference: ref,
		ProviderName:      f.name,
		Status:            payment.StatusPending,
		Amount:            req.Amount,
		ExpiresAt:         req.ExpiresAt,
	}, nil
}

func (f *fakeProvider) GetPaymentStatus(ctx context.Context, providerReference string) (*domainProvider.NormalizedPaymentStatus, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	if f.statusResp != nil {
		resp := *f.statusResp
		return &resp, nil
	}
	return &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending, ProviderReference: providerReference}, nil
}

func (f *fakeProvider) ValidateWebhook(rawPayload []byte, signature string) error {
	return f.validateErr
}

func (f *fakeProvider) ParseWebhook(rawPayload []byte) (*domainProvider.ProviderWebhookPayload, error) {
	if f.parseErr != nil {
		return nil, f.parseErr
	}
	return f.parsePayload, nil
}

func (f *fakeProvider) NormalizeStatus(providerStatus string) string { return providerStatus }

// ---- admin repository (auth) ----

type fakeAdminRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*admin.Admin
}

func newFakeAdminRepo(admins ...*admin.Admin) *fakeAdminRepo {
	r := &fakeAdminRepo{byID: map[uuid.UUID]*admin.Admin{}}
	for _, a := range admins {
		cp := *a
		r.byID[a.ID] = &cp
	}
	return r
}

func (f *fakeAdminRepo) GetByEmail(ctx context.Context, email string) (*admin.Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.byID {
		if a.Email == email {
			cp := *a
			return &cp, nil
		}
	}
	return nil, admin.ErrAdminNotFound
}

func (f *fakeAdminRepo) GetByID(ctx context.Context, id uuid.UUID) (*admin.Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return nil, admin.ErrAdminNotFound
	}
	cp := *a
	return &cp, nil
}

func (f *fakeAdminRepo) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	return nil
}

func (f *fakeAdminRepo) UpdatePasswordHash(ctx context.Context, id uuid.UUID, passwordHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return admin.ErrAdminNotFound
	}
	now := time.Now().UTC()
	a.PasswordHash = passwordHash
	a.SessionsValidAfter = &now
	return nil
}

func (f *fakeAdminRepo) RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return admin.ErrAdminNotFound
	}
	a.SessionsValidAfter = &at
	return nil
}

func (f *fakeAdminRepo) ExistsByEmailExceptID(ctx context.Context, email string, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.byID {
		if a.Email == email && a.ID != id {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeAdminRepo) UpdateProfile(ctx context.Context, id uuid.UUID, name, email string) (*admin.Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return nil, admin.ErrAdminNotFound
	}
	a.Name = name
	a.Email = email
	cp := *a
	return &cp, nil
}

// ---- merchant + merchant user repositories (auth) ----

type fakeAuthMerchantRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*merchant.Merchant
}

func newFakeAuthMerchantRepo(merchants ...*merchant.Merchant) *fakeAuthMerchantRepo {
	r := &fakeAuthMerchantRepo{byID: map[uuid.UUID]*merchant.Merchant{}}
	for _, m := range merchants {
		cp := *m
		r.byID[m.ID] = &cp
	}
	return r
}

func (f *fakeAuthMerchantRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.byID[id]
	if !ok {
		return nil, merchant.ErrMerchantNotFound
	}
	cp := *m
	return &cp, nil
}

func (f *fakeAuthMerchantRepo) GetByEmail(ctx context.Context, email string) (*merchant.Merchant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.byID {
		if m.Email == email {
			cp := *m
			return &cp, nil
		}
	}
	return nil, merchant.ErrMerchantNotFound
}

func (f *fakeAuthMerchantRepo) Create(ctx context.Context, req *merchant.CreateMerchantRequest) (*merchant.Merchant, error) {
	return nil, nil
}

func (f *fakeAuthMerchantRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return nil
}

type fakeMerchantUserRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*merchant.User
}

func newFakeMerchantUserRepo(users ...*merchant.User) *fakeMerchantUserRepo {
	r := &fakeMerchantUserRepo{byID: map[uuid.UUID]*merchant.User{}}
	for _, u := range users {
		cp := *u
		r.byID[u.ID] = &cp
	}
	return r
}

func (f *fakeMerchantUserRepo) GetByEmail(ctx context.Context, email string) (*merchant.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byID {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, merchant.ErrMerchantUserNotFound
}

func (f *fakeMerchantUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil, merchant.ErrMerchantUserNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *fakeMerchantUserRepo) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	return nil
}

func (f *fakeMerchantUserRepo) UpdateProfile(ctx context.Context, id uuid.UUID, name, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return merchant.ErrMerchantUserNotFound
	}
	u.Name = name
	u.Email = email
	return nil
}

func (f *fakeMerchantUserRepo) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return merchant.ErrMerchantUserNotFound
	}
	now := time.Now().UTC()
	u.PasswordHash = hash
	u.SessionsValidAfter = &now
	return nil
}

func (f *fakeMerchantUserRepo) RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return merchant.ErrMerchantUserNotFound
	}
	u.SessionsValidAfter = &at
	return nil
}

func (f *fakeMerchantUserRepo) Create(ctx context.Context, u *merchant.User) (*merchant.User, error) {
	return nil, nil
}

func (f *fakePaymentRepo) ListReconcileCandidates(ctx context.Context, now time.Time, pendingWindow, expiredWindow time.Duration, limit int, after *payment.ReconcileCursor) ([]*payment.Payment, error) {
	return nil, nil
}

// ---- idempotency repository ----

// fakeIdempotencyRepo is the in-memory twin of repository.IdempotencyRepository
// (same take-over rules); it satisfies the service's private interface
// structurally.
type fakeIdempotencyRepo struct {
	mu   sync.Mutex
	rows map[string]*idempotency.Record
}

func newFakeIdempotencyRepo() *fakeIdempotencyRepo {
	return &fakeIdempotencyRepo{rows: map[string]*idempotency.Record{}}
}

func (f *fakeIdempotencyRepo) Claim(
	ctx context.Context, merchantID uuid.UUID, environment, key, requestHash string, now time.Time,
) (*idempotency.Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := merchantID.String() + "|" + environment + "|" + key
	if ex, ok := f.rows[k]; ok {
		expired := ex.CreatedAt.Before(now.Add(-idempotency.TTL))
		abandoned := ex.PaymentID == nil && ex.CreatedAt.Before(now.Add(-idempotency.AbandonedAfter))
		if !expired && !abandoned {
			cp := *ex
			return &cp, false, nil
		}
	}
	rec := &idempotency.Record{ID: uuid.New(), MerchantID: merchantID, Environment: environment,
		Key: key, RequestHash: requestHash, CreatedAt: now}
	f.rows[k] = rec
	cp := *rec
	return &cp, true, nil
}

func (f *fakeIdempotencyRepo) Complete(ctx context.Context, id, paymentID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.ID == id {
			pid := paymentID
			r.PaymentID = &pid
			return nil
		}
	}
	return idempotency.ErrClaimLost
}

func (f *fakeIdempotencyRepo) Release(ctx context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, r := range f.rows {
		if r.ID == id && r.PaymentID == nil {
			delete(f.rows, k)
		}
	}
	return nil
}

func (f *fakeIdempotencyRepo) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return 0, nil
}

// forgetPayments clears every claim's payment link, modelling a claim whose
// owner is still working (in progress) with its request hash intact.
func (f *fakeIdempotencyRepo) forgetPayments() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		r.PaymentID = nil
	}
}
