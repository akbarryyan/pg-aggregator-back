package service

import (
	"context"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
)

type balanceLedgerRepository interface {
	Summary(ctx context.Context, merchantID uuid.UUID) (*ledger.Summary, error)
	List(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Entry, int64, error)
	ListBalances(ctx context.Context, negativeOnly bool, limit, offset int) ([]*ledger.MerchantBalance, int64, error)
}

type balanceOpenPayoutChecker interface {
	HasOpen(ctx context.Context, merchantID uuid.UUID) (bool, error)
}

// BalanceOverview is the merchant dashboard balance card. The embedded
// Summary is flattened into the JSON object.
type BalanceOverview struct {
	ledger.Summary
	MinPayoutAmount int64                   `json:"min_payout_amount"`
	PayoutAccount   *merchant.PayoutAccount `json:"payout_account"`
	CanRequest      bool                    `json:"can_request"`
}

// BalanceService reads merchant balances and ledgers.
type BalanceService struct {
	ledger    balanceLedgerRepository
	accounts  payoutAccountRepository
	payouts   balanceOpenPayoutChecker
	minAmount int64
}

func NewBalanceService(ledger balanceLedgerRepository, accounts payoutAccountRepository, payouts balanceOpenPayoutChecker, minAmount int64) *BalanceService {
	return &BalanceService{ledger: ledger, accounts: accounts, payouts: payouts, minAmount: minAmount}
}

// Overview computes the balance card. CanRequest is true only when the payout
// account is set, the balance reaches the minimum and no payout is open.
func (s *BalanceService) Overview(ctx context.Context, merchantID uuid.UUID) (*BalanceOverview, error) {
	sum, err := s.ledger.Summary(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	acc, err := s.accounts.GetPayoutAccount(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	open, err := s.payouts.HasOpen(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	return &BalanceOverview{
		Summary:         *sum,
		MinPayoutAmount: s.minAmount,
		PayoutAccount:   acc,
		CanRequest:      acc != nil && sum.Balance >= s.minAmount && !open,
	}, nil
}

func (s *BalanceService) Ledger(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Entry, int64, error) {
	return s.ledger.List(ctx, merchantID, limit, offset)
}

func (s *BalanceService) Balances(ctx context.Context, negativeOnly bool, limit, offset int) ([]*ledger.MerchantBalance, int64, error) {
	return s.ledger.ListBalances(ctx, negativeOnly, limit, offset)
}
