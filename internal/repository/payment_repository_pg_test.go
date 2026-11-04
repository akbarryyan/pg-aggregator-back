package repository

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// newPGRepo gives a repository on a real PostgreSQL, isolated in a throwaway
// schema built from the project's migrations. Opt-in: the sqlmock tests
// cannot prove locking/RETURNING semantics, so these run only when
// TEST_DATABASE_URL is set.
func newPGRepo(t *testing.T) (*PaymentRepository, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real PostgreSQL test")
	}

	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	schema := "pgtest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		admin.Close()
	})

	// search_path in the connection string applies to every pooled
	// connection, which the concurrency test needs (>= 2 connections).
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
	return NewPaymentRepository(db), db
}

func insertPGPayment(t *testing.T, db *sql.DB, status string) uuid.UUID {
	t.Helper()
	merchantID, id := uuid.New(), uuid.New()
	if _, err := db.Exec(
		`INSERT INTO merchants (id, name, email, business_name) VALUES ($1, 'm', $2, 'b')`,
		merchantID, merchantID.String()+"@example.com"); err != nil {
		t.Fatalf("insert merchant: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, payment_method, provider_name, status, description, expires_at)
		 VALUES ($1, $2, $3, 1000, 'qris', 'cashi', $4, 'd', now() + interval '1 hour')`,
		id, "REF-"+id.String(), merchantID, status); err != nil {
		t.Fatalf("insert payment: %v", err)
	}
	return id
}

func pgStatus(t *testing.T, db *sql.DB, id uuid.UUID) (string, bool) {
	t.Helper()
	var status string
	var paidAt sql.NullTime
	if err := db.QueryRow(`SELECT status, paid_at FROM payments WHERE id = $1`, id).Scan(&status, &paidAt); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	return status, paidAt.Valid
}

func TestPG_UpdateStatus_PendingToPaid(t *testing.T) {
	repo, db := newPGRepo(t)
	id := insertPGPayment(t, db, payment.StatusPending)
	paidAt := time.Now()

	prev, err := repo.UpdateStatus(context.Background(), id, payment.StatusPaid, &paidAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prev != payment.StatusPending {
		t.Errorf("previous = %q, want pending", prev)
	}
	if status, hasPaidAt := pgStatus(t, db, id); status != payment.StatusPaid || !hasPaidAt {
		t.Errorf("row status=%q paid_at set=%v, want paid/true", status, hasPaidAt)
	}
}

func TestPG_UpdateStatus_ExpiredToPaidReportsExpired(t *testing.T) {
	repo, db := newPGRepo(t)
	id := insertPGPayment(t, db, payment.StatusExpired)
	paidAt := time.Now()

	prev, err := repo.UpdateStatus(context.Background(), id, payment.StatusPaid, &paidAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prev != payment.StatusExpired {
		t.Errorf("previous = %q, want expired", prev)
	}
}

func TestPG_UpdateStatus_ConcurrentPaidExactlyOneWins(t *testing.T) {
	repo, db := newPGRepo(t)
	id := insertPGPayment(t, db, payment.StatusPending)
	paidAt := time.Now()

	const n = 2
	type result struct {
		prev string
		err  error
	}
	results := make([]result, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i].prev, results[i].err = repo.UpdateStatus(context.Background(), id, payment.StatusPaid, &paidAt)
		}(i)
	}
	close(start)
	wg.Wait()

	wins, terminal := 0, 0
	for _, r := range results {
		switch {
		case r.err == nil:
			wins++
			if r.prev != payment.StatusPending {
				t.Errorf("winner previous = %q, want pending", r.prev)
			}
		case errors.Is(r.err, payment.ErrPaymentAlreadyTerminal):
			terminal++
		default:
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if wins != 1 || terminal != n-1 {
		t.Fatalf("wins=%d terminal=%d, want 1 and %d", wins, terminal, n-1)
	}
}

// After expiry has won, a paid settlement still reports expired as the
// previous status, and a racing paid caller does too: the late-settlement
// signal survives either ordering.
func TestPG_UpdateStatus_PaidAfterExpiredWon(t *testing.T) {
	repo, db := newPGRepo(t)
	id := insertPGPayment(t, db, payment.StatusPending)
	ctx := context.Background()

	if _, err := repo.UpdateStatus(ctx, id, payment.StatusExpired, nil); err != nil {
		t.Fatalf("expire: %v", err)
	}

	paidAt := time.Now()
	var wg sync.WaitGroup
	results := make([]string, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.UpdateStatus(ctx, id, payment.StatusPaid, &paidAt)
		}(i)
	}
	wg.Wait()

	wins := 0
	for i := range results {
		if errs[i] == nil {
			wins++
			if results[i] != payment.StatusExpired {
				t.Errorf("winner previous = %q, want expired", results[i])
			}
		} else if !errors.Is(errs[i], payment.ErrPaymentAlreadyTerminal) {
			t.Errorf("loser error = %v, want ErrPaymentAlreadyTerminal", errs[i])
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want 1", wins)
	}
}

func TestPG_UpdateStatus_MissingID(t *testing.T) {
	repo, _ := newPGRepo(t)

	_, err := repo.UpdateStatus(context.Background(), uuid.New(), payment.StatusPaid, nil)
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("expected ErrPaymentNotFound, got %v", err)
	}
}

// pgPaymentSpec lets reconcile tests control the columns the candidate
// query filters and orders on. The columns are TIMESTAMPTZ (migration 012),
// so any zone round-trips; times are passed as UTC only to keep failure
// output readable.
type pgPaymentSpec struct {
	status      string
	environment string
	providerRef string // "" stores NULL
	createdAt   time.Time
	expiresAt   time.Time
}

func insertPGSpecPayment(t *testing.T, db *sql.DB, s pgPaymentSpec) uuid.UUID {
	t.Helper()
	merchantID, id := uuid.New(), uuid.New()
	if _, err := db.Exec(
		`INSERT INTO merchants (id, name, email, business_name) VALUES ($1, 'm', $2, 'b')`,
		merchantID, merchantID.String()+"@example.com"); err != nil {
		t.Fatalf("insert merchant: %v", err)
	}
	var ref interface{}
	if s.providerRef != "" {
		ref = s.providerRef
	}
	if _, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, payment_method, provider_name, provider_reference, status, description, environment, expires_at, created_at)
		 VALUES ($1, $2, $3, 1000, 'qris', 'cashi', $4, $5, 'd', $6, $7, $8)`,
		id, "REF-"+id.String(), merchantID, ref, s.status, s.environment, s.expiresAt.UTC(), s.createdAt.UTC()); err != nil {
		t.Fatalf("insert payment: %v", err)
	}
	return id
}

func TestPG_ListReconcileCandidates_Selection(t *testing.T) {
	repo, db := newPGRepo(t)
	now := time.Now().UTC()
	inWindow := now.Add(-2 * time.Hour)
	beforeWindow := now.Add(-30 * time.Hour)
	future := now.Add(time.Hour)
	prod := payment.EnvironmentProduction

	pendingIn := insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusPending, prod, "P1", inWindow, future})
	insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusPending, payment.EnvironmentSandbox, "P2", inWindow, future})
	insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusPending, prod, "P3", beforeWindow, future})
	expiredIn := insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusExpired, prod, "P4", beforeWindow, now.Add(-time.Hour)})
	insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusExpired, prod, "P5", beforeWindow, now.Add(-26 * time.Hour)})
	insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusPending, prod, "", inWindow, future})
	mismatched := insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusPending, prod, "P7", inWindow, future})
	insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusPaid, prod, "P8", inWindow, future})
	// Expired rows use the 6h expires_at window, not the 24h pending one.
	insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusExpired, prod, "P9", beforeWindow, now.Add(-8 * time.Hour)})
	expiredIn3h := insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusExpired, prod, "P10", beforeWindow, now.Add(-3 * time.Hour)})
	// A webhook-only mismatch must NOT exclude the payment from reconcile.
	webhookMismatched := insertPGSpecPayment(t, db, pgPaymentSpec{payment.StatusPending, prod, "P11", inWindow, future})

	insertEvent := func(paymentID uuid.UUID, ref, eventType, processingError string) {
		t.Helper()
		if _, err := db.Exec(
			`INSERT INTO webhook_events (id, payment_id, provider_name, provider_reference, event_type, status, raw_payload, processing_error)
			 VALUES ($1, $2, 'cashi', $3, $4, 'rejected', '{}', $5)`,
			uuid.New(), paymentID, ref, eventType, processingError); err != nil {
			t.Fatalf("insert webhook event: %v", err)
		}
	}
	insertEvent(mismatched, "P7", "reconcile.amount_mismatch", "amount_mismatch: expected 1, provider 2, webhook none")
	insertEvent(webhookMismatched, "P11", "payment.paid", "webhook_amount_mismatch: expected 1, provider 1, webhook 2")

	got, err := repo.ListReconcileCandidates(context.Background(), now, 24*time.Hour, 6*time.Hour, 50, nil)
	if err != nil {
		t.Fatalf("ListReconcileCandidates: %v", err)
	}
	gotIDs := map[uuid.UUID]bool{}
	for _, p := range got {
		gotIDs[p.ID] = true
	}
	if len(got) != 4 || !gotIDs[pendingIn] || !gotIDs[expiredIn] || !gotIDs[expiredIn3h] || !gotIDs[webhookMismatched] {
		t.Fatalf("want exactly {in-window pending, expired 1h ago, expired 3h ago, webhook-only mismatch}, got %d rows: %v", len(got), gotIDs)
	}
}

