package service

import (
	"context"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

type payoutRepository interface {
	Request(ctx context.Context, merchantID uuid.UUID, amount, minAmount int64, requestedBy *uuid.UUID) (*ledger.Payout, error)
	MarkPaid(ctx context.Context, id uuid.UUID, reference string, adminID *uuid.UUID) (*ledger.Payout, error)
	Reject(ctx context.Context, id uuid.UUID, reason string, adminID *uuid.UUID) (*ledger.Payout, error)
	ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Payout, int64, error)
	ListAdmin(ctx context.Context, status string, limit, offset int) ([]*ledger.Payout, int64, error)
	HasOpen(ctx context.Context, merchantID uuid.UUID) (bool, error)
	Stats(ctx context.Context, days int) (*repository.PayoutStats, error)
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

// PayoutDailyPoint is one day of the payout-requests chart.
type PayoutDailyPoint struct {
	Date   string `json:"date"`
	Label  string `json:"label"`
	Count  int64  `json:"count"`
	Amount int64  `json:"amount"`
}

// PayoutStats is the summary payload for the admin Payouts page.
type PayoutStats struct {
	Days            int                `json:"days"`
	RequestedCount  int64              `json:"requested_count"`
	RequestedAmount int64              `json:"requested_amount"`
	PaidCount       int64              `json:"paid_count"`
	PaidAmount      int64              `json:"paid_amount"`
	RejectedCount   int64              `json:"rejected_count"`
	RejectedAmount  int64              `json:"rejected_amount"`
	Daily           []PayoutDailyPoint `json:"daily"`
	Breakdown       []BreakdownPoint   `json:"breakdown"`
}

// Stats returns the payout summary: counts and amounts per status and the
// payouts requested per day over the last pageStatsDays days.
func (s *PayoutService) Stats(ctx context.Context) (*PayoutStats, error) {
	raw, err := s.repo.Stats(ctx, pageStatsDays)
	if err != nil {
		return nil, err
	}
	return buildPayoutStats(raw, pageStatsDays, time.Now()), nil
}

func buildPayoutStats(raw *repository.PayoutStats, days int, now time.Time) *PayoutStats {
	daily := make([]PayoutDailyPoint, 0, days)
	for _, d := range statsWindow(days, now) {
		a := raw.DailyByDay[d.Key]
		daily = append(daily, PayoutDailyPoint{Date: d.Key, Label: d.Label, Count: a.Count, Amount: a.Amount})
	}
	return &PayoutStats{
		Days:            days,
		RequestedCount:  raw.ByStatus[ledger.PayoutRequested].Count,
		RequestedAmount: raw.ByStatus[ledger.PayoutRequested].Amount,
		PaidCount:       raw.ByStatus[ledger.PayoutPaid].Count,
		PaidAmount:      raw.ByStatus[ledger.PayoutPaid].Amount,
		RejectedCount:   raw.ByStatus[ledger.PayoutRejected].Count,
		RejectedAmount:  raw.ByStatus[ledger.PayoutRejected].Amount,
		Daily:           daily,
		Breakdown:       toBreakdown(raw.Breakdown),
	}
}
