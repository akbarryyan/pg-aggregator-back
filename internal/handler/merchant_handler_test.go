package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
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

// webhookSecretRepo is a merchant repo that records the stored webhook secret
// so tests can prove whether a regenerate request changed it.
type webhookSecretRepo struct{ m *merchant.Merchant }

func (r *webhookSecretRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error) {
	return r.m, nil
}

func (r *webhookSecretRepo) SetWebhookSecret(ctx context.Context, id uuid.UUID, secret string) error {
	r.m.WebhookSecret = &secret
	return nil
}

func TestMerchantHandler_RegenerateWebhookSecret_RequiresPassword(t *testing.T) {
	const oldSecret = "old-secret"
	merchantID, userID := uuid.New(), uuid.New()
	m := &merchant.Merchant{ID: merchantID, IsActive: true, WebhookSecret: func() *string { s := oldSecret; return &s }()}
	repo := &webhookSecretRepo{m: m}
	user := &merchant.User{ID: userID, MerchantID: merchantID, Email: "u@example.com", PasswordHash: mustHash(t, "correct-password"), Role: "owner", IsActive: true}

	paymentSvc := service.NewPaymentService(newFakePaymentRepo(), &fakeMerchantProviderConfigRepo{}, newFakeWebhookEventRepo(), providerPkg.NewProviderRouter(), "http://localhost:8080").
		WithMerchantCallbackDeps(repo, nil)
	authSvc := service.NewAuthService(newFakeAdminRepo(), testJWTSecret).
		WithMerchantAuth(newFakeMerchantUserRepo(user), newFakeAuthMerchantRepo(m))
	h := NewMerchantHandler(paymentSvc, nil, authSvc, "http://localhost:3000")

	call := func(body string) *httptest.ResponseRecorder {
		ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, merchantID)
		ctx = context.WithValue(ctx, middleware.MerchantUserIDContextKey, userID)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/merchant/webhook-secret/regenerate", strings.NewReader(body)).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.RegenerateWebhookSecret(rec, req)
		return rec
	}

	tests := []struct {
		name        string
		body        string
		wantCode    int
		wantMessage string
		wantChanged bool
	}{
		{"empty body", ``, http.StatusBadRequest, "Invalid request body", false},
		{"empty password", `{"password":"  "}`, http.StatusBadRequest, "Password is required to regenerate webhook secret", false},
		{"missing password field", `{}`, http.StatusBadRequest, "Password is required to regenerate webhook secret", false},
		{"wrong password", `{"password":"nope"}`, http.StatusUnauthorized, "Invalid password", false},
		{"correct password", `{"password":"correct-password"}`, http.StatusOK, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if tc.wantMessage != "" && !strings.Contains(rec.Body.String(), tc.wantMessage) {
				t.Errorf("expected body to contain %q, got %s", tc.wantMessage, rec.Body.String())
			}
			changed := *m.WebhookSecret != oldSecret
			if changed != tc.wantChanged {
				t.Errorf("secret changed = %v, want %v", changed, tc.wantChanged)
			}
			if tc.wantChanged {
				var resp map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp["webhook_secret"] != *m.WebhookSecret || resp["webhook_secret"] == "" {
					t.Errorf("response should carry the new stored secret, got %s", rec.Body.String())
				}
			}
		})
	}
}
