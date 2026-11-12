package service

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
)

var statsNow = time.Date(2026, 11, 10, 15, 30, 0, 0, time.UTC)

func TestStatsWindow_OldestFirst(t *testing.T) {
	w := statsWindow(14, statsNow)
	if len(w) != 14 || w[0].Key != "2026-10-28" || w[13].Key != "2026-11-10" || w[13].Label != "10 Nov" {
		t.Fatalf("window = %+v", w)
	}
}

func TestBuildPayoutStats(t *testing.T) {
	raw := &repository.PayoutStats{
		ByStatus: map[string]repository.PayoutStatusAgg{
			"requested": {Count: 2, Amount: 150_000},
			"paid":      {Count: 3, Amount: 900_000},
			"rejected":  {Count: 1, Amount: 50_000},
		},
		Breakdown: []repository.StatusCount{{Status: "paid", Count: 3}, {Status: "rejected", Count: 1}, {Status: "requested", Count: 2}},
		DailyByDay: map[string]repository.PayoutDayAgg{
			"2026-11-10": {Count: 2, Amount: 100_000},
			"2026-10-28": {Count: 1, Amount: 50_000}, // window start
			"2026-10-27": {Count: 9, Amount: 9},      // outside the window
		},
	}
	got := buildPayoutStats(raw, 14, statsNow)
	if got.RequestedCount != 2 || got.RequestedAmount != 150_000 || got.PaidCount != 3 || got.PaidAmount != 900_000 ||
		got.RejectedCount != 1 || got.RejectedAmount != 50_000 || got.Days != 14 {
		t.Fatalf("totals = %+v", got)
	}
	if len(got.Daily) != 14 || got.Daily[0].Count != 1 || got.Daily[13].Amount != 100_000 || got.Daily[5].Count != 0 {
		t.Fatalf("daily = %+v", got.Daily)
	}
	var sum int64
	for _, p := range got.Daily {
		sum += p.Count
	}
	if sum != 3 {
		t.Fatalf("window sum = %d, want 3 (2026-10-27 is outside)", sum)
	}
	if len(got.Breakdown) != 3 || got.Breakdown[0].Status != "paid" {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}
}

func TestBuildCallbackStats(t *testing.T) {
	raw := &repository.CallbackStats{
		Breakdown: []repository.StatusCount{
			{Status: "failed", Count: 4}, {Status: "pending", Count: 2}, {Status: "sending", Count: 1},
			{Status: "skipped", Count: 5}, {Status: "success", Count: 10},
		},
		DailyByDay: map[string]repository.CallbackDayAgg{
			"2026-11-09": {Success: 3, Failed: 1, Other: 2},
			"2026-09-01": {Success: 99},
		},
	}
	got := buildCallbackStats(raw, 14, statsNow)
	if got.Total != 22 || got.Success != 10 || got.Failed != 4 || got.Pending != 3 || got.Skipped != 5 {
		t.Fatalf("totals = %+v", got)
	}
	d := got.Daily[12]
	if d.Date != "2026-11-09" || d.Success != 3 || d.Failed != 1 || d.Other != 2 || len(got.Daily) != 14 {
		t.Fatalf("daily[12] = %+v (len %d)", d, len(got.Daily))
	}
	if len(got.Breakdown) != 5 {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}
}

func TestBuildWebhookEventStats(t *testing.T) {
	raw := &repository.WebhookEventStats{
		Total: 10, Processed: 6, Failed: 3, Ignored: 1,
		Breakdown:  []repository.StatusCount{{Status: "paid", Count: 6}, {Status: "rejected", Count: 3}, {Status: "received", Count: 1}},
		DailyByDay: map[string]repository.WebhookEventDayAgg{"2026-11-10": {Processed: 4, Failed: 1}},
	}
	got := buildWebhookEventStats(raw, 14, statsNow)
	if got.Total != 10 || got.Processed != 6 || got.Failed != 3 || got.Ignored != 1 || got.Days != 14 {
		t.Fatalf("totals = %+v", got)
	}
	if last := got.Daily[13]; last.Processed != 4 || last.Failed != 1 || len(got.Daily) != 14 {
		t.Fatalf("daily = %+v", got.Daily)
	}
}

func TestBuildStats_EmptyDatabase(t *testing.T) {
	p := buildPayoutStats(&repository.PayoutStats{}, 14, statsNow)
	c := buildCallbackStats(&repository.CallbackStats{}, 14, statsNow)
	l := buildWebhookEventStats(&repository.WebhookEventStats{}, 14, statsNow)
	if len(p.Daily) != 14 || len(c.Daily) != 14 || len(l.Daily) != 14 {
		t.Fatal("daily must have 14 points")
	}
	if p.Breakdown == nil || c.Breakdown == nil || l.Breakdown == nil {
		t.Fatal("breakdown must be non-nil (JSON [])")
	}
	if p.PaidAmount != 0 || c.Total != 0 || l.Total != 0 {
		t.Fatal("expected zeros")
	}
}

func TestBenignWebhookErrors_AreTheSentinelTexts(t *testing.T) {
	got := map[string]bool{}
	for _, m := range benignWebhookErrors() {
		got[m] = true
	}
	for _, want := range []string{
		"webhook event ignored: not a settlement",
		"duplicate webhook event",
		"payment is in terminal status and cannot be updated",
		"test webhook event ignored",
	} {
		if !got[want] {
			t.Fatalf("missing benign message %q in %v", want, got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("benign = %v", got)
	}
}

func TestStats_PropagateRepositoryError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")

	if _, err := NewPayoutService(&stubPayoutRepo{err: boom}, nil, 1).Stats(ctx); !errors.Is(err, boom) {
		t.Fatalf("payouts err = %v", err)
	}

	cdb, cmock, _ := sqlmock.New()
	defer cdb.Close()
	cmock.ExpectQuery("FROM merchant_callback_deliveries").WillReturnError(boom)
	if _, err := NewAdminCallbackService(repository.NewMerchantCallbackRepository(cdb)).Stats(ctx); err == nil {
		t.Fatal("callbacks: expected error")
	}

	ldb, lmock, _ := sqlmock.New()
	defer ldb.Close()
	lmock.ExpectQuery("FROM webhook_events").WillReturnError(boom)
	if _, err := NewAdminLogService(nil, repository.NewWebhookEventRepository(ldb)).Stats(ctx); err == nil {
		t.Fatal("logs: expected error")
	}
}
