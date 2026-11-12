package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/handler"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
)

const feeBearerPath = "/api/v1/admin/merchants/"

func feeBearerURL() string { return feeBearerPath + uuid.New().String() + "/fee-bearer" }

// Only an admin may change who pays the provider fee: no token and a valid
// MERCHANT token must both be rejected by the admin auth middleware before
// the (nil here) handler is ever reached. A 200 would mean the route leaked
// into a merchant-accessible group.
func TestRouter_AdminFeeBearer_RejectsAnonymousAndMerchant(t *testing.T) {
	e := newSessEnv(t)
	body := []byte(`{"fee_bearer":"customer"}`)

	wantStatus(t, e.do(http.MethodPut, feeBearerURL(), "", body), http.StatusUnauthorized, "no token")
	merchantTok := e.merchantToken(t, time.Now().UTC())
	wantStatus(t, e.do(http.MethodPut, feeBearerURL(), merchantTok, body), http.StatusUnauthorized, "merchant JWT on admin route")
}

// The route sits behind apiRateLimiter, which runs before auth: forged
// tokens within burst get 401, the request past burst gets 429.
func TestRouter_AdminFeeBearer_APILimiterRunsBeforeAuth(t *testing.T) {
	const burst = 3
	mux := newLimiterRouter(t, middleware.NewIPRateLimiter(1000, 1000), middleware.NewIPRateLimiter(1, burst))
	url := feeBearerURL()
	for i := 0; i < burst; i++ {
		if code := serveFrom(mux, http.MethodPut, url, "forged-token"); code != http.StatusUnauthorized {
			t.Fatalf("request %d: expected 401 from auth, got %d", i+1, code)
		}
	}
	if code := serveFrom(mux, http.MethodPut, url, "forged-token"); code != http.StatusTooManyRequests {
		t.Fatalf("request past burst: expected 429 before auth, got %d", code)
	}
}

// Positive wiring: an admin token reaches SetMerchantFeeBearer (not some other
// handler) through the real router and the response carries the new bearer.
func TestRouter_AdminFeeBearer_AdminTokenReachesHandler(t *testing.T) {
	e := newSessEnv(t)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	merchantSvc := service.NewAdminMerchantService(repository.NewMerchantRepository(db), nil, nil, nil)
	adminHandler := handler.NewAdminHandler(nil, "http://localhost:3000").WithMerchantAndPaymentServices(nil, merchantSvc)
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	router := setupRouter(nil, nil, handler.NewAuthHandler(e.authSvc), adminHandler, nil, nil,
		middleware.NewAuthMiddleware(e.authSvc), nil, limiter, limiter, limiter, limiter,
		func(context.Context) error { return nil })

	id := uuid.New()
	now := time.Now()
	mock.ExpectExec(`UPDATE merchants SET fee_bearer`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM merchants`).WillReturnRows(sqlmock.NewRows([]string{
		"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
	}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "customer", now, now))

	req := httptest.NewRequest(http.MethodPut, feeBearerPath+id.String()+"/fee-bearer", strings.NewReader(`{"fee_bearer":"customer"}`))
	req.Header.Set("Authorization", "Bearer "+e.adminToken(t, time.Now().UTC()))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"fee_bearer":"customer"`) {
		t.Errorf("body %q lacks fee_bearer customer", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
