package service

import (
	"context"
	"errors"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

// maxClaimAttempts bounds the loop that retries when a conflicting claim
// disappears between the failed insert and the read.
const maxClaimAttempts = 3

// claimedCreateTimeout bounds one claimed creation. It must stay below
// idempotency.AbandonedAfter, otherwise a retry could take over a claim whose
// owner is still working and both would create a payment.
const claimedCreateTimeout = 90 * time.Second

type IdempotentPayment struct {
	Payment  *payment.Payment
	Replayed bool
}

func (s *PaymentService) WithIdempotency(repo idempotencyRepository) *PaymentService {
	s.idempotencyRepo = repo
	return s
}

// CreatePaymentIdempotent creates a payment, or replays the one an earlier call
// with the same Idempotency-Key created. Without a key (or without a configured
// store, as in unit-test wiring) it behaves exactly like CreatePayment. The key
// is claimed BEFORE the provider is called so concurrent duplicates cannot both
// charge. Known limit: a crash after the provider charged but before the claim
// is completed leaves an orphan charge (the same risk as before this feature).
func (s *PaymentService) CreatePaymentIdempotent(ctx context.Context, req *payment.CreatePaymentRequest, key string) (*IdempotentPayment, error) {
	if key == "" || s.idempotencyRepo == nil {
		p, err := s.CreatePayment(ctx, req)
		if err != nil {
			return nil, err
		}
		return &IdempotentPayment{Payment: p}, nil
	}
	if err := idempotency.ValidateKey(key); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil { // resolves defaults/environment before hashing
		return nil, err
	}
	hash := idempotency.RequestHash(idempotency.RequestFields{
		Amount:                req.Amount,
		Currency:              req.Currency,
		PaymentMethod:         req.PaymentMethod,
		Description:           req.Description,
		CustomerName:          req.CustomerName,
		CustomerEmail:         req.CustomerEmail,
		CallbackURL:           req.CallbackURL,
		ExpiresInMinutes:      req.ExpiresInMinutes,
		UseCustomMerchantName: req.UseCustomMerchantName,
	})

	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		rec, won, err := s.idempotencyRepo.Claim(ctx, req.MerchantID, req.Environment, key, hash, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if won {
			return s.createClaimed(ctx, rec.ID, req)
		}
		if rec == nil {
			continue // the conflicting claim vanished; try to take it
		}
		if rec.RequestHash != hash {
			return nil, idempotency.ErrKeyReused
		}
		if rec.PaymentID == nil {
			return nil, idempotency.ErrInProgress
		}
		p, err := s.paymentRepo.GetByID(ctx, *rec.PaymentID)
		if err != nil {
			if errors.Is(err, payment.ErrPaymentNotFound) {
				return nil, idempotency.ErrInProgress
			}
			return nil, err
		}
		return &IdempotentPayment{Payment: p, Replayed: true}, nil
	}
	return nil, idempotency.ErrInProgress
}

func (s *PaymentService) createClaimed(ctx context.Context, claimID uuid.UUID, req *payment.CreatePaymentRequest) (*IdempotentPayment, error) {
	// Detached from the request: if the merchant's client disconnects mid-call,
	// cancelling the provider call or DB insert would release the claim and let
	// the retry create a second bill. WithoutCancel keeps ctx values (request id).
	cctx, cancelCreate := context.WithTimeout(context.WithoutCancel(ctx), claimedCreateTimeout)
	defer cancelCreate()
	p, err := s.CreatePayment(cctx, req)
	// The claim must be settled even if the caller disconnected meanwhile.
	dctx, cancel := detached(ctx)
	defer cancel()
	if err != nil {
		if relErr := s.idempotencyRepo.Release(dctx, claimID); relErr != nil {
			logger.ErrorfCtx(ctx, "Failed to release idempotency claim %s: %v", claimID, relErr)
		}
		return nil, err
	}
	if cErr := s.idempotencyRepo.Complete(dctx, claimID, p.ID); cErr != nil {
		if errors.Is(cErr, idempotency.ErrClaimLost) {
			logger.WarnfCtx(ctx, "Idempotency claim %s lost to takeover; payment %s was created but its idempotency claim now belongs to another request", claimID, p.ID)
		} else {
			logger.ErrorfCtx(ctx, "Failed to complete idempotency claim %s for payment %s: %v", claimID, p.ID, cErr)
		}
	}
	return &IdempotentPayment{Payment: p}, nil
}

// PurgeIdempotencyKeys is the hourly job body.
func (s *PaymentService) PurgeIdempotencyKeys(ctx context.Context) error {
	if s.idempotencyRepo == nil {
		return nil
	}
	n, err := s.idempotencyRepo.PurgeOlderThan(ctx, time.Now().UTC().Add(-idempotency.TTL))
	if err != nil {
		return err
	}
	if n > 0 {
		logger.InfofCtx(ctx, "Purged %d expired idempotency keys", n)
	}
	return nil
}
