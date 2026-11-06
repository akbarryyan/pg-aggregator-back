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

const apiBase = "/api/v1"

type routeCase struct{ method, path, body string }

func merchantBalanceRoutes() []routeCase {
	return []routeCase{
		{http.MethodGet, apiBase + "/merchant/balance", ""},
		{http.MethodGet, apiBase + "/merchant/ledger", ""},
		{http.MethodPut, apiBase + "/merchant/payout-account", `{"password":"x"}`},
		{http.MethodPost, apiBase + "/merchant/payouts", `{"amount":60000}`},
		{http.MethodGet, apiBase + "/merchant/payouts", ""},
	}
}

func adminPayoutRoutes() []routeCase {
	id := uuid.NewString()
	return []routeCase{
		{http.MethodGet, apiBase + "/admin/balances", ""},
		{http.MethodGet, apiBase + "/admin/payouts", ""},
		{http.MethodPost, apiBase + "/admin/payouts/" + id + "/pay", `{"reference":"x"}`},
		{http.MethodPost, apiBase + "/admin/payouts/" + id + "/reject", `{"reason":"x"}`},
		{http.MethodGet, apiBase + "/admin/merchants/" + id + "/ledger", ""},
		{http.MethodPost, apiBase + "/admin/payments/" + id + "/refund", `{"amount":1000,"reason":"x"}`},
		{http.MethodPost, apiBase + "/admin/merchants/" + id + "/adjustments", `{"amount":1000,"reason":"x"}`},
		{http.MethodGet, apiBase + "/admin/payments/" + id + "/refunds", ""},
	}
}

// Merchant balance routes need a merchant JWT: nothing, or an admin JWT, is
// refused by RequireMerchant before the (nil here) handler is reached.
func TestRouter_MerchantBalanceRoutes_RejectMissingAndAdminJWT(t *testing.T) {
	e := newSessEnv(t)
	adminTok := e.adminToken(t, time.Now().UTC())
	for _, rc := range merchantBalanceRoutes() {
		wantStatus(t, e.do(rc.method, rc.path, "", []byte(rc.body)), http.StatusUnauthorized, "no token "+rc.method+" "+rc.path)
		wantStatus(t, e.do(rc.method, rc.path, adminTok, []byte(rc.body)), http.StatusUnauthorized, "admin JWT "+rc.method+" "+rc.path)
	}
}

// Admin payout routes need an admin JWT: a valid merchant JWT is refused.
func TestRouter_AdminPayoutRoutes_RejectMissingAndMerchantJWT(t *testing.T) {
	e := newSessEnv(t)
	merchantTok := e.merchantToken(t, time.Now().UTC())
	for _, rc := range adminPayoutRoutes() {
		wantStatus(t, e.do(rc.method, rc.path, "", []byte(rc.body)), http.StatusUnauthorized, "no token "+rc.method+" "+rc.path)
		wantStatus(t, e.do(rc.method, rc.path, merchantTok, []byte(rc.body)), http.StatusUnauthorized, "merchant JWT "+rc.method+" "+rc.path)
	}
}

