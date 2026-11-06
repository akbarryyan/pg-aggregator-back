package handler

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
)

var exportPaymentCols = []string{
	"id", "reference", "merchant_id", "amount", "currency", "payment_method",
	"provider_name", "provider_reference", "status", "description",
	"customer_name", "customer_email", "qris_data", "callback_url",
	"environment", "expires_at", "paid_at", "created_at", "updated_at",
	"fee_amount", "merchant_net_amount", "merchant_name",
}

func exportService(t *testing.T) (*service.AdminPaymentService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := service.NewAdminPaymentService(repository.NewPaymentRepository(db), repository.NewMerchantRepository(db))
	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(exportPaymentCols).AddRow(
			uuid.New(), "PAY-CSV", uuid.New(), int64(2000), "IDR", "qris",
			"cashi", nil, "paid", "desc", nil, nil, nil, nil,
			"production", time.Now(), nil, time.Now(), time.Now(), int64(15), int64(0), "Acme",
		))
	return svc, mock
}

func parseCSV(t *testing.T, rec *httptest.ResponseRecorder) (header, row []string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	recs, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("csv parse: err=%v records=%d", err, len(recs))
	}
	return recs[0], recs[1]
}

func assertTrailingFeeColumns(t *testing.T, header, row []string, lastExisting string) {
	t.Helper()
	n := len(header)
	if len(row) != n {
		t.Fatalf("row has %d cells, header %d", len(row), n)
	}
	if header[n-3] != lastExisting {
		t.Errorf("existing columns shifted: header[%d] = %q, want %q", n-3, header[n-3], lastExisting)
	}
	if header[n-2] != "fee_amount" || header[n-1] != "total_amount" {
		t.Errorf("last two header columns = %q,%q, want fee_amount,total_amount", header[n-2], header[n-1])
	}
	if row[n-2] != "15" || row[n-1] != "2015" {
		t.Errorf("last two cells = %q,%q, want 15,2015", row[n-2], row[n-1])
	}
	// The nominal amount column keeps its original meaning.
	for i, h := range header {
		if h == "amount" && row[i] != "2000" {
			t.Errorf("amount cell = %q, want nominal 2000", row[i])
		}
	}
}

func TestMerchantHandler_ExportPayments_FeeColumnsAreLast(t *testing.T) {
	svc, _ := exportService(t)
	h := NewMerchantHandler(nil, nil, nil, "http://localhost:3000").WithMerchantAndPaymentServices(svc, nil)

	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, uuid.New())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/merchant/payments/export", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ExportPayments(rec, req)

	header, row := parseCSV(t, rec)
	if header[0] != "id" || header[2] != "amount" {
		t.Errorf("leading columns moved: %v", header[:3])
	}
	assertTrailingFeeColumns(t, header, row, "created_at")
}

func TestAdminHandler_ExportPayments_FeeColumnsAreLast(t *testing.T) {
	svc, _ := exportService(t)
	h := NewAdminHandler(nil, "http://localhost:3000").WithMerchantAndPaymentServices(svc, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/payments/export", nil)
	rec := httptest.NewRecorder()
	h.ExportPayments(rec, req)

	header, row := parseCSV(t, rec)
	if header[0] != "id" || header[4] != "amount" {
		t.Errorf("leading columns moved: %v", header[:5])
	}
	assertTrailingFeeColumns(t, header, row, "updated_at")
}

func TestWithTotalAmount_PaymentLinkListCarriesTotal(t *testing.T) {
	items := withTotalAmount([]*payment.Payment{
		{ID: uuid.New(), Amount: 2000, FeeAmount: 15},
		{ID: uuid.New(), Amount: 500},
	})
	if len(items) != 2 || items[0].TotalAmount != 2015 || items[1].TotalAmount != 500 {
		t.Fatalf("unexpected totals: %+v", items)
	}
}
