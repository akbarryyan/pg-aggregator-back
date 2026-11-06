package repository

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/google/uuid"
)

func insertLedgerPayment(t *testing.T, db *sql.DB, merchantID uuid.UUID, amount, fee int64, status, env string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, fee_amount, payment_method, provider_name, status, description, environment, expires_at)
		 VALUES ($1, $2, $3, $4, $5, 'qris', 'cashi', $6, 'd', $7, now() + interval '1 hour')`,
		id, "REF-"+id.String(), merchantID, amount, fee, status, env); err != nil {
		t.Fatalf("insert payment: %v", err)
	}
	return id
}

func insertLedgerPayout(t *testing.T, db *sql.DB, merchantID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder)
		 VALUES ($1, $2, 50000, $3, 'BCA', '123', 'Holder')`, id, merchantID, status); err != nil {
		t.Fatalf("insert payout (%s): %v", status, err)
	}
	return id
}

func insertLedgerRefund(t *testing.T, db *sql.DB, merchantID, paymentID uuid.UUID, amount int64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO payment_refunds (id, payment_id, merchant_id, amount, reason) VALUES ($1, $2, $3, $4, 'why')`,
		id, paymentID, merchantID, amount); err != nil {
		t.Fatalf("insert refund: %v", err)
	}
	return id
}

func insertLedgerEntry(db *sql.DB, merchantID uuid.UUID, entryType string, amount int64, paymentID, payoutID, refundID *uuid.UUID) error {
	_, err := db.Exec(
		`INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, payment_id, payout_id, refund_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		uuid.New(), merchantID, entryType, amount, paymentID, payoutID, refundID)
	return err
}

func TestLedgerPG_AppendOnly(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	p := insertLedgerPayment(t, db, m, 10000, 0, "paid", "production")
	if err := insertLedgerEntry(db, m, "payment_credit", 9930, &p, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`UPDATE merchant_ledger SET note = 'x'`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE must fail with append-only, got %v", err)
	}
	_, err = db.Exec(`DELETE FROM merchant_ledger`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE must fail with append-only, got %v", err)
	}
}

func TestLedgerPG_OneCreditPerPayment(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	p := insertLedgerPayment(t, db, m, 10000, 0, "paid", "production")
	if err := insertLedgerEntry(db, m, "payment_credit", 9930, &p, nil, nil); err != nil {
		t.Fatal(err)
	}
	wantViolation(t, insertLedgerEntry(db, m, "payment_credit", 9930, &p, nil, nil), "uq_ledger_credit_per_payment")
}

func TestLedgerPG_SignAndReferenceRules(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	p := insertLedgerPayment(t, db, m, 10000, 0, "paid", "production")
	payout := insertLedgerPayout(t, db, m, "requested")

	wantViolation(t, insertLedgerEntry(db, m, "payment_credit", -100, &p, nil, nil), "merchant_ledger_sign_matches_type")
	wantViolation(t, insertLedgerEntry(db, m, "payout_debit", 100, nil, &payout, nil), "merchant_ledger_sign_matches_type")
	wantViolation(t, insertLedgerEntry(db, m, "refund_debit", -100, &p, nil, nil), "merchant_ledger_reference_matches_type")
	wantViolation(t, insertLedgerEntry(db, m, "payment_credit", 100, nil, nil, nil), "merchant_ledger_reference_matches_type")
	wantViolation(t, insertLedgerEntry(db, m, "payout_debit", -100, nil, nil, nil), "merchant_ledger_reference_matches_type")
	wantViolation(t, insertLedgerEntry(db, m, "bogus", 100, nil, nil, nil), "merchant_ledger_entry_type_check")
	wantViolation(t, insertLedgerEntry(db, m, "adjustment", 0, nil, nil, nil), "merchant_ledger_amount_check")
	if err := insertLedgerEntry(db, m, "adjustment", -100, nil, nil, nil); err != nil {
		t.Fatalf("adjustment without references must be accepted: %v", err)
	}
	if err := insertLedgerEntry(db, m, "payout_debit", -100, nil, &payout, nil); err != nil {
		t.Fatalf("valid payout_debit: %v", err)
	}
	wantViolation(t, insertLedgerEntry(db, m, "payout_debit", -100, nil, &payout, nil), "uq_ledger_debit_per_payout")
}

