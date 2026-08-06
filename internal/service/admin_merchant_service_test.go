package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

// Split out of admin_service_test.go when AdminMerchantService was
// extracted from the AdminService god-object (project backlog item #9).
// These were characterization tests against the old AdminService methods
// before the move; assertions are unchanged since the method bodies moved
// verbatim — only the receiver type changed. ListMerchantPayments now
// composes AdminPaymentService rather than an internal self-call.

func newAdminMerchantServiceWithMock(t *testing.T) (*AdminMerchantService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	merchantRepo := repository.NewMerchantRepository(db)
	merchantProviderConfigRepo := repository.NewMerchantProviderConfigRepository(db)
	paymentService := NewAdminPaymentService(repository.NewPaymentRepository(db), merchantRepo)
	svc := NewAdminMerchantService(merchantRepo, merchantProviderConfigRepo, paymentService)
	return svc, mock
}

func TestAdminMerchantService_ListMerchants(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM merchants`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "is_active", "created_at", "updated_at",
		}).AddRow(uuid.New(), "Acme", "a@example.com", "", "Acme Biz", nil, true, now, now))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchants`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := svc.ListMerchants(context.Background(), "", "", 20, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 1 || result.Total != 1 {
		t.Fatalf("expected 1 item/total, got items=%d total=%d", len(result.Items), result.Total)
	}
}

func TestAdminMerchantService_CreateMerchant(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	req := &merchant.CreateMerchantRequest{
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme Biz",
	}

	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE email = \$1`).
		WithArgs(req.Email).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

	resp, err := svc.CreateMerchant(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Email != req.Email {
		t.Errorf("expected email %s, got %s", req.Email, resp.Email)
	}
}

func TestAdminMerchantService_CreateMerchant_DuplicateEmail(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	req := &merchant.CreateMerchantRequest{
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme Biz",
	}

	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE email = \$1`).
		WithArgs(req.Email).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "is_active", "created_at", "updated_at",
		}).AddRow(uuid.New(), "Acme", req.Email, "", "Acme Biz", nil, true, now, now))

	_, err := svc.CreateMerchant(context.Background(), req)
	if err != merchant.ErrMerchantAlreadyExists {
		t.Fatalf("expected ErrMerchantAlreadyExists, got %v", err)
	}
}

func TestAdminMerchantService_ExportMerchants(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM merchants`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "is_active", "created_at", "updated_at",
		}).AddRow(uuid.New(), "Acme", "a@example.com", "", "Acme Biz", nil, true, now, now))

	items, err := svc.ExportMerchants(context.Background(), "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestAdminMerchantService_GetMerchant(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	id := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "created_at", "updated_at",
		}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, now, now))

	resp, err := svc.GetMerchant(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != id {
		t.Errorf("expected id %s, got %s", id, resp.ID)
	}
}

func TestAdminMerchantService_UpdateMerchant(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	id := uuid.New()
	newName := "Acme Renamed"

	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "created_at", "updated_at",
		}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, now, now))
	mock.ExpectQuery(`UPDATE merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(now))

	resp, err := svc.UpdateMerchant(context.Background(), id, &merchant.UpdateMerchantRequest{Name: &newName})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Name != newName {
		t.Errorf("expected name %s, got %s", newName, resp.Name)
	}
}

// TestAdminMerchantService_SetMerchantActive covers project backlog item #6
// (N+1 audit finding #6): this used to call merchantRepo.GetByID twice —
// once as an existence check, once again after the UPDATE to build the
// response — even though the first fetch already had every field the
// response needs except is_active/updated_at, which the caller already
// knows. Only 2 expectations are set here (one SELECT, one UPDATE);
// mock.ExpectationsWereMet fails if the redundant second SELECT reappears.
func TestAdminMerchantService_SetMerchantActive(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	id := uuid.New()

	getRow := sqlmock.NewRows([]string{
		"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "created_at", "updated_at",
	}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, now, now)
	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).WithArgs(id).WillReturnRows(getRow)
	mock.ExpectExec(`UPDATE merchants SET is_active`).WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := svc.SetMerchantActive(context.Background(), id, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsActive {
		t.Errorf("expected merchant to be inactive")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet/unexpected SQL expectations — SetMerchantActive issued more queries than expected: %v", err)
	}
}

func TestAdminMerchantService_ListMerchantProviderConfigs(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	id := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "created_at", "updated_at",
		}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, now, now))
	mock.ExpectQuery(`SELECT[\s\S]*FROM merchant_provider_configs`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "merchant_id", "provider_name", "payment_method", "priority", "weight", "failover_enabled", "is_enabled", "created_at", "updated_at",
		}).AddRow(uuid.New(), id, "cashi", "qris", 1, 100, true, true, now, now))

	configs, err := svc.ListMerchantProviderConfigs(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
}

func TestAdminMerchantService_UpsertMerchantProviderConfig(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	id := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "created_at", "updated_at",
		}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, now, now))
	mock.ExpectExec(`INSERT INTO merchant_provider_configs`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT[\s\S]*FROM merchant_provider_configs`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "merchant_id", "provider_name", "payment_method", "priority", "weight", "failover_enabled", "is_enabled", "created_at", "updated_at",
		}).AddRow(uuid.New(), id, "cashi", "qris", 1, 100, true, true, now, now))

	items, err := svc.UpsertMerchantProviderConfig(context.Background(), id, &provider.UpsertMerchantProviderConfigRequest{
		ProviderName: "cashi", PaymentMethod: "qris",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 config in refreshed list, got %d", len(items))
	}
}

func TestAdminMerchantService_DeleteMerchantProviderConfig(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	id := uuid.New()

	mock.ExpectExec(`DELETE FROM merchant_provider_configs`).
		WithArgs(id, "qris", "cashi").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := svc.DeleteMerchantProviderConfig(context.Background(), id, "qris", "cashi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAdminMerchantService_ListMerchantPayments(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	merchantID := uuid.New()

	mock.ExpectQuery(`SELECT[\s\S]*FROM payments p`).
		WillReturnRows(sqlmock.NewRows(adminPaymentCols).AddRow(
			uuid.New(), "PAY-1", merchantID, int64(10000), "IDR", "qris",
			"cashi", nil, "pending", "desc", nil, nil, nil, nil,
			"production", now, nil, now, now, "Acme",
		))
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := svc.ListMerchantPayments(context.Background(), merchantID, 20, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected total 1, got %d", result.Total)
	}
}
