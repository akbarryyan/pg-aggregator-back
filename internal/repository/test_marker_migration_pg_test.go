package repository

import (
	"os"
	"path/filepath"
	"testing"
)

// Migration 022 moves dashboard test payments from the old marker to the new
// one, leaves every other customer name alone, and is safe to re-apply.
func TestMigration022_RenamesDashboardTestMarker(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	old := insertLedgerPayment(t, db, m, 2000, 0, "pending", "sandbox")
	other := insertLedgerPayment(t, db, m, 2000, 0, "pending", "sandbox")
	none := insertLedgerPayment(t, db, m, 2000, 0, "pending", "sandbox")
	if _, err := db.Exec(`UPDATE payments SET customer_name = 'Dashboard test' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE payments SET customer_name = 'Budi' WHERE id = $1`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE payments SET customer_name = NULL WHERE id = $1`, none); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "022_rename_dashboard_test_customer.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("applying migration 022 (run %d): %v", i+1, err)
		}
	}

	get := func(id interface{}) *string {
		var s *string
		if err := db.QueryRow(`SELECT customer_name FROM payments WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := get(old); s == nil || *s != "TEST PAYMENT" {
		t.Errorf("old marker = %v, want TEST PAYMENT", s)
	}
	if s := get(other); s == nil || *s != "Budi" {
		t.Errorf("other customer changed: %v", s)
	}
	if s := get(none); s != nil {
		t.Errorf("NULL customer changed: %v", *s)
	}
}
