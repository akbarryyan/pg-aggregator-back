package service

import (
	"context"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

// Split out of admin_service_test.go when AdminPaymentService was extracted
// from the AdminService god-object (project backlog item #9). These were
// characterization tests against the old AdminService.ListPayments/
// ExportPayments/GetPayment before the move; assertions are unchanged since
// the method bodies moved verbatim — only the receiver type changed.

func newAdminPaymentServiceWithMock(t *testing.T) (*AdminPaymentService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := NewAdminPaymentService(
		repository.NewPaymentRepository(db),
		repository.NewMerchantRepository(db),
	)
	return svc, mock
}

var adminPaymentCols = []string{
	"id", "reference", "merchant_id", "amount", "currency", "payment_method",
	"provider_name", "provider_reference", "status", "description",
	"customer_name", "customer_email", "qris_data", "callback_url",
	"environment", "expires_at", "paid_at", "created_at", "updated_at",
	"merchant_name",
}

func TestAdminPaymentService_ListPayments(t *testing.T) {
	svc, mock := newAdminPaymentServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols).AddRow(
			uuid.New(), "PAY-1", uuid.New(), int64(10000), "IDR", "qris",
			"cashi", nil, "pending", "desc", nil, nil, nil, nil,
			"production", now, nil, now, now, "Acme",
		))
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := svc.ListPayments(context.Background(), "", "", nil, nil, nil, "", 20, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 || result.Total != 1 {
		t.Fatalf("expected 1 item/total, got items=%d total=%d", len(result.Items), result.Total)
	}
	if result.Items[0].MerchantName != "Acme" {
		t.Errorf("expected merchant name Acme, got %q", result.Items[0].MerchantName)
	}
}

func TestAdminPaymentService_ExportPayments(t *testing.T) {
	svc, mock := newAdminPaymentServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols).AddRow(
			uuid.New(), "PAY-1", uuid.New(), int64(10000), "IDR", "qris",
			"cashi", nil, "paid", "desc", nil, nil, nil, nil,
			"production", now, &now, now, now, "Acme",
		))

	items, err := svc.ExportPayments(context.Background(), "", "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestAdminPaymentService_GetPayment(t *testing.T) {
	svc, mock := newAdminPaymentServiceWithMock(t)
	now := time.Now()
	paymentID := uuid.New()
	merchantID := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(paymentID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "reference", "merchant_id", "amount", "currency", "payment_method",
			"provider_name", "provider_reference", "status", "description",
			"customer_name", "customer_email", "qris_data", "callback_url",
			"environment", "payment_link_id", "expires_at", "paid_at", "created_at", "updated_at",
		}).AddRow(
			paymentID, "PAY-1", merchantID, int64(10000), "IDR", "qris",
			"cashi", nil, "pending", "desc", nil, nil, nil, nil,
			"production", nil, now, nil, now, now,
		))
	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).
		WithArgs(merchantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "created_at", "updated_at",
		}).AddRow(merchantID, "Acme Inc", "a@example.com", "", "Acme Business", nil, nil, true, now, now))

	detail, err := svc.GetPayment(context.Background(), paymentID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.MerchantName != "Acme Business" {
		t.Errorf("expected merchant name from business_name, got %q", detail.MerchantName)
	}
}
