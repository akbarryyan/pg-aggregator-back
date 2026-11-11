package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestMerchantStatsPG_CountsBalancesAndSignups(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewMerchantRepository(db)

	// Empty database: all zero, no sign-ups.
	empty, err := repo.Stats(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || empty.Active != 0 || empty.TotalBalance != 0 || empty.NegativeBalanceCount != 0 || len(empty.SignupsByDay) != 0 {
		t.Fatalf("empty = %+v", empty)
	}

	a := insertConstraintMerchant(t, db) // active, balance +10_000
	b := insertConstraintMerchant(t, db) // active, balance -3_000 (refund after payout)
	c := insertConstraintMerchant(t, db) // inactive, no ledger rows
	if _, err := db.Exec(`UPDATE merchants SET is_active = false WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	// Old merchant: outside a 14-day window.
	old := insertConstraintMerchant(t, db)
	if _, err := db.Exec(`UPDATE merchants SET created_at = now() - interval '40 days' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		m uuid.UUID
		n int64
	}{{a, 12_000}, {a, -2_000}, {b, -3_000}} {
		if _, err := db.Exec(
			`INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, note) VALUES ($1, $2, 'adjustment', $3, 'test')`,
			uuid.New(), e.m, e.n); err != nil {
			t.Fatalf("insert ledger: %v", err)
		}
	}

	got, err := repo.Stats(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 4 || got.Active != 3 {
		t.Fatalf("total/active = %d/%d, want 4/3", got.Total, got.Active)
	}
	if got.TotalBalance != 7_000 || got.NegativeBalanceCount != 1 {
		t.Fatalf("balance/negative = %d/%d, want 7000/1 (10000 + -3000, one negative merchant)", got.TotalBalance, got.NegativeBalanceCount)
	}
	// The day buckets follow the database's CURRENT_DATE, not the test runner's clock.
	var today string
	if err := db.QueryRow(`SELECT CURRENT_DATE::text`).Scan(&today); err != nil {
		t.Fatal(err)
	}
	var window int64
	for _, n := range got.SignupsByDay {
		window += n
	}
	if got.SignupsByDay[today] != 3 || window != 3 {
		t.Fatalf("sign-ups = %v, want 3 today and none from 40 days ago", got.SignupsByDay)
	}

	// A wider window picks the old merchant up too.
	wide, err := repo.Stats(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	var wideTotal int64
	for _, n := range wide.SignupsByDay {
		wideTotal += n
	}
	if wideTotal != 4 {
		t.Fatalf("60-day sign-ups = %d, want 4", wideTotal)
	}
}
