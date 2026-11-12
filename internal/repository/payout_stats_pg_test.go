package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// insertStatsPayout inserts a payout of `amount` in `status` (satisfying the
// processing-consistency CHECK) requested at the SQL expression requestedAt,
// under a fresh merchant (one open payout per merchant is unique).
func insertStatsPayout(t *testing.T, db *sql.DB, status string, amount int64, requestedAt string) {
	t.Helper()
	m := insertConstraintMerchant(t, db)
	q := `INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder, requested_at, processed_at, reference, reject_reason)
	      VALUES ($1, $2, $3, $4::text, 'BCA', '123', 'H', ` + requestedAt + `,
	      CASE WHEN $4::text = 'requested' THEN NULL ELSE now() END,
	      CASE WHEN $4::text = 'paid' THEN 'REF' END,
	      CASE WHEN $4::text = 'rejected' THEN 'why' END)`
	if _, err := db.Exec(q, uuid.New(), m, amount, status); err != nil {
		t.Fatalf("insert payout %s: %v", status, err)
	}
}

func TestPayoutStatsPG(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewPayoutRepository(db)

	empty, err := repo.Stats(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.ByStatus) != 0 || len(empty.DailyByDay) != 0 || empty.Breakdown == nil || len(empty.Breakdown) != 0 {
		t.Fatalf("empty = %+v", empty)
	}

	insertStatsPayout(t, db, "requested", 100_000, "now()")
	insertStatsPayout(t, db, "requested", 50_000, "now()")
	insertStatsPayout(t, db, "paid", 300_000, "now()")
	insertStatsPayout(t, db, "rejected", 70_000, "now()")
	// Exactly the first instant of the 14-day window (today - 13 days, 00:00): inside.
	insertStatsPayout(t, db, "paid", 11_000, "(CURRENT_DATE - 13 * INTERVAL '1 day')")
	// One second before the window: outside the daily series, still in totals.
	insertStatsPayout(t, db, "paid", 22_000, "(CURRENT_DATE - 13 * INTERVAL '1 day' - INTERVAL '1 second')")
	insertStatsPayout(t, db, "rejected", 5_000, "now() - INTERVAL '60 days'")

	got, err := repo.Stats(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]PayoutStatusAgg{
		"requested": {2, 150_000},
		"paid":      {3, 333_000},
		"rejected":  {2, 75_000},
	}
	for st, w := range want {
		if got.ByStatus[st] != w {
			t.Fatalf("%s = %+v, want %+v", st, got.ByStatus[st], w)
		}
	}
	if len(got.Breakdown) != 3 || got.Breakdown[0] != (StatusCount{"paid", 3}) ||
		got.Breakdown[1] != (StatusCount{"rejected", 2}) || got.Breakdown[2] != (StatusCount{"requested", 2}) {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}

	var today, start string
	if err := db.QueryRow(`SELECT CURRENT_DATE::text, (CURRENT_DATE - 13)::text`).Scan(&today, &start); err != nil {
		t.Fatal(err)
	}
	if g := got.DailyByDay[today]; g != (PayoutDayAgg{4, 520_000}) {
		t.Fatalf("today = %+v, want {4 520000}", g)
	}
	if g := got.DailyByDay[start]; g != (PayoutDayAgg{1, 11_000}) {
		t.Fatalf("window start = %+v, want {1 11000}", g)
	}
	if len(got.DailyByDay) != 2 {
		t.Fatalf("daily = %v, want only today and the window start (older rows excluded)", got.DailyByDay)
	}
}