func TestPG_ListReconcileCandidates_KeysetPaging(t *testing.T) {
	repo, db := newPGRepo(t)
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	var ids []uuid.UUID // newest first
	for i := 1; i <= 3; i++ {
		ids = append(ids, insertPGSpecPayment(t, db, pgPaymentSpec{
			payment.StatusPending, payment.EnvironmentProduction, "K" + string(rune('0'+i)),
			now.Add(-time.Duration(i) * time.Hour), future}))
	}

	page1, err := repo.ListReconcileCandidates(context.Background(), now, 24*time.Hour, 6*time.Hour, 2, nil)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].ID != ids[0] || page1[1].ID != ids[1] {
		t.Fatalf("page 1 = %v, want newest two %v", page1, ids[:2])
	}
	last := page1[1]
	page2, err := repo.ListReconcileCandidates(context.Background(), now, 24*time.Hour, 6*time.Hour, 2,
		&payment.ReconcileCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != ids[2] {
		t.Fatalf("page 2 = %v, want only the oldest %v", page2, ids[2])
	}
}

// insertPGCallback seeds one callback delivery under a fresh merchant/payment.
// nextRetry may be nil (NULL).
func insertPGCallback(t *testing.T, db *sql.DB, status string, attempt int, updatedAt time.Time, nextRetry *time.Time) uuid.UUID {
	t.Helper()
	paymentID := insertPGPayment(t, db, payment.StatusPaid)
	var merchantID uuid.UUID
	if err := db.QueryRow(`SELECT merchant_id FROM payments WHERE id = $1`, paymentID).Scan(&merchantID); err != nil {
		t.Fatalf("read merchant id: %v", err)
	}
	id := uuid.New()
	if _, err := db.Exec(`
		INSERT INTO merchant_callback_deliveries
			(id, payment_id, merchant_id, event_type, target_url, request_payload,
			 attempt_number, status, next_retry_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'payment.paid', 'https://m.example/hook', '{"k":"v"}'::jsonb,
		        $4, $5, $6, $7, $7)`,
		id, paymentID, merchantID, attempt, status, nextRetry, updatedAt); err != nil {
		t.Fatalf("insert callback: %v", err)
	}
	return id
}

