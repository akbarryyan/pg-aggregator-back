package service

import (
	"context"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider/sandbox"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

// Split out of admin_service_test.go when AdminProviderService was
// extracted from the AdminService god-object (project backlog item #9).
// These were characterization tests against the old AdminService methods
// before the move; assertions are unchanged since the method bodies moved
// verbatim — only the receiver type changed.

func newAdminProviderServiceWithMock(t *testing.T) (*AdminProviderService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	router := providerPkg.NewProviderRouter()
	sb := sandbox.NewAdapter()
	router.RegisterProvider(sb)
	router.RegisterPaymentMethodProvider("qris", sb.GetName())

	svc := NewAdminProviderService(
		repository.NewPaymentRepository(db),
		repository.NewMerchantProviderConfigRepository(db),
		router,
	)
	return svc, mock
}

func TestAdminProviderService_ListProviders(t *testing.T) {
	svc, _ := newAdminProviderServiceWithMock(t)

	providers := svc.ListProviders()
	if len(providers) != 1 || providers[0].Name != "sandbox" {
		t.Fatalf("expected 1 provider named sandbox, got %+v", providers)
	}
}

func TestAdminProviderService_GetProviderDetail(t *testing.T) {
	svc, mock := newAdminProviderServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchant_provider_configs`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT[\s\S]*FROM merchant_provider_configs c`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "merchant_id", "provider_name", "payment_method", "priority", "weight",
			"failover_enabled", "is_enabled", "created_at", "updated_at", "merchant_name", "merchant_email",
		}).AddRow(uuid.New(), uuid.New(), "sandbox", "qris", 1, 100, true, true, now, now, "Acme", "a@example.com"))

	detail, err := svc.GetProviderDetail(context.Background(), "sandbox")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.MerchantCount != 1 || len(detail.MerchantRoutes) != 1 {
		t.Fatalf("expected 1 merchant count/route, got count=%d routes=%d", detail.MerchantCount, len(detail.MerchantRoutes))
	}
}

func TestAdminProviderService_GetProviderDetail_NotFound(t *testing.T) {
	svc, _ := newAdminProviderServiceWithMock(t)

	_, err := svc.GetProviderDetail(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// TestAdminProviderService_UpdateProviderHealth covers project backlog item
// #6 (N+1 audit finding #5): this used to call GetProviderDetail twice (4
// DB queries total) — once to validate the provider is registered, once to
// build the response — even though SetProviderHealth is an in-memory write
// that doesn't change what GetProviderDetail's DB queries return. The
// pre-check is now an in-memory lookup (findProvider), so only the final
// GetProviderDetail call touches the DB. Only 2 expectations are set here;
// mock.ExpectationsWereMet fails if the old double-fetch reappears.
func TestAdminProviderService_UpdateProviderHealth(t *testing.T) {
	svc, mock := newAdminProviderServiceWithMock(t)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchant_provider_configs`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT[\s\S]*FROM merchant_provider_configs c`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "merchant_id", "provider_name", "payment_method", "priority", "weight",
			"failover_enabled", "is_enabled", "created_at", "updated_at", "merchant_name", "merchant_email",
		}))

	detail, err := svc.UpdateProviderHealth(context.Background(), "sandbox", &provider.UpdateProviderHealthRequest{
		Status: provider.HealthStatusUnhealthy, Reason: "manual test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Provider.Name != "sandbox" {
		t.Fatalf("expected sandbox provider detail, got %+v", detail.Provider)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet/unexpected SQL expectations — UpdateProviderHealth issued more queries than expected: %v", err)
	}
}

// TestAdminProviderService_UpdateProviderHealth_UnknownProvider_NoDBQuery
// verifies the existence check happens in-memory: an unknown provider name
// must be rejected without ever touching the DB (no mock expectations set).
func TestAdminProviderService_UpdateProviderHealth_UnknownProvider_NoDBQuery(t *testing.T) {
	svc, mock := newAdminProviderServiceWithMock(t)

	_, err := svc.UpdateProviderHealth(context.Background(), "does-not-exist", &provider.UpdateProviderHealthRequest{
		Status: provider.HealthStatusUnhealthy,
	})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected zero DB queries for an unknown provider, got: %v", err)
	}
}

func TestAdminProviderService_ListRouting(t *testing.T) {
	svc, mock := newAdminProviderServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM merchant_provider_configs c`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "merchant_id", "provider_name", "payment_method", "priority", "weight",
			"failover_enabled", "is_enabled", "created_at", "updated_at", "merchant_name", "merchant_email",
		}).AddRow(uuid.New(), uuid.New(), "sandbox", "qris", 1, 100, true, true, now, now, "Acme", "a@example.com"))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchant_provider_configs`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := svc.ListRouting(context.Background(), 50, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 || result.Total != 1 {
		t.Fatalf("expected 1 item/total, got items=%d total=%d", len(result.Items), result.Total)
	}
}

func TestAdminProviderService_ListReconciliationCandidates(t *testing.T) {
	svc, mock := newAdminProviderServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols).AddRow(
			uuid.New(), "PAY-1", uuid.New(), int64(10000), "IDR", "qris",
			"", nil, payment.StatusPending, "desc", nil, nil, nil, nil,
			"production", now.Add(time.Hour), nil, now, now, "Acme",
		))

	result, err := svc.ListReconciliationCandidates(context.Background(), 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(result.Items))
	}
	if result.Items[0].CheckStatus != "missing_provider_ref" {
		t.Errorf("expected missing_provider_ref (no provider_reference set), got %s", result.Items[0].CheckStatus)
	}
}
