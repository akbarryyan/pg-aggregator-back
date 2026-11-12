package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
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
	svc := NewAdminMerchantService(merchantRepo, merchantProviderConfigRepo, repository.NewMerchantUserRepository(db), paymentService)
	return svc, mock
}

func TestAdminMerchantService_ListMerchants(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()

	mock.ExpectQuery(`SELECT[\s\S]*FROM merchants`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "is_active", "fee_bearer", "created_at", "updated_at",
		}).AddRow(uuid.New(), "Acme", "a@example.com", "", "Acme Biz", nil, true, "merchant", now, now))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchants`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := svc.ListMerchants(context.Background(), "", "", nil, nil, 20, 0)
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
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme Biz", Password: "s3cret-pass",
	}

	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE email = \$1`).
		WithArgs(req.Email).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM merchant_users`).
		WithArgs(req.Email).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery(`INSERT INTO merchant_users`).
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
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme Biz", Password: "s3cret-pass",
	}

	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE email = \$1`).
		WithArgs(req.Email).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "is_active", "fee_bearer", "created_at", "updated_at",
		}).AddRow(uuid.New(), "Acme", req.Email, "", "Acme Biz", nil, true, "merchant", now, now))

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
			"id", "name", "email", "phone", "business_name", "webhook_url", "is_active", "fee_bearer", "created_at", "updated_at",
		}).AddRow(uuid.New(), "Acme", "a@example.com", "", "Acme Biz", nil, true, "merchant", now, now))

	items, err := svc.ExportMerchants(context.Background(), "", "", nil, nil)
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
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
		}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "merchant", now, now))

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
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
		}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "merchant", now, now))
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
		"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
	}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "merchant", now, now)
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
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
		}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "merchant", now, now))
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
			"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
		}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "merchant", now, now))
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
			"production", now, nil, now, now, int64(0), int64(0), "Acme",
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

func TestAdminMerchantService_UpsertMerchantProviderConfig_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		req     provider.UpsertMerchantProviderConfigRequest
		wantErr error
	}{
		{"sandbox provider", provider.UpsertMerchantProviderConfigRequest{ProviderName: "sandbox", PaymentMethod: "qris"}, provider.ErrProviderConfigSandboxNotAllowed},
		{"unknown provider", provider.UpsertMerchantProviderConfigRequest{ProviderName: "xendit", PaymentMethod: "qris"}, provider.ErrProviderConfigUnknownProvider},
		{"unsupported method", provider.UpsertMerchantProviderConfigRequest{ProviderName: "cashi", PaymentMethod: "va"}, provider.ErrProviderConfigUnsupportedPaymentMethod},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, mock := newAdminMerchantServiceWithMock(t)
			router := providerPkg.NewProviderRouter()
			router.RegisterProvider(&fakeProvider{name: "cashi"})
			svc = svc.WithProviderRegistry(router)
			now := time.Now()
			id := uuid.New()
			mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).
				WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
				}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "merchant", now, now))

			req := tc.req
			_, err := svc.UpsertMerchantProviderConfig(context.Background(), id, &req)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unexpected SQL: %v", err)
			}
		})
	}
}

func TestAdminMerchantService_WebhookURLValidation(t *testing.T) {
	bad := "http://10.0.0.5/hook"
	t.Run("create rejects", func(t *testing.T) {
		svc, mock := newAdminMerchantServiceWithMock(t)
		_, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
			Name: "Acme", Email: "a@example.com", BusinessName: "Acme", WebhookURL: &bad, Password: "s3cret-pass",
		})
		if !errors.Is(err, netsafe.ErrInvalidCallbackURL) {
			t.Fatalf("want ErrInvalidCallbackURL, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unexpected SQL: %v", err)
		}
	})
	t.Run("update rejects", func(t *testing.T) {
		svc, mock := newAdminMerchantServiceWithMock(t)
		_, err := svc.UpdateMerchant(context.Background(), uuid.New(), &merchant.UpdateMerchantRequest{WebhookURL: &bad})
		if !errors.Is(err, netsafe.ErrInvalidCallbackURL) {
			t.Fatalf("want ErrInvalidCallbackURL, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unexpected SQL: %v", err)
		}
	})
}

var feeBearerMerchantCols = []string{
	"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
}

