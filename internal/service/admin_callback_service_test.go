package service

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

func newAdminCallbackServiceWithMock(t *testing.T) (*AdminCallbackService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := NewAdminCallbackService(repository.NewMerchantCallbackRepository(db))
	return svc, mock
}

func TestAdminCallbackService_ListCallbacks(t *testing.T) {
	svc, mock := newAdminCallbackServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM merchant_callback_deliveries`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "payment_id", "merchant_id", "event_type", "target_url", "request_payload",
			"attempt_number", "status", "http_status", "response_body", "error_message",
			"delivered_at", "next_retry_at", "created_at", "updated_at", "merchant_name", "payment_reference",
		}).AddRow(uuid.New(), uuid.New(), uuid.New(), "payment.paid", "https://x", nil, 1, "success", nil, nil, nil, nil, nil, now, now, "Acme", "PAY-1"))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchant_callback_deliveries`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := svc.ListCallbacks(context.Background(), "", nil, 50, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 callback, got %d", result.Total)
	}
}

func TestAdminCallbackService_ListPaymentCallbacks(t *testing.T) {
	svc, mock := newAdminCallbackServiceWithMock(t)
	now := time.Now()
	paymentID := uuid.New()

	mock.ExpectQuery(`SELECT[\s\S]*FROM merchant_callback_deliveries\s+WHERE payment_id = \$1`).
		WithArgs(paymentID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "payment_id", "merchant_id", "event_type", "target_url", "request_payload",
			"attempt_number", "status", "http_status", "response_body", "error_message",
			"delivered_at", "next_retry_at", "created_at", "updated_at",
		}).AddRow(uuid.New(), paymentID, uuid.New(), "payment.paid", "https://x", nil, 1, "success", nil, nil, nil, nil, nil, now, now))

	items, err := svc.ListPaymentCallbacks(context.Background(), paymentID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}