func pgCallbackStatus(t *testing.T, db *sql.DB, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM merchant_callback_deliveries WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read callback status: %v", err)
	}
	return s
}

func pgCallbackUpdatedAt(t *testing.T, db *sql.DB, id uuid.UUID) time.Time {
	t.Helper()
	var u time.Time
	if err := db.QueryRow(`SELECT updated_at FROM merchant_callback_deliveries WHERE id = $1`, id).Scan(&u); err != nil {
		t.Fatalf("read callback updated_at: %v", err)
	}
	return u
}

// assertWithin fails unless got is within d of want.
func assertWithin(t *testing.T, label string, got, want time.Time, d time.Duration) {
	t.Helper()
	diff := got.Sub(want)
	if diff < 0 {
		diff = -diff
	}
	if diff > d {
		t.Errorf("%s = %v, want within %v of %v", label, got, d, want)
	}
}

func TestPG_ClaimDue_ClaimsPendingAndStaleSending(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	// Microsecond precision so DB round-trips compare exactly.
	now := time.Now().UTC().Truncate(time.Microsecond)
	staleAfter := 2 * time.Minute

	pending := insertPGCallback(t, db, "pending", 1, now.Add(-time.Second), nil)
	stale := insertPGCallback(t, db, "sending", 1, now.Add(-3*time.Minute), nil)
	// Exactly at the cutoff: the boundary is inclusive (updated_at <= cutoff).
	boundary := insertPGCallback(t, db, "sending", 1, now.Add(-staleAfter), nil)
	freshAt := now.Add(-time.Second)
	fresh := insertPGCallback(t, db, "sending", 1, freshAt, nil)
	success := insertPGCallback(t, db, "success", 1, now, nil)
	failed := insertPGCallback(t, db, "failed", 1, now, nil)

	got, err := repo.ClaimDue(context.Background(), now, 50, staleAfter)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	ids := map[uuid.UUID]bool{}
	for _, d := range got {
		ids[d.ID] = true
		if d.Status != "sending" {
			t.Errorf("returned status = %q, want sending", d.Status)
		}
	}
	if len(got) != 3 || !ids[pending] || !ids[stale] || !ids[boundary] {
		t.Fatalf("claimed %v, want exactly pending %s, stale %s, boundary %s", ids, pending, stale, boundary)
	}
	for _, id := range []uuid.UUID{pending, stale, boundary} {
		if s := pgCallbackStatus(t, db, id); s != "sending" {
			t.Errorf("%s status = %q, want sending", id, s)
		}
		// A claim must refresh updated_at, or the stale-recovery clock never restarts.
		assertWithin(t, "claimed updated_at", pgCallbackUpdatedAt(t, db, id), now, time.Second)
	}
	if s := pgCallbackStatus(t, db, fresh); s != "sending" {
		t.Errorf("fresh status = %q, want sending", s)
	}
	if u := pgCallbackUpdatedAt(t, db, fresh); !u.Equal(freshAt) {
		t.Errorf("unclaimed fresh updated_at = %v, want unchanged %v", u, freshAt)
	}
	if s := pgCallbackStatus(t, db, success); s != "success" {
		t.Errorf("success row changed to %q", s)
	}
	if s := pgCallbackStatus(t, db, failed); s != "failed" {
		t.Errorf("failed row changed to %q", s)
	}
}

