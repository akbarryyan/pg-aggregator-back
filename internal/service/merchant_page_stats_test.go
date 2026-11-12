package service

import (
	"context"
	"errors"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

func TestBuildPaymentLinkStats(t *testing.T) {
	raw := &repository.PaymentLinkStats{
		Total: 5, Active: 3, Fixed: 2, Open: 3, PaidCount: 4, PaidAmount: 400_000,
		DailyByDay: map[string]repository.PaymentLinkDayAgg{
			"2026-11-10": {PaidCount: 2, PaidAmount: 150_000},
			"2026-10-28": {PaidCount: 1, PaidAmount: 11_000}, // window start
			"2026-10-27": {PaidCount: 9, PaidAmount: 9},      // outside the window
		},
	}
	got := buildPaymentLinkStats(raw, 14, statsNow)
	if got.Days != 14 || got.Total != 5 || got.Active != 3 || got.Inactive != 2 || got.Fixed != 2 || got.Open != 3 ||
		got.PaidCount != 4 || got.PaidAmount != 400_000 {
		t.Fatalf("totals = %+v", got)
	}
	if len(got.Daily) != 14 || got.Daily[0].PaidCount != 1 || got.Daily[13].PaidAmount != 150_000 ||
		got.Daily[13].Date != "2026-11-10" || got.Daily[13].Label != "10 Nov" || got.Daily[5].PaidCount != 0 {
		t.Fatalf("daily = %+v", got.Daily)
	}
	if len(got.Breakdown) != 2 || got.Breakdown[0] != (BreakdownPoint{"active", 3}) || got.Breakdown[1] != (BreakdownPoint{"inactive", 2}) {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}

	empty := buildPaymentLinkStats(&repository.PaymentLinkStats{}, 14, statsNow)
	if empty.Breakdown == nil || len(empty.Breakdown) != 0 || len(empty.Daily) != 14 || empty.Daily[0].PaidCount != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

type fakeLinkStatsRepo struct {
	merchant uuid.UUID
	env      string
	days     int
	err      error
}

func (f *fakeLinkStatsRepo) Stats(ctx context.Context, m uuid.UUID, env string, days int) (*repository.PaymentLinkStats, error) {
	f.merchant, f.env, f.days = m, env, days
	if f.err != nil {
		return nil, f.err
	}
	return &repository.PaymentLinkStats{}, nil
}

func TestPaymentLinkService_Stats(t *testing.T) {
	if _, err := NewPaymentLinkService(nil, nil, nil).Stats(context.Background(), uuid.New(), "production"); err == nil {
		t.Fatal("missing stats repository must be an error, not a panic")
	}
	f := &fakeLinkStatsRepo{}
	svc := NewPaymentLinkService(nil, nil, nil).WithStats(f)
	m := uuid.New()
	got, err := svc.Stats(context.Background(), m, "sandbox")
	if err != nil || got.Days != 14 || f.merchant != m || f.env != "sandbox" || f.days != 14 {
		t.Fatalf("got %+v err %v fake %+v", got, err, f)
	}
	f.err = errors.New("boom")
	if _, err := svc.Stats(context.Background(), m, "sandbox"); err == nil {
		t.Fatal("repository error must propagate")
	}
}

func TestClampBalanceDays(t *testing.T) {
	for in, want := range map[int]int{0: 14, -5: 1, 1: 1, 14: 14, 90: 90, 91: 90, 100000: 90} {
		if got := clampBalanceDays(in); got != want {
			t.Errorf("clampBalanceDays(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestBuildBalanceDaily(t *testing.T) {
	raw := map[string]repository.LedgerDayAgg{
		"2026-11-10": {Credited: 100_000, Debited: 40_000},
		"2026-11-08": {Credited: 0, Debited: 5_000},
		"2026-11-01": {Credited: 9, Debited: 9}, // outside a 3-day window
	}
	got := buildBalanceDaily(raw, 3, statsNow)
	if got.Days != 3 || len(got.Daily) != 3 {
		t.Fatalf("got %+v", got)
	}
	want := []BalanceDailyPoint{
		{"2026-11-08", "08 Nov", 0, 5_000},
		{"2026-11-09", "09 Nov", 0, 0},
		{"2026-11-10", "10 Nov", 100_000, 40_000},
	}
	for i, w := range want {
		if got.Daily[i] != w {
			t.Errorf("daily[%d] = %+v, want %+v", i, got.Daily[i], w)
		}
	}
	if full := buildBalanceDaily(nil, 90, statsNow); len(full.Daily) != 90 || full.Daily[0].Date != "2026-08-13" {
		t.Fatalf("90 days = %d first %v", len(full.Daily), full.Daily[0])
	}
}

type fakeLedgerDaily struct {
	days int
	err  error
}

func (f *fakeLedgerDaily) Daily(ctx context.Context, m uuid.UUID, days int) (map[string]repository.LedgerDayAgg, error) {
	f.days = days
	return map[string]repository.LedgerDayAgg{}, f.err
}

func TestBalanceService_Daily(t *testing.T) {
	if _, err := NewBalanceService(nil, nil, nil, 1).Daily(context.Background(), uuid.New(), 14); err == nil {
		t.Fatal("missing daily repository must be an error, not a panic")
	}
	f := &fakeLedgerDaily{}
	svc := NewBalanceService(nil, nil, nil, 1).WithDaily(f)
	for in, want := range map[int]int{0: 14, -1: 1, 7: 7, 500: 90} {
		got, err := svc.Daily(context.Background(), uuid.New(), in)
		if err != nil || f.days != want || got.Days != want || len(got.Daily) != want {
			t.Fatalf("days %d: got %+v err %v repo days %d, want %d", in, got, err, f.days, want)
		}
	}
	f.err = errors.New("boom")
	if _, err := svc.Daily(context.Background(), uuid.New(), 14); err == nil {
		t.Fatal("repository error must propagate")
	}
}
