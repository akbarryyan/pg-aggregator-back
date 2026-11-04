package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
	"github.com/google/uuid"
)

// Real-PostgreSQL checks for migration 017 and the claim semantics (opt-in via
// TEST_DATABASE_URL): the race, expiry and abandon rules live in SQL, so a
// mock cannot prove them.

// h64 gives a 64-char hex hash like the service produces (the column is CHAR(64),
// which would pad shorter values with spaces).
func h64(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func insertPGMerchant(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(`INSERT INTO merchants (id, name, business_name, email) VALUES ($1, 'M', 'B', $2)`,
		id, id.String()+"@example.com"); err != nil {
		t.Fatalf("insert merchant: %v", err)
	}
	return id
}

func mustClaim(t *testing.T, r *IdempotencyRepository, m uuid.UUID, env, key, hash string, now time.Time) (*idempotency.Record, bool) {
	t.Helper()
	rec, won, err := r.Claim(context.Background(), m, env, key, hash, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return rec, won
}

func TestPG_Idempotency_FirstClaimWinsSecondSeesExisting(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	m := insertPGMerchant(t, db)
	now := time.Now().UTC()

	first, won := mustClaim(t, r, m, "sandbox", "k1", h64("h1"), now)
	if !won || first == nil {
		t.Fatal("first claim must win")
	}
	second, won := mustClaim(t, r, m, "sandbox", "k1", h64("h1"), now)
	if won {
		t.Fatal("second claim must not win")
	}
	if second == nil || second.PaymentID != nil || second.ID != first.ID || second.RequestHash != h64("h1") {
		t.Fatalf("want the first record without payment, got %+v", second)
	}
}

func TestPG_Idempotency_CompleteLinksPayment(t *testing.T) {
	pr, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	_ = pr
	pid := insertPGPayment(t, db, "pending")
	var m uuid.UUID
	if err := db.QueryRow(`SELECT merchant_id FROM payments WHERE id = $1`, pid).Scan(&m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	rec, _ := mustClaim(t, r, m, "sandbox", "k1", h64("h1"), now)
	if err := r.Complete(context.Background(), rec.ID, pid); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, won := mustClaim(t, r, m, "sandbox", "k1", h64("h1"), now)
	if won || got == nil || got.PaymentID == nil || *got.PaymentID != pid {
		t.Fatalf("want existing record with payment %s, got won=%v rec=%+v", pid, won, got)
	}
}

func TestPG_Idempotency_ReleaseAllowsReclaim(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	m := insertPGMerchant(t, db)
	now := time.Now().UTC()

	rec, _ := mustClaim(t, r, m, "sandbox", "k1", h64("h1"), now)
	if err := r.Release(context.Background(), rec.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, won := mustClaim(t, r, m, "sandbox", "k1", h64("h2"), now); !won {
		t.Fatal("claim after release must win")
	}
}

func TestPG_Idempotency_ReleaseKeepsCompletedClaim(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	pid := insertPGPayment(t, db, "pending")
	var m uuid.UUID
	if err := db.QueryRow(`SELECT merchant_id FROM payments WHERE id = $1`, pid).Scan(&m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rec, _ := mustClaim(t, r, m, "sandbox", "k1", h64("h1"), now)
	if err := r.Complete(context.Background(), rec.ID, pid); err != nil {
		t.Fatal(err)
	}
	if err := r.Release(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, won := mustClaim(t, r, m, "sandbox", "k1", h64("h1"), now); won {
		t.Fatal("a completed claim must survive Release")
	}
}

func TestPG_Idempotency_AbandonedClaimTakenOverCompletedNot(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	pid := insertPGPayment(t, db, "pending")
	var m uuid.UUID
	if err := db.QueryRow(`SELECT merchant_id FROM payments WHERE id = $1`, pid).Scan(&m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	later := now.Add(idempotency.AbandonedAfter + time.Second)

	// Without a payment: not takeable just before the window, takeable after.
	mustClaim(t, r, m, "sandbox", "open", h64("h1"), now)
	if _, won := mustClaim(t, r, m, "sandbox", "open", h64("h1"), now.Add(idempotency.AbandonedAfter-time.Second)); won {
		t.Error("claim younger than AbandonedAfter must not be taken over")
	}
	taken, won := mustClaim(t, r, m, "sandbox", "open", h64("h2"), later)
	if !won || taken == nil {
		t.Error("abandoned claim must be taken over")
	}

	// With a payment: never taken over by the abandon rule.
	done, _ := mustClaim(t, r, m, "sandbox", "done", h64("h1"), now)
	if err := r.Complete(context.Background(), done.ID, pid); err != nil {
		t.Fatal(err)
	}
	if _, won := mustClaim(t, r, m, "sandbox", "done", h64("h2"), later); won {
		t.Error("completed claim must not be taken over after AbandonedAfter")
	}
}

func TestPG_Idempotency_ExpiredAfterTTLEvenWithPayment(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	pid := insertPGPayment(t, db, "pending")
	var m uuid.UUID
	if err := db.QueryRow(`SELECT merchant_id FROM payments WHERE id = $1`, pid).Scan(&m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rec, _ := mustClaim(t, r, m, "sandbox", "k1", h64("old"), now)
	if err := r.Complete(context.Background(), rec.ID, pid); err != nil {
		t.Fatal(err)
	}

	if _, won := mustClaim(t, r, m, "sandbox", "k1", h64("new"), now.Add(idempotency.TTL-time.Minute)); won {
		t.Fatal("claim inside TTL must not be taken over")
	}
	again, won := mustClaim(t, r, m, "sandbox", "k1", h64("new"), now.Add(idempotency.TTL+time.Minute))
	if !won || again == nil {
		t.Fatal("claim past TTL must be taken over")
	}
	var hash string
	var paymentID sql.NullString
	if err := db.QueryRow(`SELECT request_hash, payment_id FROM idempotency_keys WHERE id = $1`, again.ID).Scan(&hash, &paymentID); err != nil {
		t.Fatal(err)
	}
	if hash != h64("new") || paymentID.Valid {
		t.Errorf("takeover must store the new hash and clear payment, got hash=%q payment=%v", hash, paymentID)
	}
}

func TestPG_Idempotency_ConcurrentClaimsExactlyOneWins(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	m := insertPGMerchant(t, db)
	now := time.Now().UTC()

	const n = 8
	var wg sync.WaitGroup
	wins := make(chan bool, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, won, err := r.Claim(context.Background(), m, "sandbox", "race", h64("h"), now)
			if err != nil {
				t.Errorf("Claim: %v", err)
			}
			wins <- won
		}()
	}
	close(start)
	wg.Wait()
	close(wins)
	count := 0
	for w := range wins {
		if w {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want exactly 1 winner, got %d", count)
	}
}

func TestPG_Idempotency_ScopesDoNotCollide(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	m1, m2 := insertPGMerchant(t, db), insertPGMerchant(t, db)
	now := time.Now().UTC()

	if _, won := mustClaim(t, r, m1, "sandbox", "k", h64("h"), now); !won {
		t.Fatal("m1/sandbox must win")
	}
	if _, won := mustClaim(t, r, m1, "production", "k", h64("h"), now); !won {
		t.Error("other environment must not collide")
	}
	if _, won := mustClaim(t, r, m2, "sandbox", "k", h64("h"), now); !won {
		t.Error("other merchant must not collide")
	}
}

func TestPG_Idempotency_PurgeOlderThan(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	m := insertPGMerchant(t, db)
	now := time.Now().UTC()

	mustClaim(t, r, m, "sandbox", "old", h64("h"), now.Add(-48*time.Hour))
	mustClaim(t, r, m, "sandbox", "fresh", h64("h"), now)

	n, err := r.PurgeOlderThan(context.Background(), now.Add(-idempotency.TTL))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("purged %d rows, want 1", n)
	}
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE idempotency_key = 'fresh'`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("fresh row must remain (count=%d err=%v)", left, err)
	}
}

func TestPG_Idempotency_CompleteReportsLostClaim(t *testing.T) {
	_, db := newPGRepo(t)
	r := NewIdempotencyRepository(db)
	pid := insertPGPayment(t, db, "pending")
	var m uuid.UUID
	if err := db.QueryRow(`SELECT merchant_id FROM payments WHERE id = $1`, pid).Scan(&m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	// Live claim: Complete succeeds.
	live, _ := mustClaim(t, r, m, "sandbox", "live", h64("h"), now)
	if err := r.Complete(context.Background(), live.ID, pid); err != nil {
		t.Fatalf("Complete on a live claim: %v", err)
	}
	// Unknown id: ErrClaimLost.
	if err := r.Complete(context.Background(), uuid.New(), pid); !errors.Is(err, idempotency.ErrClaimLost) {
		t.Fatalf("Complete on unknown id = %v, want ErrClaimLost", err)
	}
	// Claim replaced by a takeover: the old owner's id is gone.
	old, _ := mustClaim(t, r, m, "sandbox", "taken", h64("h"), now)
	taker, won := mustClaim(t, r, m, "sandbox", "taken", h64("h"), now.Add(idempotency.AbandonedAfter+time.Second))
	if !won {
		t.Fatal("takeover must win")
	}
	if err := r.Complete(context.Background(), old.ID, pid); !errors.Is(err, idempotency.ErrClaimLost) {
		t.Fatalf("Complete by the replaced owner = %v, want ErrClaimLost", err)
	}
	if err := r.Complete(context.Background(), taker.ID, pid); err != nil {
		t.Fatalf("Complete by the new owner: %v", err)
	}
}
