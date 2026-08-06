package service

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// Split out of admin_service_test.go when AdminDashboardService was
// extracted from the AdminService god-object — the last increment of
// project backlog item #9 (AdminService itself is retired after this
// split). Assertions unchanged since method bodies moved verbatim.

func newAdminDashboardServiceWithMock(t *testing.T) (*AdminDashboardService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	paymentRepo := repository.NewPaymentRepository(db)
	merchantRepo := repository.NewMerchantRepository(db)
	adminPaymentService := NewAdminPaymentService(paymentRepo, merchantRepo)
	svc := NewAdminDashboardService(merchantRepo, paymentRepo, repository.NewWebhookEventRepository(db), adminPaymentService)
	return svc, mock
}

func TestAdminDashboardService_GetDashboardSummary(t *testing.T) {
	svc, mock := newAdminDashboardServiceWithMock(t)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments$`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(100))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE status = \$1`).WithArgs(payment.StatusPending).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(10))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE status = \$1`).WithArgs(payment.StatusPaid).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(70))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE status = \$1`).WithArgs(payment.StatusExpired).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(15))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE status = \$1`).WithArgs(payment.StatusFailed).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchants`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(20))
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(amount\), 0\) FROM payments WHERE status = \$1`).WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(int64(500000)))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM webhook_events`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(200))

	summary, err := svc.GetDashboardSummary(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.TotalPayments != 100 || summary.TotalMerchants != 20 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestAdminDashboardService_GetDashboardCharts(t *testing.T) {
	svc, mock := newAdminDashboardServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*DATE\(created_at\)[\s\S]*FROM payments`).
		WillReturnRows(sqlmock.NewRows([]string{
			"day", "total", "paid", "pending", "failed", "expired", "cancelled", "paid_amount",
		}).AddRow(now, 3, 1, 1, 0, 1, 0, int64(10000)))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments$`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE status = \$1`).WithArgs(payment.StatusPending).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE status = \$1`).WithArgs(payment.StatusPaid).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE status = \$1`).WithArgs(payment.StatusExpired).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM payments WHERE status = \$1`).WithArgs(payment.StatusFailed).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchants`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(amount\), 0\) FROM payments WHERE status = \$1`).WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(int64(10000)))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM webhook_events`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	charts, err := svc.GetDashboardCharts(context.Background(), 14)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if charts.Days != 14 || len(charts.Daily) != 14 {
		t.Fatalf("expected 14 daily points, got %+v", charts)
	}
}

// TestAdminDashboardService_MerchantDailyStats_SingleAggregateQuery covers
// project backlog item #6: this used to issue 4 separate ListPayments
// calls (8 SQL round-trips) to build the status breakdown; it now issues
// exactly one StatusBreakdownFiltered query. sqlmock's strict expectation
// ordering means this test fails if the old N+1 pattern is reintroduced.
func TestAdminDashboardService_MerchantDailyStats_SingleAggregateQuery(t *testing.T) {
	svc, mock := newAdminDashboardServiceWithMock(t)
	merchantID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*DATE\(created_at\)[\s\S]*FROM payments`).
		WillReturnRows(sqlmock.NewRows([]string{
			"day", "total", "paid", "pending", "failed", "expired", "cancelled", "paid_amount",
		}).AddRow(now, 5, 2, 1, 1, 1, 0, int64(200000)))

	mock.ExpectQuery(`SELECT[\s\S]*FROM payments\s+WHERE 1=1`).
		WillReturnRows(sqlmock.NewRows([]string{
			"total", "paid", "pending", "failed", "expired", "cancelled", "paid_amount",
		}).AddRow(5, 2, 1, 1, 1, 0, int64(200000)))

	charts, err := svc.MerchantDailyStats(context.Background(), merchantID, payment.EnvironmentSandbox, 14)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet/unexpected SQL expectations — MerchantDailyStats issued more queries than expected: %v", err)
	}

	var paidCount, pendingCount int64
	for _, sp := range charts.StatusBreakdown {
		switch sp.Status {
		case "paid":
			paidCount = sp.Count
		case "pending":
			pendingCount = sp.Count
		}
	}
	if paidCount != 2 {
		t.Errorf("expected paid=2, got %d", paidCount)
	}
	if pendingCount != 1 {
		t.Errorf("expected pending=1, got %d", pendingCount)
	}
}