func TestLedgerPG_PayoutConstraints(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	insertLedgerPayout(t, db, m, "requested")
	_, err := db.Exec(
		`INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder)
		 VALUES ($1, $2, 50000, 'requested', 'BCA', '1', 'H')`, uuid.New(), m)
	wantViolation(t, err, "uq_payouts_one_open_per_merchant")

	_, err = db.Exec(
		`INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder, processed_at)
		 VALUES ($1, $2, 50000, 'paid', 'BCA', '1', 'H', now())`, uuid.New(), m)
	wantViolation(t, err, "payouts_processing_consistent")

	// Closed payouts do not block (the unique index is partial) and a
	// complete paid row is accepted.
	if _, err := db.Exec(
		`INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder, processed_at, reference)
		 VALUES ($1, $2, 50000, 'paid', 'BCA', '1', 'H', now(), 'TRX-1')`, uuid.New(), m); err != nil {
		t.Fatalf("complete paid payout must be accepted: %v", err)
	}
}

func TestLedgerPG_PaymentStatusRefundedAndAccountColumns(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	if err := insertConstraintPayment(db, m, 1000, "refunded", nil); err != nil {
		t.Fatalf("refunded must be an accepted status: %v", err)
	}
	wantViolation(t, insertConstraintPayment(db, m, 1000, "bogus", nil), "payments_status_valid")

	_, err := db.Exec(`UPDATE merchants SET payout_bank_name = 'BCA' WHERE id = $1`, m)
	wantViolation(t, err, "merchants_payout_account_all_or_none")
	if _, err := db.Exec(
		`UPDATE merchants SET payout_bank_name = 'BCA', payout_account_number = '1', payout_account_holder = 'H' WHERE id = $1`, m); err != nil {
		t.Fatalf("complete payout account must be accepted: %v", err)
	}

	// Payment creation leaves merchant_net_amount at its default.
	var net int64
	if err := db.QueryRow(`SELECT merchant_net_amount FROM payments WHERE merchant_id = $1 LIMIT 1`, m).Scan(&net); err != nil || net != 0 {
		t.Fatalf("default merchant_net_amount = %d (err %v), want 0", net, err)
	}
	p := insertLedgerPayment(t, db, m, 10000, 0, "paid", "production")
	r := insertLedgerRefund(t, db, m, p, 3000)
	if err := insertLedgerEntry(db, m, "refund_debit", -3000, &p, nil, &r); err != nil {
		t.Fatalf("valid refund_debit: %v", err)
	}
	wantViolation(t, insertLedgerEntry(db, m, "refund_debit", -3000, &p, nil, &r), "uq_ledger_debit_per_refund")
}

