package service

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

func newAdminNotificationServiceWithMock(t *testing.T) (*AdminNotificationService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := NewAdminNotificationService(
		repository.NewPaymentRepository(db),
		repository.NewWebhookEventRepository(db),
	).WithCallbackRepo(repository.NewMerchantCallbackRepository(db))
	return svc, mock
}

func TestAdminNotificationService_ListNotifications(t *testing.T) {
	svc, mock := newAdminNotificationServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM webhook_events\s+WHERE 1=1`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "payment_id", "provider_name", "provider_reference", "event_type", "status", "processed_at", "is_processed", "processing_error", "created_at",
		}))
	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols))
	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols).AddRow(
			uuid.New(), "PAY-1", uuid.New(), int64(1000), "IDR", "qris",
			"cashi", nil, "expired", "d", nil, nil, nil, nil,
			"production", now, nil, now, now, "Acme",
		))

	result, err := svc.ListNotifications(context.Background(), 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 notification, got %d", result.Total)
	}
}

func TestAdminNotificationService_ListMerchantNotifications(t *testing.T) {
	svc, mock := newAdminNotificationServiceWithMock(t)
	now := time.Now()
	merchantID := uuid.New()

	mock.ExpectQuery(`SELECT[\s\S]*FROM merchant_callback_deliveries`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "payment_id", "merchant_id", "event_type", "target_url", "request_payload",
			"attempt_number", "status", "http_status", "response_body", "error_message",
			"delivered_at", "next_retry_at", "created_at", "updated_at", "merchant_name", "payment_reference",
		}))
	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols))
	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols).AddRow(
			uuid.New(), "PAY-1", merchantID, int64(1000), "IDR", "qris",
			"cashi", nil, "failed", "d", nil, nil, nil, nil,
			"production", now, nil, now, now, "Acme",
		))

	result, err := svc.ListMerchantNotifications(context.Background(), merchantID, "production", 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 notification, got %d", result.Total)
	}
}