// Merchant money-moving routes sit behind the sensitive limiter (after auth,
// so real tokens are used): the first request reaches the handler (400 for the
// empty body, no DB involved), the second is stopped with 429. Admin routes
// (pay/reject/refund/adjust) deliberately use the general api limiter: with the
// sensitive bucket exhausted they keep reaching the handler.
func TestRouter_MoneyRoutes_LimiterWiring(t *testing.T) {
	e := newSessEnv(t)
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	payoutRepo, merchantRepo := repository.NewPayoutRepository(db), repository.NewMerchantRepository(db)
	balanceSvc := service.NewBalanceService(repository.NewLedgerRepository(db), merchantRepo, payoutRepo, 50000)
	payoutSvc := service.NewPayoutService(payoutRepo, merchantRepo, 50000)
	refundRepo := repository.NewRefundRepository(db)
	refundSvc := service.NewRefundService(refundRepo, repository.NewLedgerRepository(db), nil).WithLister(refundRepo)
	open := middleware.NewIPRateLimiter(1000, 1000)
	id := uuid.NewString()
	now := time.Now().UTC()
	for _, rc := range []struct {
		routeCase
		admin bool
	}{
		{routeCase{http.MethodPut, apiBase + "/merchant/payout-account", ""}, false},
		{routeCase{http.MethodPost, apiBase + "/merchant/payouts", ""}, false},
		{routeCase{http.MethodPost, apiBase + "/admin/payouts/" + id + "/pay", ""}, true},
		{routeCase{http.MethodPost, apiBase + "/admin/payouts/" + id + "/reject", ""}, true},
		{routeCase{http.MethodPost, apiBase + "/admin/payments/" + id + "/refund", ""}, true},
		{routeCase{http.MethodPost, apiBase + "/admin/merchants/" + id + "/adjustments", ""}, true},
	} {
		tight := middleware.NewIPRateLimiter(1, 1)
		router := setupRouter(nil, nil, handler.NewAuthHandler(e.authSvc),
			handler.NewAdminHandler(nil, "http://localhost:3000").WithPayoutServices(balanceSvc, payoutSvc).WithRefundService(refundSvc),
			handler.NewMerchantHandler(nil, nil, e.authSvc, "http://localhost:3000").WithBalanceServices(balanceSvc, payoutSvc), nil,
			middleware.NewAuthMiddleware(e.authSvc), nil, open, open, tight, open,
			func(context.Context) error { return nil })
		tok := e.merchantToken(t, now)
		if rc.admin {
			tok = e.adminToken(t, now)
		}
		if code := serveFrom(router, rc.method, rc.path, tok); code != http.StatusBadRequest {
			t.Fatalf("%s %s first: %d, want 400 from the handler", rc.method, rc.path, code)
		}
		want := http.StatusTooManyRequests
		if rc.admin {
			want = http.StatusBadRequest // not behind the sensitive bucket
		}
		for i := 0; i < 3; i++ {
			if code := serveFrom(router, rc.method, rc.path, tok); code != want {
				t.Fatalf("%s %s repeat %d: %d, want %d", rc.method, rc.path, i, code, want)
			}
		}
	}
}

// Positive wiring: real tokens reach the real handlers through the real router.
func TestRouter_BalanceRoutes_ReachHandlers(t *testing.T) {
	e := newSessEnv(t)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ledgerRepo, payoutRepo, merchantRepo := repository.NewLedgerRepository(db), repository.NewPayoutRepository(db), repository.NewMerchantRepository(db)
	balanceSvc := service.NewBalanceService(ledgerRepo, merchantRepo, payoutRepo, 50000)
	payoutSvc := service.NewPayoutService(payoutRepo, merchantRepo, 50000)
	merchantHandler := handler.NewMerchantHandler(nil, nil, e.authSvc, "http://localhost:3000").WithBalanceServices(balanceSvc, payoutSvc)
	adminHandler := handler.NewAdminHandler(nil, "http://localhost:3000").WithPayoutServices(balanceSvc, payoutSvc)
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	router := setupRouter(nil, nil, handler.NewAuthHandler(e.authSvc), adminHandler, merchantHandler, nil,
		middleware.NewAuthMiddleware(e.authSvc), nil, limiter, limiter, limiter, limiter,
		func(context.Context) error { return nil })

	serve := func(method, path, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(""))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	mock.ExpectQuery(`FROM merchant_ledger WHERE merchant_id = \$1`).WithArgs(e.merchant.ID).
		WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c", "d"}).AddRow(70000, 100000, 30000, 0))
	mock.ExpectQuery(`FROM merchants WHERE id = \$1`).WithArgs(e.merchant.ID).
		WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c"}).AddRow(nil, nil, nil))
	mock.ExpectQuery(`FROM payouts WHERE merchant_id`).WithArgs(e.merchant.ID).
		WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(false))
	rec := serve(http.MethodGet, apiBase+"/merchant/balance", e.merchantToken(t, time.Now().UTC()))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"balance":70000`) || !strings.Contains(rec.Body.String(), `"payout_account":null`) {
		t.Fatalf("merchant balance: %d %s", rec.Code, rec.Body.String())
	}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM b`).WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectQuery(`FROM b LEFT JOIN payouts`).WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c", "d", "e"}))
	rec = serve(http.MethodGet, apiBase+"/admin/balances", e.adminToken(t, time.Now().UTC()))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("admin balances: %d %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