func TestLedgerPG_SummaryAndList(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewLedgerRepository(db)
	ctx := context.Background()
	m := insertConstraintMerchant(t, db)

	empty, err := repo.Summary(ctx, m)
	if err != nil || *empty != (ledger.Summary{}) {
		t.Fatalf("empty summary = %+v err %v, want zeros", empty, err)
	}

	p := insertLedgerPayment(t, db, m, 10000, 0, "paid", "production")
	ref := insertLedgerRefund(t, db, m, p, 3000)
	payout := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder, processed_at, reject_reason)
		 VALUES ($1, $2, 5000, 'rejected', 'BCA', '1', 'H', now(), 'no')`, payout, m); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		typ    string
		amount int64
	}{{"payment_credit", 10000}, {"refund_debit", -3000}, {"payout_debit", -5000}, {"payout_reversal", 5000}} {
		pid, oid, rid := (*uuid.UUID)(nil), (*uuid.UUID)(nil), (*uuid.UUID)(nil)
		switch e.typ {
		case "payment_credit":
			pid = &p
		case "refund_debit":
			pid, rid = &p, &ref
		default:
			oid = &payout
		}
		if err := insertLedgerEntry(db, m, e.typ, e.amount, pid, oid, rid); err != nil {
			t.Fatalf("insert %s: %v", e.typ, err)
		}
	}
	// Another merchant's entries must not leak in.
	other := insertConstraintMerchant(t, db)
	if err := insertLedgerEntry(db, other, "adjustment", 999, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Summary(ctx, m)
	want := ledger.Summary{Balance: 7000, TotalCredited: 10000, TotalPaidOut: 0, TotalRefunded: 3000}
	if err != nil || *got != want {
		t.Fatalf("summary = %+v err %v, want %+v", got, err, want)
	}

	entries, total, err := repo.List(ctx, m, 3, 0)
	if err != nil || total != 4 || len(entries) != 3 {
		t.Fatalf("list page 1: total=%d len=%d err=%v, want 4/3", total, len(entries), err)
	}
	rest, _, err := repo.List(ctx, m, 3, 3)
	if err != nil || len(rest) != 1 {
		t.Fatalf("list page 2: len=%d err=%v, want 1", len(rest), err)
	}
	all := append(entries, rest...)
	for i := 1; i < len(all); i++ {
		a, b := all[i-1], all[i]
		if a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID.String() < b.ID.String()) {
			t.Errorf("entries not ordered created_at DESC, id DESC at %d", i)
		}
	}
	for _, e := range all {
		switch e.EntryType {
		case ledger.EntryPaymentCredit:
			if e.PaymentID == nil || *e.PaymentID != p || e.PayoutID != nil {
				t.Errorf("payment_credit references wrong: %+v", e)
			}
		case ledger.EntryRefundDebit:
			if e.RefundID == nil || *e.RefundID != ref {
				t.Errorf("refund_debit refund_id wrong: %+v", e)
			}
		case ledger.EntryPayoutDebit, ledger.EntryPayoutReversal:
			if e.PayoutID == nil || *e.PayoutID != payout {
				t.Errorf("%s payout_id wrong: %+v", e.EntryType, e)
			}
		}
	}
}

// backfillStatements returns the UPDATE + INSERT backfill block of migration 021.
func backfillStatements(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "021_add_merchant_balance_payouts_refunds.sql"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	const begin, end = "-- BACKFILL BEGIN", "-- BACKFILL END"
	i, j := strings.Index(s, begin), strings.Index(s, end)
	if i < 0 || j < i {
		t.Fatalf("%s / %s sentinels not found in migration 021", begin, end)
	}
	return s[i+len(begin) : j]
}

func TestMigration021_BackfillIsIdempotent(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	merchantBears := insertLedgerPayment(t, db, m, 10000, 0, "paid", "production")
	customerBears := insertLedgerPayment(t, db, m, 10000, 70, "paid", "production")
	sandbox := insertLedgerPayment(t, db, m, 10000, 0, "paid", "sandbox")
	pending := insertLedgerPayment(t, db, m, 10000, 0, "pending", "production")
	tiny := insertLedgerPayment(t, db, m, 1, 0, "paid", "production") // net 0: not credited

	whole, err := os.ReadFile(filepath.Join("..", "..", "migrations", "021_add_merchant_balance_payouts_refunds.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(backfillStatements(t)); err != nil {
			t.Fatalf("backfill run %d: %v", i+1, err)
		}
	}
	// Re-applying the whole file (as a redeploy would) must also be harmless.
	if _, err := db.Exec(string(whole)); err != nil {
		t.Fatalf("re-applying migration 021: %v", err)
	}

	for id, want := range map[uuid.UUID]int64{merchantBears: 9930, customerBears: 10000, sandbox: 0, pending: 0, tiny: 0} {
		var net int64
		if err := db.QueryRow(`SELECT merchant_net_amount FROM payments WHERE id = $1`, id).Scan(&net); err != nil || net != want {
			t.Errorf("payment %s net = %d (err %v), want %d", id, net, err, want)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM merchant_ledger WHERE merchant_id = $1`, m).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("ledger rows = %d (err %v), want 2", rows, err)
	}
	s, err := NewLedgerRepository(db).Summary(context.Background(), m)
	if err != nil || s.Balance != 19930 {
		t.Fatalf("balance = %+v err %v, want 19930", s, err)
	}
}

func TestLedgerPG_TruncateRejected(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	p := insertLedgerPayment(t, db, m, 10000, 0, "paid", "production")
	if err := insertLedgerEntry(db, m, "payment_credit", 9930, &p, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`TRUNCATE merchant_ledger`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("TRUNCATE must fail with append-only, got %v", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM merchant_ledger`); n != 1 {
		t.Fatalf("rows = %d", n)
	}
}

func TestLedgerPG_OneReversalPerPayout(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	payout := insertLedgerPayout(t, db, m, "requested")
	if err := insertLedgerEntry(db, m, "payout_reversal", 50000, nil, &payout, nil); err != nil {
		t.Fatal(err)
	}
	wantViolation(t, insertLedgerEntry(db, m, "payout_reversal", 50000, nil, &payout, nil), "uq_ledger_reversal_per_payout")
}

func TestMigration021_StatusConstraintComment(t *testing.T) {
	_, db := newPGRepo(t)
	var c sql.NullString
	if err := db.QueryRow(`SELECT obj_description(oid, 'pg_constraint') FROM pg_constraint
		WHERE conname = 'payments_status_valid' AND conrelid = 'payments'::regclass`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if !c.Valid || !strings.Contains(c.String, "refunded") || !strings.Contains(c.String, "cancelled") {
		t.Fatalf("constraint comment = %+v", c)
	}
	// Re-applying the migration re-issues it (the constraint is re-added).
	whole, err := os.ReadFile(filepath.Join("..", "..", "migrations", "021_add_merchant_balance_payouts_refunds.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(whole)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT obj_description(oid, 'pg_constraint') FROM pg_constraint
		WHERE conname = 'payments_status_valid' AND conrelid = 'payments'::regclass`).Scan(&c); err != nil || !c.Valid {
		t.Fatalf("comment lost after re-apply: %+v %v", c, err)
	}
}
