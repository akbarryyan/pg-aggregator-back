package service

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

func newAdminLogServiceWithMock(t *testing.T) (*AdminLogService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := NewAdminLogService(
		repository.NewPaymentRepository(db),
		repository.NewWebhookEventRepository(db),
	)
	return svc, mock
}

func TestAdminLogService_ListPaymentEvents(t *testing.T) {
	svc, mock := newAdminLogServiceWithMock(t)
	now := time.Now()
	paymentID := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(paymentID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "reference", "merchant_id", "amount", "currency", "payment_method",
			"provider_name", "provider_reference", "status", "description",
			"customer_name", "customer_email", "qris_data", "callback_url",
			"environment", "payment_link_id", "expires_at", "paid_at", "created_at", "updated_at",
		}).AddRow(paymentID, "PAY-1", uuid.New(), int64(1000), "IDR", "qris", "cashi", nil, "pending", "d", nil, nil, nil, nil, "production", nil, now, nil, now, now))
	mock.ExpectQuery(`SELECT[\s\S]*FROM webhook_events WHERE payment_id = \$1`).
		WithArgs(paymentID, 100).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "payment_id", "provider_name", "provider_reference", "event_type", "status", "processed_at", "is_processed", "processing_error", "created_at",
		}).AddRow(uuid.New(), paymentID, "cashi", "REF-1", "payment.paid", "processed", now, true, nil, now))

	events, err := svc.ListPaymentEvents(context.Background(), paymentID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestAdminLogService_GetLog_RedactsSecrets(t *testing.T) {
	svc, mock := newAdminLogServiceWithMock(t)
	now := time.Now()
	id := uuid.New()

	rows := sqlmock.NewRows([]string{
		"id", "payment_id", "provider_name", "provider_reference", "event_type", "status", "raw_payload", "processed_at", "is_processed", "processing_error", "created_at",
	}).AddRow(id, nil, "cashi", "REF-1", "payment.paid", "processed", []byte(`{"api_key":"secret123","amount":1000}`), now, true, nil, now)
	mock.ExpectQuery(`SELECT[\s\S]*FROM webhook_events\s+WHERE id = \$1`).WithArgs(id).WillReturnRows(rows)

	detail, err := svc.GetLog(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.RawPayload["api_key"] != "[redacted]" {
		t.Errorf("expected api_key to be redacted, got %v", detail.RawPayload["api_key"])
	}
	if detail.RawPayload["amount"] != float64(1000) {
		t.Errorf("expected amount to pass through, got %v", detail.RawPayload["amount"])
	}
}

func TestAdminLogService_ListLogs(t *testing.T) {
	svc, mock := newAdminLogServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM webhook_events\s+WHERE 1=1`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "payment_id", "provider_name", "provider_reference", "event_type", "status", "processed_at", "is_processed", "processing_error", "created_at",
		}).AddRow(uuid.New(), nil, "cashi", "REF-1", "payment.paid", "processed", now, true, nil, now))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM webhook_events WHERE 1=1`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := svc.ListLogs(context.Background(), "", "", "", 50, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestAdminLogService_ExportLogs(t *testing.T) {
	svc, mock := newAdminLogServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM webhook_events\s+WHERE 1=1`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "payment_id", "provider_name", "provider_reference", "event_type", "status", "processed_at", "is_processed", "processing_error", "created_at",
		}).AddRow(uuid.New(), nil, "cashi", "REF-1", "payment.paid", "processed", now, true, nil, now))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM webhook_events WHERE 1=1`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	items, err := svc.ExportLogs(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}
