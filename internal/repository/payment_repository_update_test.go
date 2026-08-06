package repository

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

var adminPaymentCols = []string{
	"id", "reference", "merchant_id", "amount", "currency", "payment_method",
	"provider_name", "provider_reference", "status", "description",
	"customer_name", "customer_email", "qris_data", "callback_url",
	"environment", "expires_at", "paid_at", "created_at", "updated_at",
	"merchant_name",
}

// TestPaymentRepository_ListAdmin_NoFilters verifies the base query (no
// optional WHERE clauses appended) runs with just limit/offset as $1/$2.
func TestPaymentRepository_ListAdmin_NoFilters(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)

	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p[\s\S]*LEFT JOIN merchants m[\s\S]*WHERE 1=1[\s\S]*ORDER BY p\.created_at DESC LIMIT \$1 OFFSET \$2`).
		WithArgs(20, 0).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols))

	_, err := repo.ListAdmin(context.Background(), "", "", nil, nil, nil, "", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPaymentRepository_ListAdmin_AllFiltersArgOrder pins down the argument
// ordering ($1..$N) that the fmt.Sprintf-based query builder produces when
// every optional filter is active — this is exactly the kind of bug (wrong
// $N for the wrong filter) that's invisible from reading the code casually
// but breaks admin search silently in production.
func TestPaymentRepository_ListAdmin_AllFiltersArgOrder(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)

	merchantID := uuid.New()
	dateFrom := time.Now().Add(-24 * time.Hour)
	dateTo := time.Now()

	mock.ExpectQuery(`AND p\.status = \$1[\s\S]*AND p\.merchant_id = \$2[\s\S]*AND \([\s\S]*\$3[\s\S]*AND p\.created_at >= \$4[\s\S]*AND p\.created_at < \$5[\s\S]*AND p\.environment = \$6[\s\S]*LIMIT \$7 OFFSET \$8`).
		WithArgs(
			payment.StatusPending,
			merchantID,
			"%acme%",
			dateFrom,
			dateTo,
			payment.EnvironmentSandbox,
			20,
			0,
		).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols))

	_, err := repo.ListAdmin(
		context.Background(),
		payment.StatusPending,
		"acme",
		&merchantID,
		&dateFrom, &dateTo,
		payment.EnvironmentSandbox,
		0, 0,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (filter/arg ordering mismatch): %v", err)
	}
}

func TestPaymentRepository_ListAdmin_DefaultsLimitAndOffset(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)

	// limit<=0 → defaults to 20; offset<0 → clamped to 0 (see ListAdmin).
	mock.ExpectQuery(`LIMIT \$1 OFFSET \$2`).
		WithArgs(20, 0).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols))

	_, err := repo.ListAdmin(context.Background(), "", "", nil, nil, nil, "", -5, -5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPaymentRepository_ListAdmin_ScansMerchantName(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p := samplePayment()

	rows := sqlmock.NewRows(adminPaymentCols).AddRow(
		p.ID, p.Reference, p.MerchantID, p.Amount, p.Currency, p.PaymentMethod,
		p.ProviderName, p.ProviderReference, p.Status, p.Description,
		p.CustomerName, p.CustomerEmail, p.QRISData, p.CallbackURL,
		p.Environment, p.ExpiresAt, p.PaidAt, p.CreatedAt, p.UpdatedAt,
		"Acme Business",
	)
	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).WillReturnRows(rows)

	got, err := repo.ListAdmin(context.Background(), "", "", nil, nil, nil, "", 20, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].MerchantName != "Acme Business" {
		t.Errorf("expected merchant name 'Acme Business', got %q", got[0].MerchantName)
	}
}

func TestPaymentRepository_CountByStatus(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	merchantID := uuid.New()

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE merchant_id = \$1 AND status = \$2`).
		WithArgs(merchantID, payment.StatusPaid).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))

	count, err := repo.CountByStatus(context.Background(), merchantID, payment.StatusPaid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 7 {
		t.Errorf("expected 7, got %d", count)
	}
}

func TestPaymentRepository_GetTotalAmount(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	merchantID := uuid.New()

	mock.ExpectQuery(`SELECT COALESCE\(SUM\(amount\), 0\) FROM payments WHERE merchant_id = \$1 AND status = \$2`).
		WithArgs(merchantID, payment.StatusPaid).
		WillReturnRows(sqlmock.NewRows([]string{"total"}).AddRow(int64(150000)))

	total, err := repo.GetTotalAmount(context.Background(), merchantID, payment.StatusPaid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 150000 {
		t.Errorf("expected 150000, got %d", total)
	}
}

var statusBreakdownCols = []string{"total", "paid", "pending", "failed", "expired", "cancelled", "paid_amount"}

func TestPaymentRepository_StatusBreakdownFiltered_NoFilters(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)

	mock.ExpectQuery(`SELECT[\s\S]*FROM payments\s+WHERE 1=1\s*$`).
		WillReturnRows(sqlmock.NewRows(statusBreakdownCols).AddRow(10, 4, 3, 1, 1, 1, int64(400000)))

	result, err := repo.StatusBreakdownFiltered(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 10 || result.Paid != 4 || result.PaidAmount != 400000 {
		t.Fatalf("unexpected breakdown: %+v", result)
	}
}

func TestPaymentRepository_StatusBreakdownFiltered_MerchantAndEnvironment(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	merchantID := uuid.New()

	mock.ExpectQuery(`AND merchant_id = \$1[\s\S]*AND environment = \$2`).
		WithArgs(merchantID, payment.EnvironmentSandbox).
		WillReturnRows(sqlmock.NewRows(statusBreakdownCols).AddRow(3, 1, 1, 0, 1, 0, int64(50000)))

	result, err := repo.StatusBreakdownFiltered(context.Background(), &merchantID, payment.EnvironmentSandbox)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 3 || result.Pending != 1 || result.Expired != 1 {
		t.Fatalf("unexpected breakdown: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (arg ordering mismatch): %v", err)
	}
}
