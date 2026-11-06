package service

import (
	"context"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
)

type payoutRepository interface {
	Request(ctx context.Context, merchantID uuid.UUID, amount, minAmount int64, requestedBy *uuid.UUID) (*ledger.Payout, error)
	MarkPaid(ctx context.Context, id uuid.UUID, reference string, adminID *uuid.UUID) (*ledger.Payout, error)
	Reject(ctx context.Context, id uuid.UUID, reason string, adminID *uuid.UUID) (*ledger.Payout, error)
	ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Payout, int64, error)
	ListAdmin(ctx context.Context, status string, limit, offset int) ([]*ledger.Payout, int64, error)
	HasOpen(ctx context.Context, merchantID uuid.UUID) (bool, error)
}

type payoutAccountRepository interface {
	GetPayoutAccount(ctx context.Context, id uuid.UUID) (*merchant.PayoutAccount, error)
	SetPayoutAccount(ctx context.Context, id uuid.UUID, bank, number, holder string) error
}

// PayoutService wraps the payout repository with the configured minimum and
// the merchant's payout-account rules.
type PayoutService struct {
	repo      payoutRepository
	accounts  payoutAccountRepository
	minAmount int64
}

func NewPayoutService(repo payoutRepository, accounts payoutAccountRepository, minAmount int64) *PayoutService {
	return &PayoutService{repo: repo, accounts: accounts, minAmount: minAmount}
}

func (s *PayoutService) MinAmount() int64 { return s.minAmount }

// SetAccount validates, trims and stores the merchant's payout account. The
// caller has already re-authenticated the user (password).
func (s *PayoutService) SetAccount(ctx context.Context, merchantID uuid.UUID, acc merchant.PayoutAccount) (*merchant.PayoutAccount, error) {
	if err := acc.Normalize(); err != nil {
		return nil, err
	}
	if err := s.accounts.SetPayoutAccount(ctx, merchantID, acc.BankName, acc.AccountNumber, acc.AccountHolder); err != nil {
		return nil, err
	}
	return &acc, nil
}

func (s *PayoutService) Request(ctx context.Context, merchantID uuid.UUID, amount int64, requestedBy *uuid.UUID) (*ledger.Payout, error) {
	return s.repo.Request(ctx, merchantID, amount, s.minAmount, requestedBy)
}

func (s *PayoutService) MarkPaid(ctx context.Context, id uuid.UUID, reference string, adminID *uuid.UUID) (*ledger.Payout, error) {
	return s.repo.MarkPaid(ctx, id, reference, adminID)
}

func (s *PayoutService) Reject(ctx context.Context, id uuid.UUID, reason string, adminID *uuid.UUID) (*ledger.Payout, error) {
	return s.repo.Reject(ctx, id, reason, adminID)
}

func (s *PayoutService) ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Payout, int64, error) {
	return s.repo.ListByMerchant(ctx, merchantID, limit, offset)
}

// ListAdmin lists payouts for the admin queue; status "" means all.
func (s *PayoutService) ListAdmin(ctx context.Context, status string, limit, offset int) ([]*ledger.Payout, int64, error) {
	return s.repo.ListAdmin(ctx, status, limit, offset)
}
