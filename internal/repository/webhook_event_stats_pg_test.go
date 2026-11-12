package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

func insertStatsWebhookEvent(t *testing.T, db *sql.DB, status string, processed bool, procErr *string, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO webhook_events (id, provider_name, provider_reference, event_type, status, raw_payload, is_processed, processing_error, created_at)
		VALUES ($1, 'cashi', 'ref', 'e', $2, '{}'::jsonb, $3, $4, `+createdAt+`)`,
		uuid.New(), status, processed, procErr); err != nil {
		t.Fatalf("insert webhook event: %v", err)
	}
}

var testBenign = []string{
	"webhook event ignored: not a settlement",
	"duplicate webhook event",
	"payment is in terminal status and cannot be updated",
	"test webhook event ignored",
}

func TestWebhookEventStatsPG(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewWebhookEventRepository(db)

	empty, err := repo.Stats(ctx, 14, testBenign)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || empty.Processed != 0 || empty.Failed != 0 || empty.Ignored != 0 ||
		empty.Breakdown == nil || len(empty.Breakdown) != 0 || len(empty.DailyByDay) != 0 {
		t.Fatalf("empty = %+v", empty)
	}

	e := func(s string) *string { return &s }
	// today:
	insertStatsWebhookEvent(t, db, "paid", true, nil, "now()") // processed x2
	insertStatsWebhookEvent(t, db, "paid", true, nil, "now()")
	insertStatsWebhookEvent(t, db, "ignored", false, e(testBenign[3]), "now()")                       // ignored: test event
	insertStatsWebhookEvent(t, db, "expired", false, e(testBenign[0]), "now()")                       // ignored: not a settlement
	insertStatsWebhookEvent(t, db, "paid", false, e(testBenign[1]), "now()")                          // ignored: duplicate
	insertStatsWebhookEvent(t, db, "paid", false, e(testBenign[2]), "now()")                          // ignored: already terminal
	insertStatsWebhookEvent(t, db, "failed", false, e(testBenign[0]), "now()")                        // benign wins over status failed -> ignored
	insertStatsWebhookEvent(t, db, "rejected", false, e("amount mismatch"), "now()")                  // failed: real error
	insertStatsWebhookEvent(t, db, "verification_failed", false, e("provider says pending"), "now()") // failed: error text
	insertStatsWebhookEvent(t, db, "rejected", false, nil, "now()")                                   // failed: rejected status
	insertStatsWebhookEvent(t, db, "received", false, nil, "now()")                                   // none of them
	// window start (inside, processed), one second before (outside, failed), old.
	insertStatsWebhookEvent(t, db, "paid", true, nil, "(CURRENT_DATE - 13 * INTERVAL '1 day')")
	insertStatsWebhookEvent(t, db, "rejected", false, e("x"), "(CURRENT_DATE - 13 * INTERVAL '1 day' - INTERVAL '1 second')")
	insertStatsWebhookEvent(t, db, "paid", true, nil, "now() - INTERVAL '90 days'")

	got, err := repo.Stats(ctx, 14, testBenign)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 14 || got.Processed != 4 || got.Failed != 4 || got.Ignored != 5 {
		t.Fatalf("total/processed/failed/ignored = %d/%d/%d/%d, want 14/4/4/5", got.Total, got.Processed, got.Failed, got.Ignored)
	}
	// Disjoint: failed + ignored + processed + untouched(received) = total.
	if got.Failed+got.Ignored+got.Processed+1 != got.Total {
		t.Fatalf("buckets overlap: %+v", got)
	}
	counts := map[string]int64{}
	var sum int64
	for _, b := range got.Breakdown {
		counts[b.Status] = b.Count
		sum += b.Count
	}
	if counts["paid"] != 6 || counts["rejected"] != 3 || counts["ignored"] != 1 || counts["expired"] != 1 || counts["failed"] != 1 ||
		counts["verification_failed"] != 1 || counts["received"] != 1 || sum != got.Total {
		t.Fatalf("breakdown = %v", counts)
	}
	var today, start string
	if err := db.QueryRow(`SELECT CURRENT_DATE::text, (CURRENT_DATE - 13)::text`).Scan(&today, &start); err != nil {
		t.Fatal(err)
	}
	// Ignored rows are not in daily.failed.
	if g := got.DailyByDay[today]; g != (WebhookEventDayAgg{Processed: 2, Failed: 3}) {
		t.Fatalf("today = %+v, want processed 2 failed 3 (ignored excluded)", g)
	}
	if g := got.DailyByDay[start]; g != (WebhookEventDayAgg{Processed: 1}) {
		t.Fatalf("window start = %+v, want processed 1", g)
	}
	if len(got.DailyByDay) != 2 {
		t.Fatalf("daily = %v, want 2 days", got.DailyByDay)
	}

	// The benign list is a query argument: with none, those rows are plain errors.
	none, err := repo.Stats(ctx, 14, nil)
	if err != nil {
		t.Fatal(err)
	}
	if none.Ignored != 0 || none.Failed != 9 {
		t.Fatalf("without benign list: %+v", none)
	}
}