func TestAdminMerchantService_SetFeeBearer_Valid(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	id := uuid.New()

	mock.ExpectExec(`UPDATE merchants SET fee_bearer`).
		WithArgs(id, merchant.FeeBearerCustomer, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).WithArgs(id).
		WillReturnRows(sqlmock.NewRows(feeBearerMerchantCols).
			AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "customer", now, now))

	resp, err := svc.SetFeeBearer(context.Background(), id, merchant.FeeBearerCustomer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FeeBearer != merchant.FeeBearerCustomer {
		t.Errorf("expected fee_bearer customer in response, got %q", resp.FeeBearer)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invalid values must be rejected before any SQL runs: sqlmock has no
// expectations, so any repository call would fail the test as unexpected.
func TestAdminMerchantService_SetFeeBearer_InvalidDoesNotTouchRepo(t *testing.T) {
	for _, v := range []string{"platform", "", "Customer", " merchant"} {
		t.Run("value="+v, func(t *testing.T) {
			svc, mock := newAdminMerchantServiceWithMock(t)
			resp, err := svc.SetFeeBearer(context.Background(), uuid.New(), v)
			if !errors.Is(err, merchant.ErrInvalidFeeBearer) {
				t.Fatalf("expected ErrInvalidFeeBearer, got %v", err)
			}
			if resp != nil {
				t.Errorf("expected nil response, got %+v", resp)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("repo was touched: %v", err)
			}
		})
	}
}

func TestAdminMerchantService_SetFeeBearer_MerchantNotFound(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	id := uuid.New()
	mock.ExpectExec(`UPDATE merchants SET fee_bearer`).WillReturnResult(sqlmock.NewResult(0, 0))

	_, err := svc.SetFeeBearer(context.Background(), id, merchant.FeeBearerCustomer)
	if !errors.Is(err, merchant.ErrMerchantNotFound) {
		t.Fatalf("expected ErrMerchantNotFound, got %v", err)
	}
}

// The created_at window is passed to both the page query and the count query:
// from inclusive, to exclusive, placed before the LIMIT/OFFSET arguments.
func TestAdminMerchantService_ListMerchants_DateRange(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) // exclusive end

	mock.ExpectQuery(`SELECT[\s\S]*FROM merchants[\s\S]*created_at >= \$1 AND created_at < \$2[\s\S]*LIMIT \$3 OFFSET \$4`).
		WithArgs(from, to, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "phone", "business_name", "webhook_url", "is_active", "fee_bearer", "created_at", "updated_at",
		}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM merchants[\s\S]*created_at >= \$1 AND created_at < \$2`).
		WithArgs(from, to).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	if _, err := svc.ListMerchants(context.Background(), "", "", &from, &to, 20, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("date range not applied to both queries: %v", err)
	}
}

func TestAdminMerchantService_CreateMerchant_PasswordRules(t *testing.T) {
	cases := []struct {
		name string
		pw   string
		want error
	}{
		{"required", "", merchant.ErrMerchantPasswordRequired},
		{"too short", "1234567", merchant.ErrMerchantPasswordTooShort},
		{"too long (73 bytes)", strings.Repeat("a", 73), merchant.ErrMerchantPasswordTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, mock := newAdminMerchantServiceWithMock(t)
			_, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
				Name: "Acme", Email: "a@example.com", BusinessName: "Acme", Password: tc.pw,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("no SQL expected before password validation: %v", err)
			}
		})
	}
	t.Run("72 bytes accepted", func(t *testing.T) {
		svc, mock := newAdminMerchantServiceWithMock(t)
		now := time.Now()
		mock.ExpectQuery(`SELECT .* FROM merchants`).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`SELECT .* FROM merchant_users`).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`INSERT INTO merchants`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
		mock.ExpectQuery(`INSERT INTO merchant_users`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
		if _, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
			Name: "Acme", Email: "a@example.com", BusinessName: "Acme", Password: strings.Repeat("a", 72),
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestAdminMerchantService_CreateMerchant_EmailUsedByMerchantUser(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM merchants`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM merchant_users`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "merchant_id", "name", "email", "password_hash", "role", "is_active",
			"last_login_at", "created_at", "updated_at", "sessions_valid_after",
		}).AddRow(uuid.New(), uuid.New(), "X", "a@example.com", "h", "owner", true, nil, now, now, nil))

	_, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
		Name: "Acme", Email: "A@Example.com", BusinessName: "Acme", Password: "s3cret-pass",
	})
	if err != merchant.ErrMerchantAlreadyExists {
		t.Fatalf("expected ErrMerchantAlreadyExists, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected SQL: %v", err)
	}
}

func TestAdminMerchantService_CreateMerchant_CreatesOwnerUser(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	const pw = "s3cret-pass"
	mid := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM merchants`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM merchant_users`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(mid, now, now))
	hashArg := &hashMatcher{pw: pw}
	mock.ExpectQuery(`INSERT INTO merchant_users`).
		WithArgs(sqlmock.AnyArg(), mid, "Acme Owner", "a@example.com", hashArg, "owner", true, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

	resp, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
		Name: "  Acme Owner ", Email: " A@Example.COM ", BusinessName: "Acme", Password: pw,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
	if !hashArg.matched {
		t.Fatal("password hash arg was not verified")
	}
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), pw) || strings.Contains(strings.ToLower(string(raw)), "password") || strings.Contains(string(raw), "$2") {
		t.Errorf("response leaks password/hash: %s", raw)
	}
}

// hashMatcher accepts only a bcrypt hash of pw (and never the plaintext).
type hashMatcher struct {
	pw      string
	matched bool
}

func (h *hashMatcher) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok || s == h.pw {
		return false
	}
	if bcrypt.CompareHashAndPassword([]byte(s), []byte(h.pw)) != nil {
		return false
	}
	h.matched = true
	return true
}

func TestAdminMerchantService_CreateMerchant_RollsBackMerchantWhenUserFails(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	mid := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM merchants`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM merchant_users`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(mid, now, now))
	errInsert := errors.New("boom secret-detail")
	mock.ExpectQuery(`INSERT INTO merchant_users`).WillReturnError(errInsert)
	mock.ExpectExec(`DELETE FROM merchants WHERE id = \$1`).WithArgs(mid).WillReturnResult(sqlmock.NewResult(0, 1))

	_, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme", Password: "s3cret-pass",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// The cause is kept (wrapped) for server-side logs; the handler returns a
	// generic 500 to clients. It must never contain the password.
	if !errors.Is(err, errInsert) {
		t.Errorf("cause not wrapped: %v", err)
	}
	if strings.Contains(err.Error(), "s3cret-pass") {
		t.Errorf("error contains password: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("merchant rollback delete not executed: %v", err)
	}
}

// cancelMatcher cancels the request context when the owner insert is matched,
// simulating a client that disconnected mid-request.
type cancelMatcher struct{ cancel context.CancelFunc }

func (c cancelMatcher) Match(driver.Value) bool { c.cancel(); return true }

func TestAdminMerchantService_CreateMerchant_RollbackSurvivesCancelledContext(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	mid := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mock.ExpectQuery(`SELECT .* FROM merchants`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM merchant_users`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(mid, now, now))
	mock.ExpectQuery(`INSERT INTO merchant_users`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), cancelMatcher{cancel},
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(errors.New("boom"))
	mock.ExpectExec(`DELETE FROM merchants WHERE id = \$1`).WithArgs(mid).WillReturnResult(sqlmock.NewResult(0, 1))

	_, err := svc.CreateMerchant(ctx, &merchant.CreateMerchantRequest{
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme", Password: "s3cret-pass",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if ctx.Err() == nil {
		t.Fatal("test setup: context was not cancelled")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("rollback delete not attempted after cancellation: %v", err)
	}
}

func TestAdminMerchantService_CreateMerchant_RollbackDeleteAlsoFails(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	now := time.Now()
	mid := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM merchants`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM merchant_users`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(mid, now, now))
	mock.ExpectQuery(`INSERT INTO merchant_users`).WillReturnError(errors.New("boom-insert"))
	mock.ExpectExec(`DELETE FROM merchants WHERE id = \$1`).WithArgs(mid).WillReturnError(errors.New("boom-delete"))

	_, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme", Password: "s3cret-pass",
	})
	if err == nil || !strings.Contains(err.Error(), "failed to create merchant owner account") || !strings.Contains(err.Error(), "boom-insert") {
		t.Fatalf("want wrapped owner-account error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestAdminMerchantService_CreateMerchant_PasswordCountsBytes(t *testing.T) {
	svc, mock := newAdminMerchantServiceWithMock(t)
	_, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme", Password: strings.Repeat("é", 37), // 74 bytes
	})
	if !errors.Is(err, merchant.ErrMerchantPasswordTooLong) {
		t.Fatalf("74 bytes: want too long, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected SQL: %v", err)
	}

	svc, mock = newAdminMerchantServiceWithMock(t)
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM merchants`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM merchant_users`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery(`INSERT INTO merchant_users`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	if _, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
		Name: "Acme", Email: "a@example.com", BusinessName: "Acme", Password: strings.Repeat("é", 36), // 72 bytes
	}); err != nil {
		t.Fatalf("72 bytes should be accepted: %v", err)
	}
}
