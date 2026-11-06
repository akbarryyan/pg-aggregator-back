package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

const testMinPayout = 50000

// payoutMerchant inserts a merchant with a payout account and a production
// credit of `credit` rupiah in its ledger.
func payoutMerchant(t *testing.T, db *sql.DB, credit int64) uuid.UUID {
	t.Helper()
	m := insertConstraintMerchant(t, db)
	if _, err := db.Exec(
		`UPDATE merchants SET payout_bank_name='BCA', payout_account_number='1234567890', payout_account_holder='Budi' WHERE id=$1`, m); err != nil {
		t.Fatal(err)
	}
	if credit > 0 {
		p := insertLedgerPayment(t, db, m, credit, 0, "paid", "production")
		if err := insertLedgerEntry(db, m, "payment_credit", credit, &p, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func ledgerBalance(t *testing.T, db *sql.DB, m uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM merchant_ledger WHERE merchant_id=$1`, m).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func countRows(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPayoutPG_RequestSuccessAndSnapshot(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	ctx := context.Background()
	m := payoutMerchant(t, db, 100000)
	user := uuid.New()

	p, err := repo.Request(ctx, m, 60000, testMinPayout, &user)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if p.Status != ledger.PayoutRequested || p.Amount != 60000 || p.MerchantID != m ||
		p.BankName != "BCA" || p.AccountNumber != "1234567890" || p.AccountHolder != "Budi" ||
		p.RequestedBy == nil || *p.RequestedBy != user {
		t.Fatalf("unexpected payout %+v", p)
	}
	if got := ledgerBalance(t, db, m); got != 40000 {
		t.Fatalf("balance = %d, want 40000", got)
	}
	var lm uuid.UUID
	var amt int64
	var typ string
	if err := db.QueryRow(`SELECT merchant_id, amount, entry_type FROM merchant_ledger WHERE payout_id=$1`, p.ID).Scan(&lm, &amt, &typ); err != nil {
		t.Fatal(err)
	}
	if lm != m || amt != -60000 || typ != "payout_debit" {
		t.Fatalf("ledger row = %s %d %s", lm, amt, typ)
	}

	// Changing the merchant's account afterwards must not touch the snapshot.
	if _, err := db.Exec(`UPDATE merchants SET payout_bank_name='Mandiri', payout_account_number='999999', payout_account_holder='Lain' WHERE id=$1`, m); err != nil {
		t.Fatal(err)
	}
	items, _, err := repo.ListByMerchant(ctx, m, 10, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %v %d", err, len(items))
	}
	if items[0].BankName != "BCA" || items[0].AccountNumber != "1234567890" || items[0].AccountHolder != "Budi" {
		t.Fatalf("snapshot changed: %+v", items[0])
	}
}

func TestPayoutPG_RequestRejections(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	ctx := context.Background()

	t.Run("below minimum", func(t *testing.T) {
		m := payoutMerchant(t, db, 100000)
		if _, err := repo.Request(ctx, m, testMinPayout-1, testMinPayout, nil); !errors.Is(err, ledger.ErrBelowMinimumPayout) {
			t.Fatalf("got %v", err)
		}
		if _, err := repo.Request(ctx, m, 0, testMinPayout, nil); !errors.Is(err, ledger.ErrBelowMinimumPayout) {
			t.Fatalf("zero: got %v", err)
		}
		if got := countRows(t, db, `SELECT COUNT(*) FROM payouts WHERE merchant_id=$1`, m); got != 0 {
			t.Fatalf("payouts = %d", got)
		}
	})
	t.Run("exceeds balance", func(t *testing.T) {
		m := payoutMerchant(t, db, 100000)
		if _, err := repo.Request(ctx, m, 100001, testMinPayout, nil); !errors.Is(err, ledger.ErrInsufficientBalance) {
			t.Fatalf("got %v", err)
		}
		if _, err := repo.Request(ctx, m, 100000, testMinPayout, nil); err != nil {
			t.Fatalf("exact balance must work: %v", err)
		}
	})
	t.Run("account missing", func(t *testing.T) {
		m := payoutMerchant(t, db, 100000)
		if _, err := db.Exec(`UPDATE merchants SET payout_bank_name=NULL, payout_account_number=NULL, payout_account_holder=NULL WHERE id=$1`, m); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Request(ctx, m, 60000, testMinPayout, nil); !errors.Is(err, ledger.ErrPayoutAccountMissing) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("open payout exists", func(t *testing.T) {
		m := payoutMerchant(t, db, 200000)
		if _, err := repo.Request(ctx, m, 60000, testMinPayout, nil); err != nil {
			t.Fatal(err)
		}
		// Balance (140000) would cover a second one: only the open-payout rule refuses.
		if _, err := repo.Request(ctx, m, 60000, testMinPayout, nil); !errors.Is(err, ledger.ErrOpenPayoutExists) {
			t.Fatalf("got %v", err)
		}
		if got := countRows(t, db, `SELECT COUNT(*) FROM payouts WHERE merchant_id=$1`, m); got != 1 {
			t.Fatalf("payouts = %d", got)
		}
	})
	t.Run("negative balance", func(t *testing.T) {
		m := payoutMerchant(t, db, 100000)
		if err := insertLedgerEntry(db, m, "adjustment", -150000, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Request(ctx, m, testMinPayout, testMinPayout, nil); !errors.Is(err, ledger.ErrInsufficientBalance) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unknown merchant", func(t *testing.T) {
		if _, err := repo.Request(ctx, uuid.New(), 60000, testMinPayout, nil); !errors.Is(err, merchant.ErrMerchantNotFound) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestPayoutPG_RequestConcurrentExactlyOneWins(t *testing.T) {
	_, db := newPGRepo(t)
	db.SetMaxOpenConns(8)
	repo := NewPayoutRepository(db)
	m := payoutMerchant(t, db, 100000)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const n = 20
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = repo.Request(ctx, m, 60000, testMinPayout, nil)
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ledger.ErrOpenPayoutExists), errors.Is(err, ledger.ErrInsufficientBalance):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1", wins)
	}
	if got := ledgerBalance(t, db, m); got != 40000 {
		t.Fatalf("balance = %d, want 40000", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM merchant_ledger WHERE merchant_id=$1 AND entry_type='payout_debit'`, m); got != 1 {
		t.Fatalf("debits = %d", got)
	}
}

// The merchant row lock is what serializes Request against any other writer
// that debits the same merchant (e.g. a refund): while another transaction
// holds the lock, Request must wait, and then judge the balance it left behind.
func TestPayoutPG_RequestWaitsForMerchantLock(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	m := payoutMerchant(t, db, 100000)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM merchants WHERE id=$1 FOR UPDATE`, m); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := repo.Request(ctx, m, 60000, testMinPayout, nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Request did not wait for the merchant lock (returned %v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	// The lock holder drains the balance, then releases.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount) VALUES (gen_random_uuid(), $1, 'adjustment', -90000)`, m); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ledger.ErrInsufficientBalance) {
		t.Fatalf("Request after the lock holder committed = %v, want ErrInsufficientBalance", err)
	}
}

