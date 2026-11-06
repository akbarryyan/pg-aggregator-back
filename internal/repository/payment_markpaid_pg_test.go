package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

func markPaidFixture(t *testing.T, status string, amount, fee int64, env string) (*PaymentRepository, *sql.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	repo, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	id := insertLedgerPayment(t, db, m, amount, fee, status, env)
	return repo, db, m, id
}

func ledgerCount(t *testing.T, db *sql.DB, paymentID uuid.UUID) (n int, sum int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT count(*), COALESCE(sum(amount),0) FROM merchant_ledger WHERE payment_id = $1 AND entry_type = 'payment_credit'`, paymentID).Scan(&n, &sum); err != nil {
		t.Fatal(err)
	}
	return
}

func storedNet(t *testing.T, db *sql.DB, id uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT merchant_net_amount FROM payments WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMarkPaidPG_PendingToPaidCredits(t *testing.T) {
	repo, db, m, id := markPaidFixture(t, "pending", 10000, 0, "production")
	paidAt := time.Now()
	prev, err := repo.MarkPaid(context.Background(), id, &paidAt, 9930)
	if err != nil || prev != "pending" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
	if st, hasPaid := pgStatus(t, db, id); st != "paid" || !hasPaid {
		t.Fatalf("status=%q paid_at=%v", st, hasPaid)
	}
	if got := storedNet(t, db, id); got != 9930 {
		t.Fatalf("stored net = %d", got)
	}
	if n, sum := ledgerCount(t, db, id); n != 1 || sum != 9930 {
		t.Fatalf("ledger n=%d sum=%d", n, sum)
	}
	var lm uuid.UUID
	if err := db.QueryRow(`SELECT merchant_id FROM merchant_ledger WHERE payment_id = $1`, id).Scan(&lm); err != nil || lm != m {
		t.Fatalf("ledger merchant = %v (%v), want %v", lm, err, m)
	}
	// Read back through GetByID: guards against swapped Scan targets.
	got, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.MerchantNetAmount != 9930 || got.Amount != 10000 || got.FeeAmount != 0 {
		t.Fatalf("GetByID net=%d amount=%d fee=%d", got.MerchantNetAmount, got.Amount, got.FeeAmount)
	}
}

func TestMarkPaidPG_LateExpiredCredits(t *testing.T) {
	repo, db, _, id := markPaidFixture(t, "expired", 10000, 70, "production")
	paidAt := time.Now()
	prev, err := repo.MarkPaid(context.Background(), id, &paidAt, 10000)
	if err != nil || prev != "expired" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
	if n, sum := ledgerCount(t, db, id); n != 1 || sum != 10000 {
		t.Fatalf("ledger n=%d sum=%d", n, sum)
	}
}

func TestMarkPaidPG_SandboxNoLedger(t *testing.T) {
	repo, db, _, id := markPaidFixture(t, "pending", 10000, 0, "sandbox")
	paidAt := time.Now()
	if _, err := repo.MarkPaid(context.Background(), id, &paidAt, 9930); err != nil {
		t.Fatal(err)
	}
	if st, _ := pgStatus(t, db, id); st != "paid" {
		t.Fatalf("status = %q", st)
	}
	if got := storedNet(t, db, id); got != 9930 {
		t.Fatalf("stored net = %d", got)
	}
	if n, _ := ledgerCount(t, db, id); n != 0 {
		t.Fatalf("sandbox must not be credited, got %d rows", n)
	}
}

func TestMarkPaidPG_SecondCallIsTerminal(t *testing.T) {
	repo, db, _, id := markPaidFixture(t, "pending", 10000, 0, "production")
	paidAt := time.Now()
	if _, err := repo.MarkPaid(context.Background(), id, &paidAt, 9930); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkPaid(context.Background(), id, &paidAt, 9930); !errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
		t.Fatalf("err = %v", err)
	}
	if n, _ := ledgerCount(t, db, id); n != 1 {
		t.Fatalf("ledger rows = %d", n)
	}
}

func TestMarkPaidPG_ConcurrentExactlyOneWins(t *testing.T) {
	repo, db, m, id := markPaidFixture(t, "pending", 10000, 0, "production")
	// Tiny pool: every loser must release its connection before classifying
	// the failure, or the losers starve each other. A hang fails via timeout.
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const workers = 20
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			paidAt := time.Now()
			_, errs[i] = repo.MarkPaid(ctx, id, &paidAt, 9930)
		}(i)
	}
	close(start)
	wg.Wait()
	ok, terminal := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, payment.ErrPaymentAlreadyTerminal):
			terminal++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || terminal != workers-1 {
		t.Fatalf("ok=%d terminal=%d", ok, terminal)
	}
	if n, sum := ledgerCount(t, db, id); n != 1 || sum != 9930 {
		t.Fatalf("ledger n=%d sum=%d", n, sum)
	}
	var bal int64
	if err := db.QueryRow(`SELECT COALESCE(sum(amount),0) FROM merchant_ledger WHERE merchant_id = $1`, m).Scan(&bal); err != nil || bal != 9930 {
		t.Fatalf("balance = %d (%v)", bal, err)
	}
}

func TestMarkPaidPG_ZeroNetPaidWithoutLedger(t *testing.T) {
	repo, db, _, id := markPaidFixture(t, "pending", 1, 0, "production")
	paidAt := time.Now()
	if _, err := repo.MarkPaid(context.Background(), id, &paidAt, 0); err != nil {
		t.Fatal(err)
	}
	if st, _ := pgStatus(t, db, id); st != "paid" {
		t.Fatalf("status = %q", st)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM merchant_ledger WHERE payment_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("ledger rows = %d (%v)", n, err)
	}
}

func TestMarkPaidPG_LedgerFailureRollsBackStatus(t *testing.T) {
	repo, db, m, id := markPaidFixture(t, "pending", 10000, 0, "production")
	// A pre-existing credit makes the INSERT violate the one-credit unique index.
	if err := insertLedgerEntry(db, m, "payment_credit", 9930, &id, nil, nil); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Now()
	_, err := repo.MarkPaid(context.Background(), id, &paidAt, 9930)
	if err == nil || !strings.Contains(err.Error(), "failed to credit merchant ledger") {
		t.Fatalf("err = %v, want failed to credit merchant ledger", err)
	}
	if n, _ := ledgerCount(t, db, id); n != 1 {
		t.Fatalf("ledger rows = %d, want exactly the pre-inserted one", n)
	}
	if st, hasPaid := pgStatus(t, db, id); st != "pending" || hasPaid {
		t.Fatalf("status=%q paid_at=%v, want untouched pending", st, hasPaid)
	}
	if got := storedNet(t, db, id); got != 0 {
		t.Fatalf("net = %d, want 0 (rolled back)", got)
	}
}
