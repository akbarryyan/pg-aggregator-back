package service

import (
	"context"
	"errors"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
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
	daily     ledgerDailyRepository
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

// Daily-series window bounds of the Balance page chart.
const (
	defaultBalanceDays = 14
	maxBalanceDays     = 90
)

// ledgerDailyRepository is the per-day ledger aggregate;
// *repository.LedgerRepository satisfies it.
type ledgerDailyRepository interface {
	Daily(ctx context.Context, merchantID uuid.UUID, days int) (map[string]repository.LedgerDayAgg, error)
}

// WithDaily wires the daily ledger aggregate (kept out of the constructor so
// existing wiring and fakes stay unchanged).
func (s *BalanceService) WithDaily(r ledgerDailyRepository) *BalanceService {
	s.daily = r
	return s
}

// BalanceDailyPoint is one day of the credited/debited chart.
type BalanceDailyPoint struct {
	Date     string `json:"date"`
	Label    string `json:"label"`
	Credited int64  `json:"credited"`
	Debited  int64  `json:"debited"`
}

// BalanceDaily is the payload of the merchant balance chart.
type BalanceDaily struct {
	Days  int                 `json:"days"`
	Daily []BalanceDailyPoint `json:"daily"`
}

// clampBalanceDays maps days to 1..maxBalanceDays; zero (unset) means the default.
func clampBalanceDays(days int) int {
	switch {
	case days == 0:
		return defaultBalanceDays
	case days < 1:
		return 1
	case days > maxBalanceDays:
		return maxBalanceDays
	}
	return days
}

// Daily returns the merchant's credited/debited ledger movement for each of
// the last `days` days (clamped to 1..90, default 14); empty days are 0.
func (s *BalanceService) Daily(ctx context.Context, merchantID uuid.UUID, days int) (*BalanceDaily, error) {
	if s.daily == nil {
		return nil, errors.New("ledger daily repository not configured")
	}
	days = clampBalanceDays(days)
	raw, err := s.daily.Daily(ctx, merchantID, days)
	if err != nil {
		return nil, err
	}
	return buildBalanceDaily(raw, days, time.Now()), nil
}

func buildBalanceDaily(raw map[string]repository.LedgerDayAgg, days int, now time.Time) *BalanceDaily {
	points := make([]BalanceDailyPoint, 0, days)
	for _, d := range statsWindow(days, now) {
		a := raw[d.Key]
		points = append(points, BalanceDailyPoint{Date: d.Key, Label: d.Label, Credited: a.Credited, Debited: a.Debited})
	}
	return &BalanceDaily{Days: days, Daily: points}
}