func TestPayoutPG_MarkPaid(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	ctx := context.Background()
	m := payoutMerchant(t, db, 100000)
	admin := uuid.New()
	p, err := repo.Request(ctx, m, 60000, testMinPayout, nil)
	if err != nil {
		t.Fatal(err)
	}
	rowsBefore := countRows(t, db, `SELECT COUNT(*) FROM merchant_ledger WHERE merchant_id=$1`, m)

	if _, err := repo.MarkPaid(ctx, p.ID, "  ", &admin); !errors.Is(err, ledger.ErrReasonRequired) {
		t.Fatalf("blank reference: %v", err)
	}
	long := make([]rune, 201)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := repo.MarkPaid(ctx, p.ID, string(long), &admin); !errors.Is(err, ledger.ErrTextTooLong) {
		t.Fatalf("long reference: %v", err)
	}

	got, err := repo.MarkPaid(ctx, p.ID, "  TRX-1  ", &admin)
	if err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if got.Status != ledger.PayoutPaid || got.Reference == nil || *got.Reference != "TRX-1" ||
		got.ProcessedBy == nil || *got.ProcessedBy != admin || got.ProcessedAt == nil {
		t.Fatalf("unexpected %+v", got)
	}
	if after := countRows(t, db, `SELECT COUNT(*) FROM merchant_ledger WHERE merchant_id=$1`, m); after != rowsBefore {
		t.Fatalf("pay wrote ledger rows: %d -> %d", rowsBefore, after)
	}
	if _, err := repo.MarkPaid(ctx, p.ID, "TRX-2", &admin); !errors.Is(err, ledger.ErrPayoutAlreadyProcessed) {
		t.Fatalf("second pay: %v", err)
	}
	if _, err := repo.Reject(ctx, p.ID, "late", &admin); !errors.Is(err, ledger.ErrPayoutAlreadyProcessed) {
		t.Fatalf("reject after pay: %v", err)
	}
	if _, err := repo.MarkPaid(ctx, uuid.New(), "TRX", &admin); !errors.Is(err, ledger.ErrPayoutNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if got := ledgerBalance(t, db, m); got != 40000 {
		t.Fatalf("balance = %d, want 40000", got)
	}
}

func TestPayoutPG_RejectRestoresBalanceOnce(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	ctx := context.Background()
	m := payoutMerchant(t, db, 100000)
	admin := uuid.New()
	p, err := repo.Request(ctx, m, 60000, testMinPayout, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Reject(ctx, p.ID, "", &admin); !errors.Is(err, ledger.ErrReasonRequired) {
		t.Fatalf("blank reason: %v", err)
	}
	if got := ledgerBalance(t, db, m); got != 40000 {
		t.Fatalf("balance after refused reject = %d", got)
	}

	got, err := repo.Reject(ctx, p.ID, " wrong account ", &admin)
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got.Status != ledger.PayoutRejected || got.RejectReason == nil || *got.RejectReason != "wrong account" {
		t.Fatalf("unexpected %+v", got)
	}
	if bal := ledgerBalance(t, db, m); bal != 100000 {
		t.Fatalf("balance = %d, want 100000", bal)
	}
	var lm uuid.UUID
	var by uuid.NullUUID
	if err := db.QueryRow(`SELECT merchant_id, created_by FROM merchant_ledger WHERE payout_id=$1 AND entry_type='payout_reversal'`, p.ID).Scan(&lm, &by); err != nil {
		t.Fatal(err)
	}
	if lm != got.MerchantID || !by.Valid || by.UUID != admin {
		t.Fatalf("reversal row merchant=%s by=%v", lm, by)
	}
	if _, err := repo.Reject(ctx, p.ID, "again", &admin); !errors.Is(err, ledger.ErrPayoutAlreadyProcessed) {
		t.Fatalf("second reject: %v", err)
	}
	if bal := ledgerBalance(t, db, m); bal != 100000 {
		t.Fatalf("balance after second reject = %d, want 100000", bal)
	}
	// The merchant can request again once the payout is rejected.
	if _, err := repo.Request(ctx, m, 60000, testMinPayout, nil); err != nil {
		t.Fatalf("re-request after reject: %v", err)
	}
}

// Pay and reject race for the same payout: exactly one wins and the ledger
// matches the winner. A 2-connection pool also proves losers release their
// connection before classifying the failure.
func TestPayoutPG_PayAndRejectRace(t *testing.T) {
	for round := 0; round < 3; round++ {
		_, db := newPGRepo(t)
		db.SetMaxOpenConns(2)
		repo := NewPayoutRepository(db)
		m := payoutMerchant(t, db, 100000)
		p, err := repo.Request(context.Background(), m, 60000, testMinPayout, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)

		const n = 10
		type res struct {
			paid bool
			err  error
		}
		results := make([]res, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if i%2 == 0 {
					_, err := repo.MarkPaid(ctx, p.ID, "TRX", nil)
					results[i] = res{true, err}
				} else {
					_, err := repo.Reject(ctx, p.ID, "no", nil)
					results[i] = res{false, err}
				}
			}(i)
		}
		wg.Wait()
		cancel()

		wins, paidWins := 0, 0
		for _, r := range results {
			switch {
			case r.err == nil:
				wins++
				if r.paid {
					paidWins++
				}
			case errors.Is(r.err, ledger.ErrPayoutAlreadyProcessed):
			default:
				t.Fatalf("unexpected error: %v", r.err)
			}
		}
		if wins != 1 {
			t.Fatalf("wins = %d, want exactly 1", wins)
		}
		want := int64(100000)
		if paidWins == 1 {
			want = 40000
		}
		if got := ledgerBalance(t, db, m); got != want {
			t.Fatalf("balance = %d, want %d (paid won: %v)", got, want, paidWins == 1)
		}
	}
}

func TestPayoutPG_ListsAreScopedCappedAndNeverNil(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	ctx := context.Background()
	a := payoutMerchant(t, db, 100000)
	b := payoutMerchant(t, db, 100000)

	items, total, err := repo.ListByMerchant(ctx, a, 20, 0)
	if err != nil || items == nil || len(items) != 0 || total != 0 {
		t.Fatalf("empty ListByMerchant: %v %v %d", items, err, total)
	}
	adminItems, _, err := repo.ListAdmin(ctx, "", 20, 0)
	if err != nil || adminItems == nil {
		t.Fatalf("empty ListAdmin must be non-nil: %v %v", adminItems, err)
	}

	pa, err := repo.Request(ctx, a, 60000, testMinPayout, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Request(ctx, b, 50000, testMinPayout, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkPaid(ctx, pa.ID, "TRX", nil); err != nil {
		t.Fatal(err)
	}

	items, total, err = repo.ListByMerchant(ctx, a, 20, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != pa.ID {
		t.Fatalf("ListByMerchant(a) = %v total %d err %v", items, total, err)
	}
	all, total, err := repo.ListAdmin(ctx, "", 20, 0)
	if err != nil || total != 2 || len(all) != 2 || all[0].MerchantName == "" {
		t.Fatalf("ListAdmin all = %d total %d err %v", len(all), total, err)
	}
	req, total, err := repo.ListAdmin(ctx, ledger.PayoutRequested, 20, 0)
	if err != nil || total != 1 || len(req) != 1 || req[0].MerchantID != b {
		t.Fatalf("ListAdmin requested = %v total %d err %v", req, total, err)
	}
	none, _, err := repo.ListAdmin(ctx, ledger.PayoutRejected, 20, 0)
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("ListAdmin rejected: %v %v", none, err)
	}

	// Limit cap: 105 paid payouts, ask for 1000, get 100.
	bulk := payoutMerchant(t, db, 0)
	if _, err := db.Exec(`
		INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder, processed_at, reference)
		SELECT gen_random_uuid(), $1, 1000, 'paid', 'BCA', '12345', 'H', now(), 'r' FROM generate_series(1, 105)`, bulk); err != nil {
		t.Fatal(err)
	}
	capped, total, err := repo.ListByMerchant(ctx, bulk, 1000, 0)
	if err != nil || len(capped) != 100 || total != 105 {
		t.Fatalf("capped ListByMerchant = %d total %d err %v", len(capped), total, err)
	}
	cappedAdmin, _, err := repo.ListAdmin(ctx, ledger.PayoutPaid, 1000, 0)
	if err != nil || len(cappedAdmin) != 100 {
		t.Fatalf("capped ListAdmin = %d err %v", len(cappedAdmin), err)
	}
	def, _, err := repo.ListByMerchant(ctx, bulk, 0, 0)
	if err != nil || len(def) != 20 {
		t.Fatalf("default limit = %d err %v", len(def), err)
	}
}

func TestPayoutPG_HasOpenAndPendingSummary(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	ctx := context.Background()
	m := payoutMerchant(t, db, 100000)

	if open, err := repo.HasOpen(ctx, m); err != nil || open {
		t.Fatalf("HasOpen before: %v %v", open, err)
	}
	if n, latest, err := repo.PendingSummary(ctx); err != nil || n != 0 || latest != nil {
		t.Fatalf("PendingSummary before: %d %v %v", n, latest, err)
	}
	p, err := repo.Request(ctx, m, 60000, testMinPayout, nil)
	if err != nil {
		t.Fatal(err)
	}
	if open, err := repo.HasOpen(ctx, m); err != nil || !open {
		t.Fatalf("HasOpen after: %v %v", open, err)
	}
	if n, latest, err := repo.PendingSummary(ctx); err != nil || n != 1 || latest == nil {
		t.Fatalf("PendingSummary after: %d %v %v", n, latest, err)
	}
	if _, err := repo.MarkPaid(ctx, p.ID, "T", nil); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := repo.PendingSummary(ctx); n != 0 {
		t.Fatalf("pending after pay = %d", n)
	}
}

func TestLedgerPG_ListCapAndEmptyNonNil(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewLedgerRepository(db)
	ctx := context.Background()
	m := insertConstraintMerchant(t, db)

	entries, total, err := repo.List(ctx, m, 20, 0)
	if err != nil || entries == nil || len(entries) != 0 || total != 0 {
		t.Fatalf("empty List must be a non-nil empty slice: %#v total %d err %v", entries, total, err)
	}
	if _, err := db.Exec(`
		INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount)
		SELECT gen_random_uuid(), $1, 'adjustment', 1 FROM generate_series(1, 105)`, m); err != nil {
		t.Fatal(err)
	}
	entries, total, err = repo.List(ctx, m, 1000, 0)
	if err != nil || len(entries) != 100 || total != 105 {
		t.Fatalf("capped List = %d total %d err %v", len(entries), total, err)
	}
	entries, _, err = repo.List(ctx, m, 0, -5)
	if err != nil || len(entries) != 20 {
		t.Fatalf("default List = %d err %v", len(entries), err)
	}
}

func TestLedgerPG_ListBalances(t *testing.T) {
	_, db := newPGRepo(t)
	ledgerRepo := NewLedgerRepository(db)
	payouts := NewPayoutRepository(db)
	ctx := context.Background()

	rich := payoutMerchant(t, db, 300000)
	poor := payoutMerchant(t, db, 60000)
	neg := payoutMerchant(t, db, 10000)
	if err := insertLedgerEntry(db, neg, "adjustment", -30000, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := payouts.Request(ctx, poor, 50000, testMinPayout, nil); err != nil {
		t.Fatal(err)
	}

	items, total, err := ledgerRepo.ListBalances(ctx, false, 20, 0)
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("ListBalances = %d total %d err %v", len(items), total, err)
	}
	if items[0].MerchantID != rich || items[0].Balance != 300000 || items[0].OpenPayoutID != nil {
		t.Fatalf("first = %+v", items[0])
	}
	if items[1].MerchantID != poor || items[1].Balance != 10000 || items[1].OpenPayoutID == nil || items[1].OpenPayoutAmount != 50000 {
		t.Fatalf("second = %+v", items[1])
	}
	if items[2].MerchantID != neg || items[2].Balance != -20000 {
		t.Fatalf("third = %+v", items[2])
	}

	onlyNeg, total, err := ledgerRepo.ListBalances(ctx, true, 20, 0)
	if err != nil || total != 1 || len(onlyNeg) != 1 || onlyNeg[0].MerchantID != neg {
		t.Fatalf("negative filter = %v total %d err %v", onlyNeg, total, err)
	}
}

func TestMerchantPG_PayoutAccount(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantRepository(db)
	ctx := context.Background()
	m := insertConstraintMerchant(t, db)

	if acc, err := repo.GetPayoutAccount(ctx, m); err != nil || acc != nil {
		t.Fatalf("unset account: %v %v", acc, err)
	}
	if err := repo.SetPayoutAccount(ctx, m, "BCA", "1234567", "Budi"); err != nil {
		t.Fatal(err)
	}
	acc, err := repo.GetPayoutAccount(ctx, m)
	if err != nil || acc == nil || *acc != (merchant.PayoutAccount{BankName: "BCA", AccountNumber: "1234567", AccountHolder: "Budi"}) {
		t.Fatalf("account = %+v err %v", acc, err)
	}
	if err := repo.SetPayoutAccount(ctx, uuid.New(), "BCA", "1234567", "Budi"); !errors.Is(err, merchant.ErrMerchantNotFound) {
		t.Fatalf("unknown merchant: %v", err)
	}
	if _, err := repo.GetPayoutAccount(ctx, uuid.New()); !errors.Is(err, merchant.ErrMerchantNotFound) {
		t.Fatalf("unknown merchant get: %v", err)
	}
}

func TestPayoutPG_RequestLedgerFailureLeavesNoPayout(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	m := payoutMerchant(t, db, 100000)
	mustExec(t, db, `CREATE FUNCTION test_reject_payout_debit() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected ledger failure'; END; $$ LANGUAGE plpgsql`)
	mustExec(t, db, `CREATE TRIGGER test_reject_payout_debit BEFORE INSERT ON merchant_ledger
		FOR EACH ROW WHEN (NEW.entry_type = 'payout_debit') EXECUTE FUNCTION test_reject_payout_debit()`)

	_, err := repo.Request(context.Background(), m, 60000, testMinPayout, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to debit merchant ledger") {
		t.Fatalf("err = %v", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM payouts WHERE merchant_id = $1`, m); n != 0 {
		t.Fatalf("orphan payouts rows = %d", n)
	}
	if b := ledgerBalance(t, db, m); b != 100000 {
		t.Fatalf("balance = %d", b)
	}
}

func TestPayoutPG_RejectReasonRuneCap(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewPayoutRepository(db)
	ctx := context.Background()
	m := payoutMerchant(t, db, 100000)
	p, err := repo.Request(ctx, m, 60000, testMinPayout, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Reject(ctx, p.ID, strings.Repeat("é", 501), nil); !errors.Is(err, ledger.ErrTextTooLong) {
		t.Fatalf("501 runes: %v", err)
	}
	if got := ledgerBalance(t, db, m); got != 40000 {
		t.Fatalf("balance after refused reject = %d", got)
	}
	// Exactly 500 multi-byte runes (1000 bytes) is accepted.
	if got, err := repo.Reject(ctx, p.ID, strings.Repeat("é", 500), nil); err != nil || got.Status != ledger.PayoutRejected {
		t.Fatalf("500 runes: %v %+v", err, got)
	}
}

func TestIsOpenPayoutViolation_OnlyForThatConstraint(t *testing.T) {
	mk := func(code, constraint string) error {
		return fmt.Errorf("wrapped: %w", &pq.Error{Code: pq.ErrorCode(code), Constraint: constraint})
	}
	if !isOpenPayoutViolation(mk("23505", "uq_payouts_one_open_per_merchant")) {
		t.Error("open-payout index violation must match")
	}
	if isOpenPayoutViolation(mk("23505", "payouts_pkey")) {
		t.Error("another unique violation must not be disguised as ErrOpenPayoutExists")
	}
	if isOpenPayoutViolation(mk("23503", "uq_payouts_one_open_per_merchant")) {
		t.Error("only 23505 matches")
	}
	if isOpenPayoutViolation(errors.New("plain")) || isOpenPayoutViolation(nil) {
		t.Error("non-pq errors must not match")
	}
}
