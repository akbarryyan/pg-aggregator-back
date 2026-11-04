package repository

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func insertConstraintMerchant(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO merchants (id, name, email, business_name) VALUES ($1, 'm', $2, 'b')`,
		id, id.String()+"@example.com"); err != nil {
		t.Fatalf("insert merchant: %v", err)
	}
	return id
}

// insertConstraintPayment inserts a payment and returns the error so each
// test can assert on a constraint violation.
func insertConstraintPayment(db *sql.DB, merchantID uuid.UUID, amount int64, status string, providerRef *string) error {
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, payment_method, provider_name, provider_reference, status, description, expires_at)
		 VALUES ($1, $2, $3, $4, 'qris', 'cashi', $5, $6, 'd', now() + interval '1 hour')`,
		id, "REF-"+id.String(), merchantID, amount, providerRef, status)
	return err
}

func wantViolation(t *testing.T, err error, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s violation, insert succeeded", constraint)
	}
	if !strings.Contains(err.Error(), constraint) {
		t.Fatalf("expected error to name %s, got: %v", constraint, err)
	}
}

func TestPaymentConstraints_AmountMustBePositive(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	for _, amt := range []int64{0, -1} {
		wantViolation(t, insertConstraintPayment(db, m, amt, "pending", nil), "payments_amount_positive")
	}
	if err := insertConstraintPayment(db, m, 1, "pending", nil); err != nil {
		t.Fatalf("amount 1 must be accepted: %v", err)
	}
}

func TestPaymentConstraints_StatusMustBeKnown(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	wantViolation(t, insertConstraintPayment(db, m, 1000, "processing", nil), "payments_status_valid")
	for _, st := range []string{"pending", "paid", "expired", "failed", "cancelled"} {
		if err := insertConstraintPayment(db, m, 1000, st, nil); err != nil {
			t.Fatalf("status %q must be accepted: %v", st, err)
		}
	}
}

func TestPaymentConstraints_ProviderReferenceUniquePerProvider(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	ref := "PROV-1"
	if err := insertConstraintPayment(db, m, 1000, "pending", &ref); err != nil {
		t.Fatal(err)
	}
	wantViolation(t, insertConstraintPayment(db, m, 1000, "pending", &ref), "uq_payments_provider_reference")

	// Same reference under a different provider name is a different payment.
	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, payment_method, provider_name, provider_reference, status, description, expires_at)
		 VALUES ($1, $2, $3, 1000, 'qris', 'sandbox', $4, 'pending', 'd', now() + interval '1 hour')`,
		id, "REF-"+id.String(), m, ref); err != nil {
		t.Fatalf("same reference under another provider must be accepted: %v", err)
	}

	// Any number of payments may have no provider reference yet.
	for i := 0; i < 3; i++ {
		if err := insertConstraintPayment(db, m, 1000, "pending", nil); err != nil {
			t.Fatalf("NULL provider_reference must not collide: %v", err)
		}
	}
}

func TestPaymentConstraints_MerchantWithPaymentsCannotBeDeleted(t *testing.T) {
	_, db := newPGRepo(t)
	withPayment := insertConstraintMerchant(t, db)
	if err := insertConstraintPayment(db, withPayment, 1000, "paid", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM merchants WHERE id = $1`, withPayment); err == nil {
		t.Fatal("deleting a merchant that has payments must be rejected")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM payments WHERE merchant_id = $1`, withPayment).Scan(&n); err != nil || n != 1 {
		t.Fatalf("payment history must survive the rejected delete: n=%d err=%v", n, err)
	}

	// A merchant without payments (registration rollback) is still deletable,
	// and its dependent rows keep cascading.
	empty := insertConstraintMerchant(t, db)
	if _, err := db.Exec(`DELETE FROM merchants WHERE id = $1`, empty); err != nil {
		t.Fatalf("deleting a merchant without payments must work: %v", err)
	}
}

func TestMigration019_IsIdempotent(t *testing.T) {
	_, db := newPGRepo(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "019_add_payment_integrity_constraints.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("re-applying migration 019 (run %d): %v", i+1, err)
		}
	}
	m := insertConstraintMerchant(t, db)
	wantViolation(t, insertConstraintPayment(db, m, 0, "pending", nil), "payments_amount_positive")

	// The FK must still be exactly one RESTRICT constraint after the re-applies.
	var fkCount int
	var delType string
	if err := db.QueryRow(`SELECT count(*), COALESCE(min(confdeltype::text), '')
		FROM pg_constraint
		WHERE conrelid = 'payments'::regclass AND confrelid = 'merchants'::regclass AND contype = 'f'`).
		Scan(&fkCount, &delType); err != nil {
		t.Fatal(err)
	}
	if fkCount != 1 || delType != "r" {
		t.Fatalf("want exactly one payments->merchants FK with confdeltype 'r', got count=%d type=%q", fkCount, delType)
	}
	if err := insertConstraintPayment(db, m, 1000, "pending", nil); err != nil {
		t.Fatalf("valid payment insert: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM merchants WHERE id = $1`, m); err == nil {
		t.Fatal("deleting a merchant that has a payment must be rejected after re-applying 019")
	}
}
