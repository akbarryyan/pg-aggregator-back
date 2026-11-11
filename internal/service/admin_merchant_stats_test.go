package service

import (
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
)

func TestBuildMerchantStats_FillsEveryDayOldestFirst(t *testing.T) {
	now := time.Date(2026, 11, 10, 15, 30, 0, 0, time.UTC)
	raw := &repository.MerchantStats{
		Total: 12, Active: 9, TotalBalance: 4_500_000, NegativeBalanceCount: 1,
		SignupsByDay: map[string]int64{"2026-11-10": 2, "2026-11-08": 1, "2026-10-01": 7 /* outside the window */},
	}
	got := buildMerchantStats(raw, 14, now)

	if got.Total != 12 || got.Active != 9 || got.Inactive != 3 {
		t.Fatalf("counts = %d/%d/%d, want 12/9/3", got.Total, got.Active, got.Inactive)
	}
	if got.TotalBalance != 4_500_000 || got.NegativeBalanceCount != 1 || got.Days != 14 {
		t.Fatalf("balance/negative/days = %d/%d/%d", got.TotalBalance, got.NegativeBalanceCount, got.Days)
	}
	if len(got.SignupsDaily) != 14 {
		t.Fatalf("daily points = %d, want 14", len(got.SignupsDaily))
	}
	first, last := got.SignupsDaily[0], got.SignupsDaily[13]
	if first.Date != "2026-10-28" || last.Date != "2026-11-10" || last.Label != "10 Nov" {
		t.Fatalf("window = %s..%s (%s), want 2026-10-28..2026-11-10 (10 Nov)", first.Date, last.Date, last.Label)
	}
	counts := map[string]int64{}
	var sum int64
	for _, p := range got.SignupsDaily {
		counts[p.Date] = p.Count
		sum += p.Count
	}
	if counts["2026-11-10"] != 2 || counts["2026-11-08"] != 1 || counts["2026-11-09"] != 0 {
		t.Fatalf("per-day counts wrong: %v", counts)
	}
	if sum != 3 {
		t.Fatalf("sum of window = %d, want 3 (the 2026-10-01 row is outside the window)", sum)
	}
}

func TestBuildMerchantStats_EmptyDatabase(t *testing.T) {
	got := buildMerchantStats(&repository.MerchantStats{SignupsByDay: map[string]int64{}}, 14, time.Now())
	if got.Total != 0 || got.Inactive != 0 || len(got.SignupsDaily) != 14 {
		t.Fatalf("empty stats = %+v", got)
	}
	for _, p := range got.SignupsDaily {
		if p.Count != 0 {
			t.Fatalf("expected all-zero days, got %+v", p)
		}
	}
}
