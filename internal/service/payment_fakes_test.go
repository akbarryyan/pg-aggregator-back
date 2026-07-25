package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// ---- fakePaymentRepo -------------------------------------------------

// fakePaymentRepo is an in-memory stand-in for *repository.PaymentRepository.
// It intentionally re-implements the same CanTransitionTo guard as the real
// repository's UpdateStatus, since that defense-in-depth check is part of
// the behavior under test (idempotency / invalid transitions).
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
	if !payment.CanTransitionTo(p.Status, newStatus) {
		return payment.ErrInvalidStatusTransition
	}
	p.Status = newStatus
	p.PaidAt = paidAt
	return nil
}

func (f *fakePaymentRepo) List(ctx context.Context, limit, offset int) ([]*payment.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*payment.Payment, 0, len(f.byID))
	for _, p := range f.byID {
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakePaymentRepo) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]*payment.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*payment.Payment, 0)
	for _, p := range f.byID {
		if p.Status != payment.StatusPending || !p.ExpiresAt.Before(before) {
			continue
		}
		cp := *p
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
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
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repository.AdminPaymentRow, 0)
	for _, p := range f.byID {
		if status != "" && p.Status != status {
			continue
		}
		cp := *p
		out = append(out, repository.AdminPaymentRow{Payment: &cp})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ---- fakeWebhookEventRepo ---------------------------------------------

type fakeWebhookEventRepo struct {
	mu     sync.Mutex
	events map[uuid.UUID]*domainProvider.WebhookEvent
}

func newFakeWebhookEventRepo() *fakeWebhookEventRepo {
	return &fakeWebhookEventRepo{events: map[uuid.UUID]*domainProvider.WebhookEvent{}}
}

func (f *fakeWebhookEventRepo) Create(ctx context.Context, e *domainProvider.WebhookEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	cp := *e
	f.events[e.ID] = &cp
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
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.events[id]
	if !ok {
		return fmt.Errorf("webhook event %s not found", id)
	}
	e.PaymentID = paymentID
	e.ProviderReference = providerReference
	e.EventType = eventType
	e.Status = status
	e.IsProcessed = isProcessed
	e.ProcessingError = processingError
	return nil
}

// ---- fakeMerchantProviderConfigRepo ------------------------------------

type fakeMerchantProviderConfigRepo struct {
	configs []*domainProvider.MerchantProviderConfig
}

func (f *fakeMerchantProviderConfigRepo) ListEnabledByMerchantAndPaymentMethod(
	ctx context.Context, merchantID uuid.UUID, paymentMethod string,
) ([]*domainProvider.MerchantProviderConfig, error) {
	out := make([]*domainProvider.MerchantProviderConfig, 0)
	for _, c := range f.configs {
		if c.MerchantID == merchantID && c.PaymentMethod == paymentMethod && c.IsEnabled {
			out = append(out, c)
		}
	}
	return out, nil
}

// ---- fakeMerchantRepo ---------------------------------------------------

type fakeMerchantRepo struct {
	byID map[uuid.UUID]*merchant.Merchant
}

func newFakeMerchantRepo() *fakeMerchantRepo {
	return &fakeMerchantRepo{byID: map[uuid.UUID]*merchant.Merchant{}}
}

func (f *fakeMerchantRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error) {
	m, ok := f.byID[id]
	if !ok {
		return nil, merchant.ErrMerchantNotFound
	}
	return m, nil
}

func (f *fakeMerchantRepo) SetWebhookSecret(ctx context.Context, id uuid.UUID, secret string) error {
	m, ok := f.byID[id]
	if !ok {
		return nil // mirrors a real UPDATE matching 0 rows: no error
	}
	m.WebhookSecret = &secret
	return nil
}

// ---- fakeMerchantCallbackRepo -------------------------------------------

type fakeMerchantCallbackRepo struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]*merchant.CallbackDelivery
	created []*merchant.CallbackDelivery
}

func newFakeMerchantCallbackRepo() *fakeMerchantCallbackRepo {
	return &fakeMerchantCallbackRepo{byID: map[uuid.UUID]*merchant.CallbackDelivery{}}
}

func (f *fakeMerchantCallbackRepo) Create(ctx context.Context, d *merchant.CallbackDelivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	f.byID[d.ID] = d
	f.created = append(f.created, d)
	return nil
}

func (f *fakeMerchantCallbackRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.CallbackDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("callback delivery not found")
	}
	return d, nil
}

func (f *fakeMerchantCallbackRepo) UpdateResult(ctx context.Context, d *merchant.CallbackDelivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[d.ID] = d
	return nil
}

func (f *fakeMerchantCallbackRepo) ListDueForRetry(ctx context.Context, before time.Time, limit int) ([]merchant.CallbackDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]merchant.CallbackDelivery, 0)
	for _, d := range f.byID {
		if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt == nil || d.NextRetryAt.After(before) {
			continue
		}
		out = append(out, *d)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ---- fakeProvider (PaymentProvider) --------------------------------------

// fakeProvider is a minimal providerPkg.PaymentProvider used to drive
// PaymentService without touching the real Cashi/sandbox adapters.
type fakeProvider struct {
	name string

	createResp *domainProvider.ProviderPaymentResponse
	createErr  error

	statusResp *domainProvider.NormalizedPaymentStatus
	statusErr  error

	validateErr error

	parseResp *domainProvider.ProviderWebhookPayload
	parseErr  error
}

func (f *fakeProvider) GetName() string { return f.name }

func (f *fakeProvider) CreatePayment(ctx context.Context, req *domainProvider.ProviderPaymentRequest) (*domainProvider.ProviderPaymentResponse, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createResp != nil {
		resp := *f.createResp
		return &resp, nil
	}
	return &domainProvider.ProviderPaymentResponse{
		ProviderReference: "REF-" + req.InternalReference,
		ProviderName:      f.name,
		Status:            "pending",
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
	return &domainProvider.NormalizedPaymentStatus{Status: "pending", ProviderReference: providerReference}, nil
}

func (f *fakeProvider) ValidateWebhook(rawPayload []byte, signature string) error {
	return f.validateErr
}

func (f *fakeProvider) ParseWebhook(rawPayload []byte) (*domainProvider.ProviderWebhookPayload, error) {
	if f.parseErr != nil {
		return nil, f.parseErr
	}
	if f.parseResp != nil {
		resp := *f.parseResp
		return &resp, nil
	}
	return nil, fmt.Errorf("not implemented in fakeProvider")
}

func (f *fakeProvider) NormalizeStatus(providerStatus string) string { return providerStatus }

var _ providerPkg.PaymentProvider = (*fakeProvider)(nil)
