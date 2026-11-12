package main

import (
	"context"
	"database/sql"
	"errors"
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
		{http.MethodGet, apiBase + "/merchant/balance/daily", ""},
		{http.MethodGet, apiBase + "/merchant/payment-links/stats", ""},
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
		{http.MethodGet, apiBase + "/admin/merchants/stats", ""},
		{http.MethodGet, apiBase + "/admin/payouts", ""},
		{http.MethodGet, apiBase + "/admin/payouts/stats", ""},
		{http.MethodGet, apiBase + "/admin/callbacks/stats", ""},
		{http.MethodGet, apiBase + "/admin/logs/stats", ""},
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

// Positive wiring of the merchant page stats: "/payment-links/stats" must reach
// the stats handler (not "/payment-links/{id}" with id "stats") and
// "/balance/daily" its handler, both scoped to the session's merchant.
func TestRouter_MerchantPageStatsRoutes_ReachHandlers(t *testing.T) {
	e := newSessEnv(t)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ledgerRepo, payoutRepo, merchantRepo := repository.NewLedgerRepository(db), repository.NewPayoutRepository(db), repository.NewMerchantRepository(db)
	balanceSvc := service.NewBalanceService(ledgerRepo, merchantRepo, payoutRepo, 50000).WithDaily(ledgerRepo)
	payoutSvc := service.NewPayoutService(payoutRepo, merchantRepo, 50000)
	linkRepo := repository.NewPaymentLinkRepository(db)
	linkHandler := handler.NewPaymentLinkHandler(service.NewPaymentLinkService(linkRepo, nil, nil).WithStats(linkRepo), "http://localhost:3000")
	merchantHandler := handler.NewMerchantHandler(nil, nil, e.authSvc, "http://localhost:3000").WithBalanceServices(balanceSvc, payoutSvc)
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	router := setupRouter(nil, nil, handler.NewAuthHandler(e.authSvc), handler.NewAdminHandler(nil, "http://localhost:3000"), merchantHandler, linkHandler,
		middleware.NewAuthMiddleware(e.authSvc), nil, limiter, limiter, limiter, limiter,
		func(context.Context) error { return nil })
	tok := e.merchantToken(t, time.Now().UTC())

	mock.ExpectQuery(`FROM payment_links\s+WHERE merchant_id = \$1 AND environment = \$2`).WithArgs(e.merchant.ID, "sandbox").
		WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c", "d"}).AddRow(3, 2, 1, 2))
	mock.ExpectQuery(`FROM payments\s+WHERE merchant_id = \$1 AND payment_link_id IS NOT NULL\s+AND status = 'paid' AND environment = \$2\s*$`).
		WithArgs(e.merchant.ID, "sandbox").WillReturnRows(sqlmock.NewRows([]string{"a", "b"}).AddRow(0, 0))
	mock.ExpectQuery(`DATE\(paid_at\)`).WithArgs(e.merchant.ID, "sandbox", 14).
		WillReturnRows(sqlmock.NewRows([]string{"day", "n", "s"}))
	code := serveFrom(router, http.MethodGet, apiBase+"/merchant/payment-links/stats?environment=sandbox", tok)
	if code != http.StatusOK {
		t.Fatalf("payment-links/stats = %d, want 200 (route order: stats before {id})", code)
	}

	mock.ExpectQuery(`FROM merchant_ledger\s+WHERE merchant_id = \$1`).WithArgs(e.merchant.ID, 7).
		WillReturnRows(sqlmock.NewRows([]string{"day", "c", "d"}))
	if code = serveFrom(router, http.MethodGet, apiBase+"/merchant/balance/daily?days=7", tok); code != http.StatusOK {
		t.Fatalf("balance/daily = %d, want 200", code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The sandbox simulator routes: the merchant one is JWT-protected and reaches
// MerchantHandler.SimulatePayment; the public one needs no auth and reaches
// PaymentHandler.SimulateByReference.
func TestRouter_SandboxSimulateRoutes(t *testing.T) {
	e := newSessEnv(t)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	paymentSvc := service.NewPaymentService(repository.NewPaymentRepository(db), nil, nil, nil, "http://localhost:3000")
	paymentHandler := handler.NewPaymentHandler(paymentSvc, "http://localhost:3000")
	merchantHandler := handler.NewMerchantHandler(paymentSvc, nil, e.authSvc, "http://localhost:3000")
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	router := setupRouter(paymentHandler, nil, handler.NewAuthHandler(e.authSvc), handler.NewAdminHandler(nil, "http://localhost:3000"), merchantHandler, nil,
		middleware.NewAuthMiddleware(e.authSvc), nil, limiter, limiter, limiter, limiter,
		func(context.Context) error { return nil })
	tok := e.merchantToken(t, time.Now().UTC())

	do := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"outcome":"paid"}`))
		req.RemoteAddr = "203.0.113.9:4444"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	merchantPath := apiBase + "/merchant/payments/" + uuid.NewString() + "/simulate"
	if rec := do(merchantPath, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("merchant simulate without token = %d, want 401", rec.Code)
	}

	mock.ExpectQuery(`FROM payments WHERE id = \$1`).WillReturnError(sql.ErrNoRows)
	rec := do(merchantPath, tok)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Payment not found") {
		t.Fatalf("merchant simulate with token = %d %s, want 404 Payment not found", rec.Code, rec.Body.String())
	}

	mock.ExpectQuery(`FROM payments WHERE reference = \$1`).WithArgs("PAY-NOPE").WillReturnError(sql.ErrNoRows)
	rec = do(apiBase+"/public/payments/by-reference/PAY-NOPE/simulate", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Payment not found") {
		t.Fatalf("public simulate = %d %s, want 404 Payment not found", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Registration ORDER of the admin stats routes: gorilla/mux matches in
// registration order and the auth middleware runs after matching, so only a
// real request with an admin token proves "/x/stats" is not captured by
// "/x/{id}" (which would answer 400/404 from the id handlers instead of 200).
func TestRouter_AdminStatsRoutes_ReachHandlers(t *testing.T) {
	e := newSessEnv(t)
	newMock := func() (*sql.DB, sqlmock.Sqlmock) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db, mock
	}
	mDB, mMock := newMock()
	cDB, cMock := newMock()
	lDB, lMock := newMock()
	pDB, pMock := newMock()

	merchantRepo := repository.NewMerchantRepository(mDB)
	adminHandler := handler.NewAdminHandler(nil, "http://localhost:3000").
		WithMerchantAndPaymentServices(nil, service.NewAdminMerchantService(merchantRepo, nil, nil, nil)).
		WithReportingServices(nil, nil,
			service.NewAdminLogService(nil, repository.NewWebhookEventRepository(lDB)),
			service.NewAdminCallbackService(repository.NewMerchantCallbackRepository(cDB))).
		WithPayoutServices(service.NewBalanceService(nil, nil, nil, 50000),
			service.NewPayoutService(repository.NewPayoutRepository(pDB), merchantRepo, 50000))
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	router := setupRouter(nil, nil, handler.NewAuthHandler(e.authSvc), adminHandler,
		handler.NewMerchantHandler(nil, nil, e.authSvc, "http://localhost:3000"), nil,
		middleware.NewAuthMiddleware(e.authSvc), nil, limiter, limiter, limiter, limiter,
		func(context.Context) error { return nil })
	tok := e.adminToken(t, time.Now().UTC())
	serve := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, apiBase+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	wantKeys := func(path string, rec *httptest.ResponseRecorder, keys ...string) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s, want 200 from the stats handler (route order: stats before {id})", path, rec.Code, rec.Body.String())
		}
		for _, k := range append(keys, `"days":14`) {
			if !strings.Contains(rec.Body.String(), k) {
				t.Errorf("%s body lacks %s: %s", path, k, rec.Body.String())
			}
		}
	}

	mMock.ExpectQuery(`FROM merchants`).WillReturnRows(sqlmock.NewRows([]string{"a", "b"}).AddRow(2, 1))
	mMock.ExpectQuery(`WITH b AS`).WillReturnRows(sqlmock.NewRows([]string{"a", "b"}).AddRow(0, 0))
	mMock.ExpectQuery(`GROUP BY day`).WillReturnRows(sqlmock.NewRows([]string{"day", "n"}))
	wantKeys("/admin/merchants/stats", serve("/admin/merchants/stats"), `"total":2`, `"signups_daily":[`)

	cMock.ExpectQuery(`GROUP BY status`).WillReturnRows(sqlmock.NewRows([]string{"s", "n"}))
	cMock.ExpectQuery(`GROUP BY day`).WillReturnRows(sqlmock.NewRows([]string{"d", "s", "f", "o"}))
	wantKeys("/admin/callbacks/stats", serve("/admin/callbacks/stats"), `"breakdown":[]`, `"daily":[`)

	lMock.ExpectQuery(`GROUP BY status`).WillReturnRows(sqlmock.NewRows([]string{"s", "n", "p", "f", "i"}))
	lMock.ExpectQuery(`GROUP BY day`).WillReturnRows(sqlmock.NewRows([]string{"d", "p", "f"}))
	wantKeys("/admin/logs/stats", serve("/admin/logs/stats"), `"processed":0`, `"breakdown":[]`, `"daily":[`)

	pMock.ExpectQuery(`GROUP BY status`).WillReturnRows(sqlmock.NewRows([]string{"s", "n", "a"}))
	pMock.ExpectQuery(`GROUP BY day`).WillReturnRows(sqlmock.NewRows([]string{"d", "n", "a"}))
	wantKeys("/admin/payouts/stats", serve("/admin/payouts/stats"), `"requested_count":0`, `"breakdown":[]`, `"daily":[`)

	// "/logs/export" is not shadowed by the stats route or "/logs/{id}": it
	// reaches ExportLogs (here failing on the mocked DB with its own generic
	// 500), never GetLog's 400 "Invalid log ID".
	lMock.ExpectQuery(`.*`).WillReturnError(errors.New("boom"))
	rec := serve("/admin/logs/export")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "Failed to export logs") {
		t.Fatalf("/admin/logs/export = %d %s, want ExportLogs' 500", rec.Code, rec.Body.String())
	}

	for name, m := range map[string]sqlmock.Sqlmock{"merchants": mMock, "callbacks": cMock, "logs": lMock, "payouts": pMock} {
		if err := m.ExpectationsWereMet(); err != nil {
			t.Errorf("%s unmet expectations: %v", name, err)
		}
	}
}
