package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

var paymentCols = []string{
	"id", "reference", "merchant_id", "amount", "currency", "payment_method",
	"provider_name", "provider_reference", "status", "description",
	"customer_name", "customer_email", "qris_data", "callback_url",
	"environment", "payment_link_id", "expires_at", "paid_at", "created_at", "updated_at",
}

func paymentRow(p *payment.Payment) []driver.Value {
	return []driver.Value{
		p.ID, p.Reference, p.MerchantID, p.Amount, p.Currency, p.PaymentMethod,
		p.ProviderName, p.ProviderReference, p.Status, p.Description,
		p.CustomerName, p.CustomerEmail, p.QRISData, p.CallbackURL,
		p.Environment, p.PaymentLinkID, p.ExpiresAt, p.PaidAt, p.CreatedAt, p.UpdatedAt,
	}
}

func samplePayment() *payment.Payment {
	now := time.Now()
	return &payment.Payment{
		ID:            uuid.New(),
		Reference:     "PAY-1",
		MerchantID:    uuid.New(),
		Amount:        50000,
		Currency:      payment.CurrencyIDR,
		PaymentMethod: payment.PaymentMethodQRIS,
		Status:        payment.StatusPending,
		Description:   "Test",
		Environment:   payment.EnvironmentSandbox,
		ExpiresAt:     now.Add(time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func TestPaymentRepository_Create(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p := samplePayment()

	mock.ExpectQuery(`INSERT INTO payments`).
		WithArgs(
			p.ID, p.Reference, p.MerchantID, p.Amount, p.Currency, p.PaymentMethod,
			p.ProviderName, p.ProviderReference, p.Status, p.Description,
			p.CustomerName, p.CustomerEmail, p.QRISData, p.CallbackURL,
			p.Environment, p.PaymentLinkID, p.ExpiresAt, p.CreatedAt, p.UpdatedAt,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(p.ID, p.CreatedAt, p.UpdatedAt))

	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPaymentRepository_Create_DefaultsEnvironmentToProduction(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p := samplePayment()
	p.Environment = "" // repository must default this before INSERT

	mock.ExpectQuery(`INSERT INTO payments`).
		WithArgs(
			p.ID, p.Reference, p.MerchantID, p.Amount, p.Currency, p.PaymentMethod,
			p.ProviderName, p.ProviderReference, p.Status, p.Description,
			p.CustomerName, p.CustomerEmail, p.QRISData, p.CallbackURL,
			payment.EnvironmentProduction, p.PaymentLinkID, p.ExpiresAt, p.CreatedAt, p.UpdatedAt,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(p.ID, p.CreatedAt, p.UpdatedAt))

	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Environment != payment.EnvironmentProduction {
		t.Errorf("expected environment defaulted to production, got %q", p.Environment)
	}
}

func TestPaymentRepository_GetByID_Found(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p := samplePayment()

	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(p.ID).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(p)...))

	got, err := repo.GetByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Reference != p.Reference {
		t.Errorf("expected reference %s, got %s", p.Reference, got.Reference)
	}
}

func TestPaymentRepository_GetByID_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	id := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)

	_, err := repo.GetByID(context.Background(), id)
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("expected ErrPaymentNotFound, got %v", err)
	}
}

func TestPaymentRepository_GetByReference_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)

	mock.ExpectQuery(`SELECT .* FROM payments WHERE reference = \$1`).
		WithArgs("does-not-exist").
		WillReturnError(sql.ErrNoRows)

	_, err := repo.GetByReference(context.Background(), "does-not-exist")
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("expected ErrPaymentNotFound, got %v", err)
	}
}

func TestPaymentRepository_GetByProviderReference_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)

	mock.ExpectQuery(`SELECT .* FROM payments WHERE provider_reference = \$1`).
		WithArgs("does-not-exist").
		WillReturnError(sql.ErrNoRows)

	_, err := repo.GetByProviderReference(context.Background(), "does-not-exist")
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("expected ErrPaymentNotFound, got %v", err)
	}
}

func TestPaymentRepository_List(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p1, p2 := samplePayment(), samplePayment()

	rows := sqlmock.NewRows(paymentCols).AddRow(paymentRow(p1)...).AddRow(paymentRow(p2)...)
	mock.ExpectQuery(`SELECT .* FROM payments ORDER BY created_at DESC LIMIT \$1 OFFSET \$2`).
		WithArgs(10, 0).
		WillReturnRows(rows)

	got, err := repo.List(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 payments, got %d", len(got))
	}
}

// TestPaymentRepository_UpdateStatus_RejectsInvalidTransition verifies the
// defense-in-depth check at the repository layer: even if a caller (or a
// future bug in the service layer) tries an illegal transition like
// paid → pending, the repository refuses it and issues no UPDATE at all.
func TestPaymentRepository_UpdateStatus_RejectsInvalidTransition(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p := samplePayment()
	p.Status = payment.StatusPaid // terminal — no transition is legal from here

	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(p.ID).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(p)...))

	err := repo.UpdateStatus(context.Background(), p.ID, payment.StatusPending, nil)
	if !errors.Is(err, payment.ErrInvalidStatusTransition) {
		t.Fatalf("expected ErrInvalidStatusTransition, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (an UPDATE must not have been issued): %v", err)
	}
}

func TestPaymentRepository_UpdateStatus_ValidTransition(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p := samplePayment()
	p.Status = payment.StatusPending

	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(p.ID).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(p)...))
	mock.ExpectExec(`UPDATE payments`).
		WithArgs(payment.StatusPaid, sqlmock.AnyArg(), sqlmock.AnyArg(), p.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateStatus(context.Background(), p.ID, payment.StatusPaid, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPaymentRepository_UpdateStatus_PaymentNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	id := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)

	err := repo.UpdateStatus(context.Background(), id, payment.StatusPaid, nil)
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("expected ErrPaymentNotFound, got %v", err)
	}
}

func TestPaymentRepository_CountByPaymentLinkID(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	linkID := uuid.New()

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE payment_link_id = \$1`).
		WithArgs(linkID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	count, err := repo.CountByPaymentLinkID(context.Background(), linkID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}
