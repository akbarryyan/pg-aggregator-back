package service

import (
	"context"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/paymentlink"
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
	UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) (string, error)
	MarkPaid(ctx context.Context, id uuid.UUID, paidAt *time.Time, merchantNet int64) (string, error)
	List(ctx context.Context, limit, offset int) ([]*payment.Payment, error)
	ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]*payment.Payment, error)
	ListReconcileCandidates(ctx context.Context, now time.Time, pendingWindow, expiredWindow time.Duration, limit int, after *payment.ReconcileCursor) ([]*payment.Payment, error)
	ListByPaymentLinkID(ctx context.Context, linkID uuid.UUID, limit, offset int) ([]*payment.Payment, error)
	CountByPaymentLinkID(ctx context.Context, linkID uuid.UUID) (int, error)
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

type idempotencyRepository interface {
	Claim(ctx context.Context, merchantID uuid.UUID, environment, key, requestHash string, now time.Time) (*idempotency.Record, bool, error)
	Complete(ctx context.Context, id, paymentID uuid.UUID) error
	Release(ctx context.Context, id uuid.UUID) error
	PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
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
	// ClaimDue marks up to limit pending deliveries (plus 'sending' ones
	// untouched for staleAfter, left by a dead worker) as 'sending' and
	// returns them, so concurrent workers never send the same row twice.
	ClaimDue(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]merchant.CallbackDelivery, error)
	// RequeueDueFailed turns failed deliveries whose retry time has come into
	// new pending attempts and clears their schedule; returns how many.
	RequeueDueFailed(ctx context.Context, now time.Time, limit int) (int64, error)
	// ClearRetrySchedule nulls next_retry_at on a failed row that still has
	// one (and touches nothing else); reports whether a row changed.
	ClearRetrySchedule(ctx context.Context, id uuid.UUID, now time.Time) (bool, error)
	// ReleaseClaimed puts the given rows back to 'pending' if they are still
	// 'sending' (claimed but never sent); returns how many.
	ReleaseClaimed(ctx context.Context, ids []uuid.UUID, now time.Time) (int64, error)
}

type paymentLinkRepository interface {
	Create(ctx context.Context, l *paymentlink.PaymentLink) error
	GetByID(ctx context.Context, id uuid.UUID) (*paymentlink.PaymentLink, error)
	GetBySlug(ctx context.Context, slug string) (*paymentlink.PaymentLink, error)
	List(ctx context.Context, merchantID uuid.UUID, environment string, isActive *bool, limit, offset int) ([]*paymentlink.PaymentLink, error)
	Count(ctx context.Context, merchantID uuid.UUID, environment string, isActive *bool) (int, error)
	Update(ctx context.Context, l *paymentlink.PaymentLink) error
	SetActive(ctx context.Context, id uuid.UUID, isActive bool) error
}
