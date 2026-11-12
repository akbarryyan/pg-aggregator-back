package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// insertLedgerAt appends a ledger entry of the given type/amount whose
// created_at is the SQL expression createdAt (the table is append-only, so the
// timestamp must be set on insert). payment/payout/refund ids are created as
// the entry type requires.
func insertLedgerAt(t *testing.T, db *sql.DB, merchantID uuid.UUID, entryType string, amount int64, createdAt string) {
	t.Helper()
	var paymentID, payoutID, refundID *uuid.UUID
	switch entryType {
	case "payment_credit":
		p := insertLedgerPayment(t, db, merchantID, 100_000, 0, "paid", "production")
		paymentID = &p
	case "refund_debit":
		p := insertLedgerPayment(t, db, merchantID, 100_000, 0, "paid", "production")
		r := insertLedgerRefund(t, db, merchantID, p, 1_000)
		paymentID, refundID = &p, &r
	case "payout_debit", "payout_reversal":
		// A paid payout (processed_at + reference set) so several can coexist
		// for one merchant (only one open payout per merchant is allowed).
		po := uuid.New()
		if _, err := db.Exec(
			`INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder, processed_at, reference)
			 VALUES ($1, $2, 50000, 'paid', 'BCA', '123', 'H', now(), 'REF')`, po, merchantID); err != nil {
			t.Fatalf("insert payout: %v", err)
		}
		payoutID = &po
	}
	if _, err := db.Exec(
		`INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, payment_id, payout_id, refund_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, `+createdAt+`)`,
		uuid.New(), merchantID, entryType, amount, paymentID, payoutID, refundID); err != nil {
		t.Fatalf("insert ledger %s: %v", entryType, err)
	}
}

func TestLedgerDailyPG(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewLedgerRepository(db)
	m := insertConstraintMerchant(t, db)
	other := insertConstraintMerchant(t, db)

	empty, err := repo.Daily(ctx, m, 14)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %#v, want non-nil empty map", empty)
	}

	// Today: two credits, a positive and a negative adjustment, a refund and a
	// payout debit.
	insertLedgerAt(t, db, m, "payment_credit", 60_000, "now()")
	insertLedgerAt(t, db, m, "payment_credit", 40_000, "now()")
	insertLedgerAt(t, db, m, "adjustment", 5_000, "now()")
	insertLedgerAt(t, db, m, "adjustment", -2_000, "now()")
	insertLedgerAt(t, db, m, "refund_debit", -1_000, "now()")
	insertLedgerAt(t, db, m, "payout_debit", -50_000, "now()")
	// Window start: in. One second before: out.
	insertLedgerAt(t, db, m, "payout_reversal", 7_000, "(CURRENT_DATE - 13 * INTERVAL '1 day')")
	insertLedgerAt(t, db, m, "payment_credit", 99_000, "(CURRENT_DATE - 13 * INTERVAL '1 day' - INTERVAL '1 second')")
	// A 3-day window excludes everything older than today-2.
	insertLedgerAt(t, db, m, "adjustment", -300, "(CURRENT_DATE - 2 * INTERVAL '1 day')")

	// Another merchant's entries must change nothing.
	insertLedgerAt(t, db, other, "payment_credit", 777_000, "now()")
	insertLedgerAt(t, db, other, "payout_debit", -555_000, "now()")

	got, err := repo.Daily(ctx, m, 14)
	if err != nil {
		t.Fatal(err)
	}
	var today, start, d2 string
	if err := db.QueryRow(`SELECT CURRENT_DATE::text, (CURRENT_DATE - 13)::text, (CURRENT_DATE - 2)::text`).Scan(&today, &start, &d2); err != nil {
		t.Fatal(err)
	}
	if g := got[today]; g != (LedgerDayAgg{Credited: 105_000, Debited: 53_000}) {
		t.Fatalf("today = %+v, want {105000 53000}", g)
	}
	if g := got[start]; g != (LedgerDayAgg{Credited: 7_000, Debited: 0}) {
		t.Fatalf("window start = %+v, want {7000 0}", g)
	}
	if g := got[d2]; g != (LedgerDayAgg{Credited: 0, Debited: 300}) {
		t.Fatalf("today-2 = %+v, want {0 300}", g)
	}
	if len(got) != 3 {
		t.Fatalf("daily = %v, want 3 days", got)
	}

	short, err := repo.Daily(ctx, m, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(short) != 2 || short[today].Credited != 105_000 || short[d2].Debited != 300 {
		t.Fatalf("3-day window = %v", short)
	}
	one, err := repo.Daily(ctx, m, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[today] != (LedgerDayAgg{105_000, 53_000}) {
		t.Fatalf("1-day window = %v", one)
	}

	oth, err := repo.Daily(ctx, other, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(oth) != 1 || oth[today] != (LedgerDayAgg{777_000, 555_000}) {
		t.Fatalf("other = %v", oth)
	}
}
