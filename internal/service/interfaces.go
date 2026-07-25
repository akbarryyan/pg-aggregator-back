package service

import (
	"context"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// The interfaces below capture exactly the repository methods PaymentService
// depends on. They exist so tests can substitute in-memory fakes instead of a
// live Postgres connection; concrete *repository.X types already satisfy
// these implicitly, so callers (main.go) need no changes.

type paymentRepository interface {
	Create(ctx context.Context, p *payment.Payment) error
	GetByID(ctx context.Context, id uuid.UUID) (*payment.Payment, error)
	GetByReference(ctx context.Context, reference string) (*payment.Payment, error)
	GetByProviderReference(ctx context.Context, providerReference string) (*payment.Payment, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) error
	List(ctx context.Context, limit, offset int) ([]*payment.Payment, error)
	ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]*payment.Payment, error)
	ListAdmin(
		ctx context.Context,
		status string,
		search string,
		merchantID *uuid.UUID,
		dateFrom, dateTo *time.Time,
		environment string,
		limit, offset int,
	) ([]repository.AdminPaymentRow, error)
}

type merchantProviderConfigRepository interface {
	ListEnabledByMerchantAndPaymentMethod(ctx context.Context, merchantID uuid.UUID, paymentMethod string) ([]*domainProvider.MerchantProviderConfig, error)
}

type webhookEventRepository interface {
	Create(ctx context.Context, e *domainProvider.WebhookEvent) error
	Finalize(
		ctx context.Context,
		id uuid.UUID,
		paymentID *uuid.UUID,
		providerReference, eventType, status string,
		isProcessed bool,
		processingError *string,
	) error
}

type merchantRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error)
	SetWebhookSecret(ctx context.Context, id uuid.UUID, secret string) error
}

type merchantCallbackRepository interface {
	Create(ctx context.Context, d *merchant.CallbackDelivery) error
	GetByID(ctx context.Context, id uuid.UUID) (*merchant.CallbackDelivery, error)
	UpdateResult(ctx context.Context, d *merchant.CallbackDelivery) error
	ListDueForRetry(ctx context.Context, before time.Time, limit int) ([]merchant.CallbackDelivery, error)
}
