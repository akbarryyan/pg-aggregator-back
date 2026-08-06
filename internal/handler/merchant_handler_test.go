package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

// TestMerchantHandler_GetDashboardSummary_SingleQuery covers the project
// backlog item #6 fix: this endpoint used to issue 6 ListPayments calls (12
// SQL round-trips, plus a latent bug capping paid_amount at the 100 most
// recent paid payments). It now issues exactly one StatusBreakdownFiltered
// query. sqlmock's strict expectation ordering means this test fails if the
// old N+1 pattern — or the amount-cap bug — is reintroduced.
func TestMerchantHandler_GetDashboardSummary_SingleQuery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	paymentRepo := repository.NewPaymentRepository(db)
	merchantRepo := repository.NewMerchantRepository(db)
	adminPaymentService := service.NewAdminPaymentService(paymentRepo, merchantRepo)

	h := NewMerchantHandler(nil, nil, nil, "http://localhost:3000").
		WithMerchantAndPaymentServices(adminPaymentService, nil)

	merchantID := uuid.New()

	// 150 paid payments totalling more than the old 100-row cap could see —
	// proves paid_amount is no longer silently truncated.
	mock.ExpectQuery(`SELECT[\s\S]*FROM payments\s+WHERE 1=1`).
		WillReturnRows(sqlmock.NewRows([]string{
			"total", "paid", "pending", "failed", "expired", "cancelled", "paid_amount",
		}).AddRow(200, 150, 30, 10, 8, 2, int64(999_000_000)))

	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, merchantID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/merchant/dashboard/summary?environment=production", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.GetDashboardSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet/unexpected SQL expectations — handler issued more queries than expected: %v", err)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if int(resp["paid_amount"].(float64)) != 999_000_000 {
		t.Errorf("expected paid_amount=999000000 (uncapped), got %v", resp["paid_amount"])
	}
	if int(resp["total_payments"].(float64)) != 200 {
		t.Errorf("expected total_payments=200, got %v", resp["total_payments"])
	}
}

func TestMerchantHandler_GetDashboardSummary_RequiresMerchantContext(t *testing.T) {
	h := NewMerchantHandler(nil, nil, nil, "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/merchant/dashboard/summary", nil)
	rec := httptest.NewRecorder()

	h.GetDashboardSummary(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without merchant context, got %d", rec.Code)
	}
}
