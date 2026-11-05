package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
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
	"fee_amount",
}

func paymentRow(p *payment.Payment) []driver.Value {
	return []driver.Value{
		p.ID, p.Reference, p.MerchantID, p.Amount, p.Currency, p.PaymentMethod,
		p.ProviderName, p.ProviderReference, p.Status, p.Description,
		p.CustomerName, p.CustomerEmail, p.QRISData, p.CallbackURL,
		p.Environment, p.PaymentLinkID, p.ExpiresAt, p.PaidAt, p.CreatedAt, p.UpdatedAt,
		p.FeeAmount,
	}
}

func samplePayment() *payment.Payment {
	now := time.Now()
	return &payment.Payment{
		ID:            uuid.New(),
		Reference:     "PAY-1",
		MerchantID:    uuid.New(),
		Amount:        50000,
		FeeAmount:     350,
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
			p.FeeAmount,
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
			p.FeeAmount,
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

const updateStatusSQL = `(?s)UPDATE payments p SET status = \$1, paid_at = \$2, updated_at = \$3.*FROM \(SELECT id, status FROM payments WHERE id = \$4 FOR UPDATE\) prev.*WHERE p.id = prev.id AND p.status IN \(%s\).*RETURNING prev.status`

func TestPaymentRepository_UpdateStatus_PaidFromPendingOrExpired(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	id := uuid.New()
	paidAt := time.Now()

	mock.ExpectQuery(fmt.Sprintf(updateStatusSQL, `\$5, \$6`)).
		WithArgs(payment.StatusPaid, &paidAt, sqlmock.AnyArg(), id, payment.StatusPending, payment.StatusExpired).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(payment.StatusExpired))

	prev, err := repo.UpdateStatus(context.Background(), id, payment.StatusPaid, &paidAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prev != payment.StatusExpired {
		t.Errorf("previous status = %q, want expired", prev)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPaymentRepository_UpdateStatus_ExpireOnlyFromPending(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	id := uuid.New()

	mock.ExpectQuery(fmt.Sprintf(updateStatusSQL, `\$5`)).
		WithArgs(payment.StatusExpired, sqlmock.AnyArg(), sqlmock.AnyArg(), id, payment.StatusPending).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(payment.StatusPending))

	if _, err := repo.UpdateStatus(context.Background(), id, payment.StatusExpired, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// No row matched the guarded UPDATE: someone else moved the payment first
// (or it was never eligible). The repository re-reads it to explain why.
func TestPaymentRepository_UpdateStatus_NoRowsReportsTerminal(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p := samplePayment()
	p.Status = payment.StatusPaid

	mock.ExpectQuery(fmt.Sprintf(updateStatusSQL, `\$5`)).
		WithArgs(payment.StatusExpired, sqlmock.AnyArg(), sqlmock.AnyArg(), p.ID, payment.StatusPending).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(p.ID).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(p)...))

	_, err := repo.UpdateStatus(context.Background(), p.ID, payment.StatusExpired, nil)
	if !errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
		t.Fatalf("expected ErrPaymentAlreadyTerminal, got %v", err)
	}
}

func TestPaymentRepository_UpdateStatus_NoRowsPaymentNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	id := uuid.New()

	mock.ExpectQuery(fmt.Sprintf(updateStatusSQL, `\$5, \$6`)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).WithArgs(id).WillReturnError(sql.ErrNoRows)

	_, err := repo.UpdateStatus(context.Background(), id, payment.StatusPaid, nil)
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("expected ErrPaymentNotFound, got %v", err)
	}
}

// A target no status may move to (e.g. back to pending) is refused before
// any SQL is issued.
func TestPaymentRepository_UpdateStatus_InvalidTargetIssuesNoSQL(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)

	_, err := repo.UpdateStatus(context.Background(), uuid.New(), payment.StatusPending, nil)
	if !errors.Is(err, payment.ErrInvalidStatusTransition) {
		t.Fatalf("expected ErrInvalidStatusTransition, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected SQL issued: %v", err)
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

func TestPaymentRepository_ListReconcileCandidates(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	p := samplePayment()

	// Pending rows are windowed by created_at, expired ones by expires_at,
	// each with its own window.
	mock.ExpectQuery(`(?s)FROM payments p.*p.environment = \$1.*COALESCE\(p.provider_reference, ''\) <> ''.*p.status = \$2 AND p.created_at >= \$3.*p.status = \$4 AND p.expires_at >= \$5.*NOT EXISTS.*webhook_events w.*w.processing_error LIKE 'amount_mismatch%'.*ORDER BY p.created_at DESC, p.id DESC\s+LIMIT \$6`).
		WithArgs(payment.EnvironmentProduction, payment.StatusPending, now.Add(-24*time.Hour), payment.StatusExpired, now.Add(-6*time.Hour), 50).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(p)...))

	got, err := repo.ListReconcileCandidates(context.Background(), now, 24*time.Hour, 6*time.Hour, 50, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != p.ID {
		t.Fatalf("expected the one candidate, got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPaymentRepository_ListReconcileCandidates_WithCursor(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	p := samplePayment()
	cursor := &payment.ReconcileCursor{CreatedAt: now.Add(-time.Hour), ID: uuid.New()}

	mock.ExpectQuery(`(?s)FROM payments p.*NOT EXISTS.*\(p.created_at, p.id\) < \(\$7, \$8\).*ORDER BY p.created_at DESC, p.id DESC\s+LIMIT \$6`).
		WithArgs(payment.EnvironmentProduction, payment.StatusPending, now.Add(-24*time.Hour), payment.StatusExpired, now.Add(-6*time.Hour), 2, cursor.CreatedAt, cursor.ID).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(p)...))

	got, err := repo.ListReconcileCandidates(context.Background(), now, 24*time.Hour, 6*time.Hour, 2, cursor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != p.ID {
		t.Fatalf("expected the one candidate, got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A row error mid-iteration must surface, not come back as a short (or
// empty) list the expire job would silently accept.
func TestPaymentRepository_ListExpiredPending_RowError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	before := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`(?s)FROM payments\s+WHERE status = \$1 AND expires_at < \$2\s+ORDER BY expires_at ASC\s+LIMIT \$3`).
		WithArgs(payment.StatusPending, before, 200).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(samplePayment())...).RowError(0, errors.New("connection reset")))

	if _, err := repo.ListExpiredPending(context.Background(), before, 200); err == nil {
		t.Fatal("expected the row error to be returned")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
