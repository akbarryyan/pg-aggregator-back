package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// insertStatsLink inserts a payment link for the merchant (fixed or open amount).
func insertStatsLink(t *testing.T, db *sql.DB, merchantID uuid.UUID, amountType string, active bool) uuid.UUID {
	return insertStatsLinkEnv(t, db, merchantID, amountType, active, "production")
}

func insertStatsLinkEnv(t *testing.T, db *sql.DB, merchantID uuid.UUID, amountType string, active bool, env string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var amount any
	if amountType == "fixed" {
		amount = int64(10_000)
	}
	if _, err := db.Exec(
		`INSERT INTO payment_links (id, merchant_id, slug, title, amount_type, amount, is_active, environment)
		 VALUES ($1, $2, $3, 't', $4, $5, $6, $7)`,
		id, merchantID, "s-"+id.String(), amountType, amount, active, env); err != nil {
		t.Fatalf("insert link: %v", err)
	}
	return id
}

// insertLinkPayment inserts a payment spawned by linkID (NULL = direct payment)
// with paid_at set to the SQL expression paidAt.
func insertLinkPayment(t *testing.T, db *sql.DB, merchantID uuid.UUID, linkID *uuid.UUID, amount int64, status, env, paidAt string) {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, payment_method, provider_name, status, description, environment, expires_at, payment_link_id, paid_at)
		 VALUES ($1, $2, $3, $4, 'qris', 'cashi', $5, 'd', $6, now() + interval '1 hour', $7, `+paidAt+`)`,
		id, "REF-"+id.String(), merchantID, amount, status, env, linkID); err != nil {
		t.Fatalf("insert link payment: %v", err)
	}
}

func TestPaymentLinkStatsPG(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewPaymentLinkRepository(db)
	m := insertConstraintMerchant(t, db)
	other := insertConstraintMerchant(t, db)

	// Empty merchant: zeros and a non-nil map.
	empty, err := repo.Stats(ctx, m, "production", 14)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total+empty.Active+empty.Fixed+empty.Open+empty.PaidCount+empty.PaidAmount != 0 || empty.DailyByDay == nil || len(empty.DailyByDay) != 0 {
		t.Fatalf("empty = %+v", empty)
	}

	l1 := insertStatsLink(t, db, m, "fixed", true)
	l2 := insertStatsLink(t, db, m, "open", true)
	insertStatsLink(t, db, m, "open", false)
	// Sandbox links of the same merchant: 2 active (1 fixed, 1 open) + 1 inactive fixed.
	insertStatsLinkEnv(t, db, m, "fixed", true, "sandbox")
	insertStatsLinkEnv(t, db, m, "open", true, "sandbox")
	insertStatsLinkEnv(t, db, m, "fixed", false, "sandbox")

	insertLinkPayment(t, db, m, &l1, 100_000, "paid", "production", "now()")
	insertLinkPayment(t, db, m, &l2, 50_000, "paid", "production", "now()")
	// Not counted: unpaid, direct payment (no link), sandbox.
	insertLinkPayment(t, db, m, &l1, 7_000, "pending", "production", "NULL")
	insertLinkPayment(t, db, m, nil, 9_000, "paid", "production", "now()")
	insertLinkPayment(t, db, m, &l1, 3_000, "paid", "sandbox", "now()")
	// Window start (today - 13 days 00:00) is in; one second earlier is out of
	// the daily series but still in the totals.
	insertLinkPayment(t, db, m, &l1, 11_000, "paid", "production", "(CURRENT_DATE - 13 * INTERVAL '1 day')")
	insertLinkPayment(t, db, m, &l1, 22_000, "paid", "production", "(CURRENT_DATE - 13 * INTERVAL '1 day' - INTERVAL '1 second')")

	// Another merchant with links and link payments must change nothing.
	o1 := insertStatsLink(t, db, other, "fixed", true)
	insertStatsLink(t, db, other, "open", false)
	insertStatsLinkEnv(t, db, other, "fixed", true, "sandbox")
	insertStatsLinkEnv(t, db, other, "open", true, "sandbox")
	insertStatsLinkEnv(t, db, other, "open", true, "sandbox")
	insertLinkPayment(t, db, other, &o1, 999_000, "paid", "production", "now()")
	insertLinkPayment(t, db, other, &o1, 888_000, "paid", "sandbox", "now()")

	got, err := repo.Stats(ctx, m, "production", 14)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || got.Active != 2 || got.Fixed != 1 || got.Open != 2 {
		t.Fatalf("links = %+v", got)
	}
	if got.PaidCount != 4 || got.PaidAmount != 183_000 {
		t.Fatalf("paid = %d / %d, want 4 / 183000", got.PaidCount, got.PaidAmount)
	}
	var today, start string
	if err := db.QueryRow(`SELECT CURRENT_DATE::text, (CURRENT_DATE - 13)::text`).Scan(&today, &start); err != nil {
		t.Fatal(err)
	}
	if g := got.DailyByDay[today]; g != (PaymentLinkDayAgg{2, 150_000}) {
		t.Fatalf("today = %+v, want {2 150000}", g)
	}
	if g := got.DailyByDay[start]; g != (PaymentLinkDayAgg{1, 11_000}) {
		t.Fatalf("window start = %+v, want {1 11000}", g)
	}
	if len(got.DailyByDay) != 2 {
		t.Fatalf("daily = %v, want only today and window start", got.DailyByDay)
	}

	// Sandbox: only the sandbox payment and the sandbox links (production links ignored).
	sb, err := repo.Stats(ctx, m, "sandbox", 14)
	if err != nil {
		t.Fatal(err)
	}
	if sb.PaidCount != 1 || sb.PaidAmount != 3_000 || sb.Total != 3 || sb.Active != 2 || sb.Fixed != 2 || sb.Open != 1 {
		t.Fatalf("sandbox = %+v", sb)
	}
	if g := sb.DailyByDay[today]; g != (PaymentLinkDayAgg{1, 3_000}) || len(sb.DailyByDay) != 1 {
		t.Fatalf("sandbox daily = %v", sb.DailyByDay)
	}

	// The other merchant sees only its own data.
	oth, err := repo.Stats(ctx, other, "production", 14)
	if err != nil {
		t.Fatal(err)
	}
	if oth.Total != 2 || oth.Active != 1 || oth.PaidCount != 1 || oth.PaidAmount != 999_000 {
		t.Fatalf("other = %+v", oth)
	}
}
