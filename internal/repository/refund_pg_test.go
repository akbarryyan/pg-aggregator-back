package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

var refundPaidAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// refundFixture inserts a payment of 10000 (fee 0, net 9930) in the given
// status/environment with a fixed paid_at and, when paid in production, its
// credit in the ledger.
func refundFixture(t *testing.T, status, env string) (*RefundRepository, *sql.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	id := insertLedgerPayment(t, db, m, 10000, 0, status, env)
	if _, err := db.Exec(`UPDATE payments SET merchant_net_amount = 9930, paid_at = $2 WHERE id = $1`, id, refundPaidAt); err != nil {
		t.Fatal(err)
	}
	if status == "paid" && env == "production" {
		if err := insertLedgerEntry(db, m, "payment_credit", 9930, &id, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return NewRefundRepository(db), db, m, id
}

func refundTotals(t *testing.T, db *sql.DB, paymentID uuid.UUID) (refunds, ledgerSum int64, ledgerRows int) {
	t.Helper()
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payment_refunds WHERE payment_id=$1`, paymentID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(-SUM(amount),0), count(*) FROM merchant_ledger WHERE payment_id=$1 AND entry_type='refund_debit'`, paymentID).Scan(&ledgerSum, &ledgerRows); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRefundPG_PartialThenFull(t *testing.T) {
	repo, db, m, id := refundFixture(t, "paid", "production")
	ctx := context.Background()
	admin := uuid.New()

	ref, full, p, err := repo.Refund(ctx, id, 3000, "  customer asked  ", &admin)
	if err != nil || full {
		t.Fatalf("partial: full=%v err=%v", full, err)
	}
	if p.Status != payment.StatusPaid || ref.Amount != 3000 || ref.Reason != "customer asked" || ref.CreatedBy == nil || *ref.CreatedBy != admin {
		t.Fatalf("partial result: %+v / %+v", ref, p)
	}
	if st, _ := pgStatus(t, db, id); st != "paid" {
		t.Fatalf("status = %q", st)
	}
	// Refund row and ledger row agree with the payment's merchant and with each other.
	var rm, rp, lm, lp, lr uuid.UUID
	var la int64
	if err := db.QueryRow(`SELECT merchant_id, payment_id FROM payment_refunds WHERE id=$1`, ref.ID).Scan(&rm, &rp); err != nil {
		t.Fatal(err)
	}
	var lby uuid.UUID
	if err := db.QueryRow(`SELECT merchant_id, payment_id, refund_id, amount, created_by FROM merchant_ledger WHERE entry_type='refund_debit' AND payment_id=$1`, id).
		Scan(&lm, &lp, &lr, &la, &lby); err != nil {
		t.Fatal(err)
	}
	if rm != m || lm != m || rp != id || lp != id || lr != ref.ID || la != -3000 || lby != admin {
		t.Fatalf("rows disagree: refund(m=%v p=%v) ledger(m=%v p=%v r=%v a=%d by=%v) want m=%v p=%v r=%v", rm, rp, lm, lp, lr, la, lby, m, id, ref.ID)
	}

	_, full, p, rem, err := func() (*ledger.Refund, bool, *payment.Payment, int64, error) {
		return repo.RefundWithRemaining(ctx, id, 6930, "rest", &admin)
	}()
	if err != nil || !full || rem != 0 || p.Status != payment.StatusRefunded {
		t.Fatalf("full: full=%v rem=%d p=%+v err=%v", full, rem, p, err)
	}
	if st, _ := pgStatus(t, db, id); st != "refunded" {
		t.Fatalf("status = %q", st)
	}
	// paid_at survives, updated_at moves.
	var paidAt, updatedAt time.Time
	if err := db.QueryRow(`SELECT paid_at, updated_at FROM payments WHERE id=$1`, id).Scan(&paidAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if !paidAt.Equal(refundPaidAt) {
		t.Fatalf("paid_at changed: %v", paidAt)
	}
	if !updatedAt.After(refundPaidAt) || !p.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated_at = %v (returned %v)", updatedAt, p.UpdatedAt)
	}
	if p.PaidAt == nil || !p.PaidAt.Equal(refundPaidAt) {
		t.Fatalf("returned paid_at = %v", p.PaidAt)
	}
	if r, l, n := refundTotals(t, db, id); r != 9930 || l != 9930 || n != 2 {
		t.Fatalf("totals refunds=%d ledger=%d rows=%d", r, l, n)
	}
	if b := ledgerBalance(t, db, m); b != 0 {
		t.Fatalf("balance = %d", b)
	}
	// Terminal now.
	if _, _, _, err := repo.Refund(ctx, id, 1, "again", &admin); !errors.Is(err, ledger.ErrPaymentNotRefundable) {
		t.Fatalf("refund of refunded: %v", err)
	}
}

func TestRefundPG_ExceedsNetChangesNothing(t *testing.T) {
	repo, db, m, id := refundFixture(t, "paid", "production")
	ctx := context.Background()
	if _, _, _, err := repo.Refund(ctx, id, 9931, "too much", nil); !errors.Is(err, ledger.ErrRefundExceedsNet) {
		t.Fatalf("err = %v", err)
	}
	if _, _, _, err := repo.Refund(ctx, id, 5000, "ok", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.Refund(ctx, id, 4931, "too much", nil); !errors.Is(err, ledger.ErrRefundExceedsNet) {
		t.Fatalf("err = %v", err)
	}
	if r, l, n := refundTotals(t, db, id); r != 5000 || l != 5000 || n != 1 {
		t.Fatalf("totals refunds=%d ledger=%d rows=%d", r, l, n)
	}
	if st, _ := pgStatus(t, db, id); st != "paid" {
		t.Fatalf("status = %q", st)
	}
	if b := ledgerBalance(t, db, m); b != 4930 {
		t.Fatalf("balance = %d", b)
	}
}

func TestRefundPG_NotRefundable(t *testing.T) {
	for _, c := range []struct{ status, env string }{
		{"pending", "production"}, {"expired", "production"}, {"failed", "production"},
		{"cancelled", "production"}, {"refunded", "production"}, {"paid", "sandbox"},
	} {
		t.Run(c.status+"/"+c.env, func(t *testing.T) {
			repo, db, _, id := refundFixture(t, c.status, c.env)
			if _, _, _, err := repo.Refund(context.Background(), id, 1000, "x", nil); !errors.Is(err, ledger.ErrPaymentNotRefundable) {
				t.Fatalf("err = %v", err)
			}
			if r, l, _ := refundTotals(t, db, id); r != 0 || l != 0 {
				t.Fatalf("totals refunds=%d ledger=%d", r, l)
			}
			if st, _ := pgStatus(t, db, id); st != c.status {
				t.Fatalf("status changed to %q", st)
			}
		})
	}
	repo, _, _, _ := refundFixture(t, "paid", "production")
	if _, _, _, err := repo.Refund(context.Background(), uuid.New(), 1000, "x", nil); !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("unknown payment: %v", err)
	}
}

func TestRefundPG_ZeroNetIsNotRefundable(t *testing.T) {
	repo, db, _, id := refundFixture(t, "paid", "production")
	if _, err := db.Exec(`UPDATE payments SET merchant_net_amount = 0 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.Refund(context.Background(), id, 1, "tiny", nil); !errors.Is(err, ledger.ErrPaymentNotRefundable) {
		t.Fatalf("err = %v, want ErrPaymentNotRefundable", err)
	}
	sum, err := repo.ListRefunds(context.Background(), id)
	if err != nil || sum.Refundable || sum.RemainingRefundable != 0 {
		t.Fatalf("summary = %+v err=%v", sum, err)
	}
}

// A failure after the payment_refunds insert (here: the ledger insert is
// rejected by a trigger) must roll everything back.
func TestRefundPG_LedgerFailureLeavesNoOrphanRefund(t *testing.T) {
	repo, db, m, id := refundFixture(t, "paid", "production")
	mustExec(t, db, `CREATE FUNCTION test_reject_refund_debit() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected ledger failure'; END; $$ LANGUAGE plpgsql`)
	mustExec(t, db, `CREATE TRIGGER test_reject_refund_debit BEFORE INSERT ON merchant_ledger
		FOR EACH ROW WHEN (NEW.entry_type = 'refund_debit') EXECUTE FUNCTION test_reject_refund_debit()`)

	for _, amount := range []int64{3000, 9930} { // partial and would-be-full
		_, _, _, err := repo.Refund(context.Background(), id, amount, "boom", nil)
		if err == nil || !strings.Contains(err.Error(), "failed to debit merchant ledger") {
			t.Fatalf("amount %d: err = %v", amount, err)
		}
	}
	if n := countRows(t, db, `SELECT count(*) FROM payment_refunds WHERE payment_id = $1`, id); n != 0 {
		t.Fatalf("orphan payment_refunds rows = %d", n)
	}
	if st, _ := pgStatus(t, db, id); st != "paid" {
		t.Fatalf("status = %q", st)
	}
	var paidAt time.Time
	if err := db.QueryRow(`SELECT paid_at FROM payments WHERE id = $1`, id).Scan(&paidAt); err != nil || !paidAt.Equal(refundPaidAt) {
		t.Fatalf("paid_at = %v err=%v", paidAt, err)
	}
	if b := ledgerBalance(t, db, m); b != 9930 {
		t.Fatalf("balance = %d", b)
	}
}

func TestRefundPG_ListRefunds(t *testing.T) {
	repo, db, _, id := refundFixture(t, "paid", "production")
	ctx := context.Background()

	sum, err := repo.ListRefunds(ctx, id)
	if err != nil || len(sum.Items) != 0 || sum.Items == nil || sum.MerchantNetAmount != 9930 ||
		sum.RefundedAmount != 0 || sum.RemainingRefundable != 9930 || !sum.Refundable {
		t.Fatalf("empty: %+v err=%v", sum, err)
	}
	admin := uuid.New()
	if _, _, _, err := repo.Refund(ctx, id, 3000, "first", &admin); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, _, err := repo.Refund(ctx, id, 1000, "second", &admin); err != nil {
		t.Fatal(err)
	}
	sum, err = repo.ListRefunds(ctx, id)
	if err != nil || len(sum.Items) != 2 || sum.Items[0].Reason != "second" || sum.Items[1].Reason != "first" ||
		sum.RefundedAmount != 4000 || sum.RemainingRefundable != 5930 || !sum.Refundable {
		t.Fatalf("after two refunds: %+v err=%v", sum, err)
	}
	b, _ := json.Marshal(sum)
	if strings.Contains(string(b), "created_by") || strings.Contains(string(b), admin.String()) {
		t.Fatalf("admin id leaked: %s", b)
	}
	if _, _, _, err := repo.Refund(ctx, id, 5930, "rest", &admin); err != nil {
		t.Fatal(err)
	}
	sum, _ = repo.ListRefunds(ctx, id)
	if sum.Refundable || sum.RemainingRefundable != 0 || sum.RefundedAmount != 9930 {
		t.Fatalf("fully refunded: %+v", sum)
	}
	if _, err := repo.ListRefunds(ctx, uuid.New()); !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("unknown payment: %v", err)
	}
	// Not refundable in other states, even with a net.
	for _, c := range []struct{ status, env string }{{"pending", "production"}, {"paid", "sandbox"}} {
		r2, _, _, id2 := refundFixture(t, c.status, c.env)
		sum, err := r2.ListRefunds(ctx, id2)
		if err != nil || sum.Refundable {
			t.Fatalf("%v: %+v err=%v", c, sum, err)
		}
	}
	_ = db
}

func TestPaymentPG_StatusBreakdownHasRefundedBucket(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPaymentRepository(db)
	m := insertConstraintMerchant(t, db)
	for _, st := range []string{"paid", "paid", "pending", "failed", "expired", "cancelled", "refunded"} {
		insertLedgerPayment(t, db, m, 10000, 0, st, "production")
	}
	b, err := repo.StatusBreakdownFiltered(context.Background(), &m, "production")
	if err != nil {
		t.Fatal(err)
	}
	if b.Refunded != 1 || b.Paid != 2 || b.PaidAmount != 20000 {
		t.Fatalf("breakdown = %+v (fully refunded payments are not in paid_amount)", b)
	}
	if sum := b.Paid + b.Pending + b.Failed + b.Expired + b.Cancelled + b.Refunded; sum != b.Total {
		t.Fatalf("buckets sum to %d, total %d", sum, b.Total)
	}
	days, err := repo.DailyStatsFiltered(context.Background(), 2, &m, "production")
	if err != nil || len(days) != 1 || days[0].Refunded != 1 || days[0].Total != 7 {
		t.Fatalf("daily = %+v err=%v", days, err)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestRefundPG_Validation(t *testing.T) {
	repo, db, _, id := refundFixture(t, "paid", "production")
	ctx := context.Background()
	long := make([]rune, 501)
	for i := range long {
		long[i] = 'a'
	}
	for name, c := range map[string]struct {
		amount int64
		reason string
		want   error
	}{
		"zero":         {0, "r", ledger.ErrInvalidAmount},
		"negative":     {-5, "r", ledger.ErrInvalidAmount},
		"empty reason": {1000, "", ledger.ErrReasonRequired},
		"blank reason": {1000, "  \t ", ledger.ErrReasonRequired},
		"long reason":  {1000, string(long), ledger.ErrTextTooLong},
	} {
		if _, _, _, err := repo.Refund(ctx, id, c.amount, c.reason, nil); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if r, _, _ := refundTotals(t, db, id); r != 0 {
		t.Fatalf("refunds = %d", r)
	}
}

func TestRefundPG_ConcurrentNeverOverRefunds(t *testing.T) {
	repo, db, m, id := refundFixture(t, "paid", "production")
	// Tiny pool plus timeout: a loser must free its connection (rollback)
	// without needing another one, or the pool starves and the test times out.
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const workers = 20
	errs := make([]error, workers)
	fulls := make([]bool, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, fulls[i], _, errs[i] = repo.Refund(ctx, id, 1000, "load", nil)
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ledger.ErrRefundExceedsNet):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 9 {
		t.Fatalf("successful refunds = %d, want 9 (9930/1000)", ok)
	}
	if r, l, n := refundTotals(t, db, id); r != 9000 || l != 9000 || n != 9 {
		t.Fatalf("totals refunds=%d ledger=%d rows=%d", r, l, n)
	}
	if b := ledgerBalance(t, db, m); b != 930 {
		t.Fatalf("balance = %d", b)
	}
	for i := range fulls {
		if fulls[i] {
			t.Fatal("no refund of 1000 can complete 9930")
		}
	}
	if st, _ := pgStatus(t, db, id); st != "paid" {
		t.Fatalf("status = %q", st)
	}
}

func TestRefundPG_ConcurrentFullRefundsExactlyOneWins(t *testing.T) {
	repo, db, _, id := refundFixture(t, "paid", "production")
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, _, errs[i] = repo.Refund(ctx, id, 9930, "full", nil)
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ledger.ErrPaymentNotRefundable) && !errors.Is(err, ledger.ErrRefundExceedsNet) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("successes = %d, want 1 (%v)", ok, errs)
	}
	if r, l, n := refundTotals(t, db, id); r != 9930 || l != 9930 || n != 1 {
		t.Fatalf("totals refunds=%d ledger=%d rows=%d", r, l, n)
	}
	if st, _ := pgStatus(t, db, id); st != "refunded" {
		t.Fatalf("status = %q", st)
	}
}

func TestLedgerPG_Adjust(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewLedgerRepository(db)
	ctx := context.Background()
	m := insertConstraintMerchant(t, db)
	admin := uuid.New()

	e, err := repo.Adjust(ctx, m, 5000, "  goodwill ", &admin)
	if err != nil || e.Amount != 5000 || e.EntryType != "adjustment" || e.Note != "goodwill" || e.CreatedBy == nil || *e.CreatedBy != admin || e.CreatedAt.IsZero() {
		t.Fatalf("credit: %+v err=%v", e, err)
	}
	if _, err := repo.Adjust(ctx, m, -7000, "chargeback", &admin); err != nil {
		t.Fatal(err)
	}
	if b := ledgerBalance(t, db, m); b != -2000 {
		t.Fatalf("balance = %d", b)
	}
	var by uuid.UUID
	var note string
	if err := db.QueryRow(`SELECT created_by, note FROM merchant_ledger WHERE id=$1`, e.ID).Scan(&by, &note); err != nil || by != admin || note != "goodwill" {
		t.Fatalf("stored by=%v note=%q err=%v", by, note, err)
	}
	long := make([]rune, 501)
	for i := range long {
		long[i] = 'a'
	}
	for name, c := range map[string]struct {
		amount int64
		reason string
		want   error
	}{
		"zero":         {0, "r", ledger.ErrInvalidAdjustment},
		"too big":      {ledger.MaxAdjustment + 1, "r", ledger.ErrInvalidAdjustment},
		"too negative": {-ledger.MaxAdjustment - 1, "r", ledger.ErrInvalidAdjustment},
		"no reason":    {100, "   ", ledger.ErrReasonRequired},
		"long reason":  {100, string(long), ledger.ErrTextTooLong},
	} {
		if _, err := repo.Adjust(ctx, m, c.amount, c.reason, nil); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := repo.Adjust(ctx, m, ledger.MaxAdjustment, "max", nil); err != nil {
		t.Fatalf("max boundary: %v", err)
	}
	if _, err := repo.Adjust(ctx, uuid.New(), 100, "ghost", nil); !errors.Is(err, merchant.ErrMerchantNotFound) {
		t.Fatalf("unknown merchant: %v", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM merchant_ledger WHERE merchant_id=$1`, m); n != 3 {
		t.Fatalf("rows = %d", n)
	}
}