// Deterministic SKIP LOCKED proof: rows held by another open transaction must
// be skipped immediately, not waited on and not claimed.
func TestPG_ClaimDue_SkipsRowsLockedByAnotherTransaction(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)

	var all []uuid.UUID
	for i := 0; i < 6; i++ {
		all = append(all, insertPGCallback(t, db, "pending", 1, now.Add(-time.Second), nil))
	}
	locked, free := all[:3], all[3:]

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	lockedStrs := make([]string, len(locked))
	for i, id := range locked {
		lockedStrs[i] = id.String()
	}
	rows, err := tx.Query(`SELECT id FROM merchant_callback_deliveries WHERE id = ANY($1::uuid[]) FOR UPDATE`,
		"{"+strings.Join(lockedStrs, ",")+"}")
	if err != nil {
		t.Fatalf("lock rows: %v", err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	if n != 3 {
		t.Fatalf("locked %d rows, want 3", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	got, err := repo.ClaimDue(ctx, now, 50, 2*time.Minute)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ClaimDue blocked or failed after %v: %v", elapsed, err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("ClaimDue took %v, want prompt return (< 2s)", elapsed)
	}
	claimed := map[uuid.UUID]bool{}
	for _, d := range got {
		claimed[d.ID] = true
	}
	if len(got) != 3 {
		t.Fatalf("claimed %d rows, want 3", len(got))
	}
	for _, id := range free {
		if !claimed[id] {
			t.Errorf("unlocked row %s not claimed", id)
		}
	}
	for _, id := range locked {
		if claimed[id] {
			t.Errorf("locked row %s was claimed", id)
		}
		if s := pgCallbackStatus(t, db, id); s != "pending" {
			t.Errorf("locked row %s status = %q, want pending", id, s)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestPG_ClaimDue_ConcurrentClaimsNeverOverlap(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var all []uuid.UUID
	for i := 0; i < 20; i++ {
		all = append(all, insertPGCallback(t, db, "pending", 1, now.Add(-time.Second), nil))
	}

	results := make([][]uuid.UUID, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			got, err := repo.ClaimDue(context.Background(), now, 20, 2*time.Minute)
			errs[w] = err
			for _, d := range got {
				results[w] = append(results[w], d.ID)
			}
		}(w)
	}
	close(start)
	wg.Wait()

	seen := map[uuid.UUID]bool{}
	total := 0
	for w := range results {
		if errs[w] != nil {
			t.Fatalf("worker %d: %v", w, errs[w])
		}
		for _, id := range results[w] {
			if seen[id] {
				t.Errorf("delivery %s claimed by both workers", id)
			}
			seen[id] = true
			total++
		}
	}
	if total != 20 {
		t.Errorf("total claimed = %d, want 20 (worker sizes %d/%d)", total, len(results[0]), len(results[1]))
	}
	for _, id := range all {
		if s := pgCallbackStatus(t, db, id); s != "sending" {
			t.Errorf("%s status = %q, want sending", id, s)
		}
	}
}

func TestPG_ClaimDue_StressWorkersDrainWithoutDuplicates(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var all []uuid.UUID
	for i := 0; i < 40; i++ {
		all = append(all, insertPGCallback(t, db, "pending", 1, now.Add(-time.Second), nil))
	}

	const workers = 4
	results := make([][]uuid.UUID, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for {
				got, err := repo.ClaimDue(context.Background(), now, 5, 2*time.Minute)
				if err != nil {
					errs[w] = err
					return
				}
				if len(got) == 0 {
					return
				}
				for _, d := range got {
					results[w] = append(results[w], d.ID)
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()

	seen := map[uuid.UUID]bool{}
	for w := range results {
		if errs[w] != nil {
			t.Fatalf("worker %d: %v", w, errs[w])
		}
		for _, id := range results[w] {
			if seen[id] {
				t.Errorf("delivery %s claimed twice", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 40 {
		t.Errorf("distinct claimed = %d, want 40", len(seen))
	}
	for _, id := range all {
		if s := pgCallbackStatus(t, db, id); s != "sending" {
			t.Errorf("%s status = %q, want sending", id, s)
		}
	}
}

func TestPG_RequeueDueFailed(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)

	due := insertPGCallback(t, db, "failed", 2, now.Add(-10*time.Minute), &past)
	notDue := insertPGCallback(t, db, "failed", 2, now.Add(-10*time.Minute), &future)
	noRetry := insertPGCallback(t, db, "failed", 2, now.Add(-10*time.Minute), nil)

	n, err := repo.RequeueDueFailed(context.Background(), now, 50)
	if err != nil {
		t.Fatalf("RequeueDueFailed: %v", err)
	}
	if n != 1 {
		t.Fatalf("requeued = %d, want 1", n)
	}

	var retryNull bool
	if err := db.QueryRow(`SELECT next_retry_at IS NULL FROM merchant_callback_deliveries WHERE id = $1`, due).Scan(&retryNull); err != nil || !retryNull {
		t.Errorf("old due row next_retry_at NULL = %v (err %v), want true", retryNull, err)
	}
	if err := db.QueryRow(`SELECT next_retry_at IS NULL FROM merchant_callback_deliveries WHERE id = $1`, notDue).Scan(&retryNull); err != nil || retryNull {
		t.Errorf("not-due row next_retry_at was cleared (err %v)", err)
	}
	if err := db.QueryRow(`SELECT next_retry_at IS NULL FROM merchant_callback_deliveries WHERE id = $1`, noRetry).Scan(&retryNull); err != nil || !retryNull {
		t.Errorf("no-retry row changed (err %v)", err)
	}
	// Rows that were not requeued must have no pending sibling.
	for _, id := range []uuid.UUID{notDue, noRetry} {
		var siblings int
		if err := db.QueryRow(`
			SELECT count(*) FROM merchant_callback_deliveries
			WHERE status = 'pending' AND payment_id = (SELECT payment_id FROM merchant_callback_deliveries WHERE id = $1)`, id).Scan(&siblings); err != nil || siblings != 0 {
			t.Errorf("row %s has %d pending siblings (err %v), want 0", id, siblings, err)
		}
	}

	var (
		cnt, attempt                    int
		url, payload, status, eventType string
		nextNull                        bool
		createdAt                       time.Time
		oldPayment, newPayment          uuid.UUID
		oldMerchant, newMerchant        uuid.UUID
	)
	if err := db.QueryRow(`SELECT payment_id, merchant_id FROM merchant_callback_deliveries WHERE id = $1`, due).Scan(&oldPayment, &oldMerchant); err != nil {
		t.Fatalf("read old row: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM merchant_callback_deliveries WHERE status = 'pending' AND payment_id = $1`, oldPayment).Scan(&cnt); err != nil {
		t.Fatalf("count new rows: %v", err)
	}
	err = db.QueryRow(`
		SELECT payment_id, merchant_id, attempt_number, target_url, request_payload::text, status, event_type,
		       next_retry_at IS NULL, created_at
		FROM merchant_callback_deliveries WHERE status = 'pending' AND payment_id = $1`, oldPayment).
		Scan(&newPayment, &newMerchant, &attempt, &url, &payload, &status, &eventType, &nextNull, &createdAt)
	if err != nil {
		t.Fatalf("read new row: %v", err)
	}
	if cnt != 1 || attempt != 3 || url != "https://m.example/hook" || payload != `{"k": "v"}` {
		t.Errorf("new row count=%d attempt=%d url=%q payload=%q, want 1/3/same/same", cnt, attempt, url, payload)
	}
	if newPayment != oldPayment || newMerchant != oldMerchant || eventType != "payment.paid" || status != "pending" || !nextNull {
		t.Errorf("new row ids/event/status/next_retry mismatch: payment %s/%s merchant %s/%s event %q status %q nextNull %v",
			newPayment, oldPayment, newMerchant, oldMerchant, eventType, status, nextNull)
	}
	assertWithin(t, "new row created_at", createdAt, now, time.Second)

	n, err = repo.RequeueDueFailed(context.Background(), now, 50)
	if err != nil || n != 0 {
		t.Errorf("second call = (%d, %v), want (0, nil)", n, err)
	}
}

func TestPG_RequeueDueFailed_RespectsLimit(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	past := now.Add(-time.Minute)

	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		ids = append(ids, insertPGCallback(t, db, "failed", 1, now.Add(-10*time.Minute), &past))
	}

	n, err := repo.RequeueDueFailed(context.Background(), now, 2)
	if err != nil {
		t.Fatalf("RequeueDueFailed: %v", err)
	}
	if n != 2 {
		t.Fatalf("requeued = %d, want 2", n)
	}
	var pending, stillDue int
	if err := db.QueryRow(`SELECT count(*) FROM merchant_callback_deliveries WHERE status = 'pending'`).Scan(&pending); err != nil || pending != 2 {
		t.Errorf("pending rows = %d (err %v), want 2", pending, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM merchant_callback_deliveries WHERE id = ANY($1::uuid[]) AND next_retry_at IS NOT NULL`,
		"{"+ids[0].String()+","+ids[1].String()+","+ids[2].String()+"}").Scan(&stillDue); err != nil || stillDue != 1 {
		t.Errorf("untouched due rows = %d (err %v), want 1", stillDue, err)
	}
}

func TestPG_ClearRetrySchedule(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seededAt := now.Add(-time.Hour)
	sched := now.Add(-time.Minute)

	due := insertPGCallback(t, db, "failed", 1, seededAt, &sched)
	// The others carry a schedule too (except the NULL case), so "untouched"
	// proves the status guard, not just the NULL filter.
	untouched := map[string]uuid.UUID{
		"success":          insertPGCallback(t, db, "success", 1, seededAt, &sched),
		"pending":          insertPGCallback(t, db, "pending", 1, seededAt, &sched),
		"sending":          insertPGCallback(t, db, "sending", 1, seededAt, &sched),
		"failed, no retry": insertPGCallback(t, db, "failed", 5, seededAt, nil),
	}

	changed, err := repo.ClearRetrySchedule(context.Background(), due, now)
	if err != nil || !changed {
		t.Fatalf("due failed row: changed=%v err=%v, want true", changed, err)
	}
	var isNull bool
	if err := db.QueryRow(`SELECT next_retry_at IS NULL FROM merchant_callback_deliveries WHERE id = $1`, due).Scan(&isNull); err != nil || !isNull {
		t.Errorf("due row next_retry_at NULL = %v (err %v), want true", isNull, err)
	}
	if s := pgCallbackStatus(t, db, due); s != "failed" {
		t.Errorf("due row status = %q, want failed unchanged", s)
	}
	assertWithin(t, "cleared updated_at", pgCallbackUpdatedAt(t, db, due), now, time.Second)

	// Clearing twice is a no-op.
	if changed, err := repo.ClearRetrySchedule(context.Background(), due, now); err != nil || changed {
		t.Errorf("second clear: changed=%v err=%v, want false", changed, err)
	}

	for name, id := range untouched {
		var before struct {
			status string
			next   sql.NullTime
		}
		if err := db.QueryRow(`SELECT status, next_retry_at FROM merchant_callback_deliveries WHERE id = $1`, id).
			Scan(&before.status, &before.next); err != nil {
			t.Fatalf("%s: read: %v", name, err)
		}
		changed, err := repo.ClearRetrySchedule(context.Background(), id, now)
		if err != nil || changed {
			t.Errorf("%s: changed=%v err=%v, want false", name, changed, err)
		}
		var after struct {
			status string
			next   sql.NullTime
		}
		if err := db.QueryRow(`SELECT status, next_retry_at FROM merchant_callback_deliveries WHERE id = $1`, id).
			Scan(&after.status, &after.next); err != nil {
			t.Fatalf("%s: read: %v", name, err)
		}
		if after.status != before.status || after.next.Valid != before.next.Valid || !after.next.Time.Equal(before.next.Time) {
			t.Errorf("%s: row changed from %+v to %+v", name, before, after)
		}
		if u := pgCallbackUpdatedAt(t, db, id); !u.Equal(seededAt) {
			t.Errorf("%s: updated_at = %v, want untouched %v", name, u, seededAt)
		}
	}
}

func TestPG_ReleaseClaimed_OnlyReleasesSending(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seededAt := now.Add(-time.Minute)

	sending1 := insertPGCallback(t, db, "sending", 1, seededAt, nil)
	sending2 := insertPGCallback(t, db, "sending", 1, seededAt, nil)
	pending := insertPGCallback(t, db, "pending", 1, seededAt, nil)
	success := insertPGCallback(t, db, "success", 1, seededAt, nil)
	notListed := insertPGCallback(t, db, "sending", 1, seededAt, nil)

	n, err := repo.ReleaseClaimed(context.Background(), []uuid.UUID{sending1, sending2, pending, success}, now)
	if err != nil {
		t.Fatalf("ReleaseClaimed: %v", err)
	}
	if n != 2 {
		t.Errorf("released = %d, want 2", n)
	}
	for _, id := range []uuid.UUID{sending1, sending2} {
		if s := pgCallbackStatus(t, db, id); s != "pending" {
			t.Errorf("%s status = %q, want pending", id, s)
		}
		assertWithin(t, "released updated_at", pgCallbackUpdatedAt(t, db, id), now, time.Second)
	}
	for id, want := range map[uuid.UUID]string{pending: "pending", success: "success", notListed: "sending"} {
		if s := pgCallbackStatus(t, db, id); s != want {
			t.Errorf("%s status = %q, want %q untouched", id, s, want)
		}
		if u := pgCallbackUpdatedAt(t, db, id); !u.Equal(seededAt) {
			t.Errorf("%s updated_at = %v, want untouched %v", id, u, seededAt)
		}
	}
}

// Pins the premise of the service's text sanitising and its fake repo:
// PostgreSQL refuses invalid UTF-8 and NUL in TEXT, so such a result write
// fails outright (and the row would stay 'sending').
func TestPG_UpdateResult_RejectsUnstorableText(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantCallbackRepository(db)
	for name, body := range map[string]string{"invalid UTF-8": "ok\xff\xfe", "NUL": "a\x00b"} {
		t.Run(name, func(t *testing.T) {
			id := insertPGCallback(t, db, "sending", 1, time.Now().UTC(), nil)
			code := 200
			d := &merchant.CallbackDelivery{ID: id, Status: merchant.CallbackStatusSuccess, HTTPStatus: &code, ResponseBody: &body, AttemptNumber: 1}
			if err := repo.UpdateResult(context.Background(), d); err == nil {
				t.Fatalf("UpdateResult accepted %q; the sanitiser's premise no longer holds", body)
			}
			if s := pgCallbackStatus(t, db, id); s != "sending" {
				t.Errorf("status = %q, want the failed write to change nothing", s)
			}
		})
	}
}

// Migration 016: the queue's partial indexes exist and the status column
// documents the 'sending' state. Applying all migrations twice must also be
// harmless (the files are meant to be idempotent).
func TestPG_CallbackQueueIndexesMigration(t *testing.T) {
	_, db := newPGRepo(t)

	for _, name := range []string{
		"idx_merchant_callback_deliveries_retry_due",
		"idx_merchant_callback_deliveries_queue",
	} {
		var def string
		err := db.QueryRow(
			`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`, name).Scan(&def)
		if err != nil {
			t.Fatalf("index %s missing: %v", name, err)
		}
		if !strings.Contains(def, "WHERE") {
			t.Errorf("index %s should be partial, got %s", name, def)
		}
	}

	var comment string
	if err := db.QueryRow(`SELECT col_description('merchant_callback_deliveries'::regclass,
		(SELECT attnum FROM pg_attribute WHERE attrelid = 'merchant_callback_deliveries'::regclass AND attname = 'status'))`).Scan(&comment); err != nil {
		t.Fatalf("read status comment: %v", err)
	}
	if !strings.Contains(comment, "sending") {
		t.Errorf("status comment = %q, want it to mention sending", comment)
	}

	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "016_add_callback_queue_indexes.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Errorf("re-applying 016 must be idempotent: %v", err)
	}
}
