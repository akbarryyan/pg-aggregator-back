package service

import (
	"context"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

type refundRepository interface {
	RefundWithRemaining(ctx context.Context, paymentID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*ledger.Refund, bool, *payment.Payment, int64, error)
}

type refundLister interface {
	ListRefunds(ctx context.Context, paymentID uuid.UUID) (*ledger.RefundSummary, error)
}

type adjustmentRepository interface {
	Adjust(ctx context.Context, merchantID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*ledger.Entry, error)
}

// refundNotifier queues the merchant callback; PaymentService implements it.
type refundNotifier interface {
	NotifyMerchantPaymentEvent(ctx context.Context, p *payment.Payment, eventType string, latePayment bool)
}

// RefundResult is what an admin refund returns.
type RefundResult struct {
	Refund              *ledger.Refund
	PaymentStatus       string
	RemainingRefundable int64
	Full                bool
}

// RefundService records admin refunds and balance adjustments.
type RefundService struct {
	refunds     refundRepository
	lister      refundLister
	adjustments adjustmentRepository
	notifier    refundNotifier
}

func NewRefundService(refunds refundRepository, adjustments adjustmentRepository, notifier refundNotifier) *RefundService {
	return &RefundService{refunds: refunds, adjustments: adjustments, notifier: notifier}
}

// Refund records the refund and, only when it completes the payment
// (full refund), queues a payment.refunded merchant callback. The callback is
// best-effort after the commit: a failed enqueue is logged by the notifier
// and never fails or undoes the refund.
func (s *RefundService) Refund(ctx context.Context, paymentID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*RefundResult, error) {
	refund, full, p, remaining, err := s.refunds.RefundWithRemaining(ctx, paymentID, amount, reason, adminID)
	if err != nil {
		return nil, err
	}
	if full && s.notifier != nil {
		// The refund is committed: queue the callback even if the admin's
		// request is cancelled now.
		nctx, cancel := detached(ctx)
		defer cancel()
		s.notifier.NotifyMerchantPaymentEvent(nctx, p, eventTypeForStatus(payment.StatusRefunded), false)
	}
	return &RefundResult{Refund: refund, PaymentStatus: p.Status, RemainingRefundable: remaining, Full: full}, nil
}

// WithLister wires the refund history reader.
func (s *RefundService) WithLister(l refundLister) *RefundService {
	s.lister = l
	return s
}

func (s *RefundService) ListRefunds(ctx context.Context, paymentID uuid.UUID) (*ledger.RefundSummary, error) {
	return s.lister.ListRefunds(ctx, paymentID)
}

func (s *RefundService) Adjust(ctx context.Context, merchantID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*ledger.Entry, error) {
	return s.adjustments.Adjust(ctx, merchantID, amount, reason, adminID)
}
