package repository

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

func TestFeeColumns_DefaultsKeepTodaysBehaviour(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	var bearer string
	if err := db.QueryRow(`SELECT fee_bearer FROM merchants WHERE id = $1`, m).Scan(&bearer); err != nil || bearer != "merchant" {
		t.Fatalf("new merchant fee_bearer = %q (err %v), want 'merchant'", bearer, err)
	}
	if err := insertConstraintPayment(db, m, 1000, "pending", nil); err != nil {
		t.Fatal(err)
	}
	var fee int64
	if err := db.QueryRow(`SELECT fee_amount FROM payments WHERE merchant_id = $1`, m).Scan(&fee); err != nil || fee != 0 {
		t.Fatalf("new payment fee_amount = %d (err %v), want 0", fee, err)
	}
}

func TestFeeColumns_Constraints(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	if _, err := db.Exec(`UPDATE merchants SET fee_bearer = 'platform' WHERE id = $1`, m); err == nil {
		t.Error("fee_bearer outside {merchant, customer} must be rejected")
	}
	if _, err := db.Exec(`UPDATE merchants SET fee_bearer = 'customer' WHERE id = $1`, m); err != nil {
		t.Errorf("fee_bearer customer must be accepted: %v", err)
	}
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, fee_amount, payment_method, provider_name, status, description, expires_at)
		 VALUES ($1, $2, $3, 1000, -1, 'qris', 'cashi', 'pending', 'd', now() + interval '1 hour')`,
		id, "REF-"+id.String(), m)
	wantViolation(t, err, "payments_fee_amount_non_negative")
}

func TestFeeColumns_RepositoryRoundTrip(t *testing.T) {
	repo, db := newPGRepo(t)
	ctx := context.Background()
	m := insertConstraintMerchant(t, db)
	now := time.Now().UTC()
	p := &payment.Payment{
		ID: uuid.New(), Reference: "REF-RT-" + uuid.NewString(), MerchantID: m,
		Amount: 2000, FeeAmount: 15, Currency: "IDR", PaymentMethod: "qris",
		ProviderName: "cashi", Status: payment.StatusPending, Description: "d",
		Environment: payment.EnvironmentProduction,
		ExpiresAt:   now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount != 2000 || got.FeeAmount != 15 || got.TotalAmount() != 2015 {
		t.Fatalf("round trip: amount=%d fee=%d total=%d", got.Amount, got.FeeAmount, got.TotalAmount())
	}
	rows, err := repo.ListAdmin(ctx, "", "", nil, nil, nil, "", 10, 0)
	if err != nil || len(rows) != 1 || rows[0].Payment.FeeAmount != 15 {
		t.Fatalf("ListAdmin must carry fee_amount: err=%v rows=%v", err, rows)
	}
}

// insertScannableMerchant is insertConstraintMerchant plus a non-NULL phone:
// merchants.phone is nullable but merchant.Merchant.Phone is a plain string,
// so a NULL phone cannot be scanned by the repository (a pre-existing quirk
// unrelated to fees; real merchants are created with phone ”).
func insertScannableMerchant(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	m := insertConstraintMerchant(t, db)
	if _, err := db.Exec(`UPDATE merchants SET phone = '' WHERE id = $1`, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMerchantRepository_FeeBearer(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewMerchantRepository(db)
	m := insertScannableMerchant(t, db)

	got, err := repo.GetByID(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.FeeBearer != merchant.FeeBearerMerchant {
		t.Fatalf("default fee bearer = %q", got.FeeBearer)
	}
	if err := repo.SetFeeBearer(ctx, m, merchant.FeeBearerCustomer); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, m)
	if got.FeeBearer != merchant.FeeBearerCustomer {
		t.Fatalf("fee bearer after set = %q", got.FeeBearer)
	}
	if err := repo.SetFeeBearer(ctx, uuid.New(), merchant.FeeBearerCustomer); err != merchant.ErrMerchantNotFound {
		t.Fatalf("unknown merchant: want ErrMerchantNotFound, got %v", err)
	}
}

// GetByEmail and List/ListAdmin are separate SELECTs from GetByID; each must
// carry fee_bearer or the admin UI and auth paths silently see "".
func TestMerchantRepository_FeeBearer_AllReadPaths(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewMerchantRepository(db)
	m := insertScannableMerchant(t, db)
	if err := repo.SetFeeBearer(ctx, m, merchant.FeeBearerCustomer); err != nil {
		t.Fatal(err)
	}
	var email string
	if err := db.QueryRow(`SELECT email FROM merchants WHERE id = $1`, m).Scan(&email); err != nil {
		t.Fatal(err)
	}
	byEmail, err := repo.GetByEmail(ctx, email)
	if err != nil || byEmail.FeeBearer != merchant.FeeBearerCustomer {
		t.Fatalf("GetByEmail fee bearer = %v (err %v)", byEmail, err)
	}
	list, err := repo.List(ctx, 10, 0)
	if err != nil || len(list) != 1 || list[0].FeeBearer != merchant.FeeBearerCustomer {
		t.Fatalf("List fee bearer: err=%v list=%v", err, list)
	}
}

func TestMigration020_IsIdempotent(t *testing.T) {
	_, db := newPGRepo(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "020_add_customer_borne_fee.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("re-applying migration 020 (run %d): %v", i+1, err)
		}
	}
	m := insertConstraintMerchant(t, db)
	if _, err := db.Exec(`UPDATE merchants SET fee_bearer = 'platform' WHERE id = $1`, m); err == nil {
		t.Error("constraint must still reject an invalid fee_bearer after re-applying")
	}
}
