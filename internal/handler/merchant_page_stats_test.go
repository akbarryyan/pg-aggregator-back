package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
)

type linkStatsFake struct {
	merchant uuid.UUID
	env      string
	err      error
}

func (f *linkStatsFake) Stats(ctx context.Context, m uuid.UUID, env string, days int) (*repository.PaymentLinkStats, error) {
	f.merchant, f.env = m, env
	if f.err != nil {
		return nil, f.err
	}
	return &repository.PaymentLinkStats{}, nil
}

type ledgerDailyFake struct {
	merchant uuid.UUID
	days     int
	err      error
}

func (f *ledgerDailyFake) Daily(ctx context.Context, m uuid.UUID, days int) (map[string]repository.LedgerDayAgg, error) {
	f.merchant, f.days = m, days
	return map[string]repository.LedgerDayAgg{}, f.err
}

func serveWithMerchant(fn http.HandlerFunc, target string, merchantID uuid.UUID, withCtx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if withCtx {
		req = req.WithContext(context.WithValue(req.Context(), middleware.MerchantIDContextKey, merchantID))
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func TestPaymentLinkHandler_GetPaymentLinkStats(t *testing.T) {
	f := &linkStatsFake{}
	h := NewPaymentLinkHandler(service.NewPaymentLinkService(nil, nil, nil).WithStats(f), "http://localhost:3000")
	m := uuid.New()

	rec := serveWithMerchant(h.GetPaymentLinkStats, "/x?environment=sandbox&merchant_id="+uuid.NewString(), m, true)
	if rec.Code != 200 || f.merchant != m || f.env != "sandbox" {
		t.Fatalf("code %d merchant %v env %q: %s", rec.Code, f.merchant, f.env, rec.Body.String())
	}
	body := rec.Body.String()
	for _, k := range []string{`"days":14`, `"total":0`, `"active":0`, `"inactive":0`, `"fixed":0`, `"open":0`, `"paid_count":0`, `"paid_amount":0`, `"daily":[{`, `"breakdown":[]`} {
		if !strings.Contains(body, k) {
			t.Errorf("body lacks %s: %s", k, body)
		}
	}
	if strings.Contains(body, "null") {
		t.Fatalf("arrays must be [] never null: %s", body)
	}

	// Default and unknown environment follow the other merchant endpoints: production.
	for _, target := range []string{"/x", "/x?environment=bogus", "/x?environment=live"} {
		serveWithMerchant(h.GetPaymentLinkStats, target, m, true)
		if f.env != "production" {
			t.Errorf("%s -> env %q, want production", target, f.env)
		}
	}

	if rec := serveWithMerchant(h.GetPaymentLinkStats, "/x", m, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", rec.Code)
	}

	f.err = errors.New("pq: secret detail")
	rec = serveWithMerchant(h.GetPaymentLinkStats, "/x", m, true)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("error = %d %s, want generic 500", rec.Code, rec.Body.String())
	}
}

func TestMerchantHandler_GetBalanceDaily(t *testing.T) {
	e := newPayoutEnv(t)
	f := &ledgerDailyFake{}
	// Rebuild the balance service with the daily source wired.
	e.h.balanceService = service.NewBalanceService(e.fake, e.fake, e.fake, 50000).WithDaily(f)

	cases := map[string]int{"": 14, "?days=7": 7, "?days=0": 14, "?days=-3": 1, "?days=500": 90, "?days=abc": 14, "?days=90": 90}
	for q, want := range cases {
		rec := serveWithMerchant(e.h.GetBalanceDaily, "/x"+q, e.merchantID, true)
		if rec.Code != 200 || f.days != want || f.merchant != e.merchantID {
			t.Fatalf("%q: code %d days %d (want %d) merchant %v: %s", q, rec.Code, f.days, want, f.merchant, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"daily":[{"date":`) || strings.Contains(body, "null") || !strings.Contains(body, `"credited":0,"debited":0`) {
			t.Fatalf("%q: bad body %s", q, body)
		}
	}

	if rec := serveWithMerchant(e.h.GetBalanceDaily, "/x", e.merchantID, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", rec.Code)
	}
	f.err = errors.New("pq: secret detail")
	rec := serveWithMerchant(e.h.GetBalanceDaily, "/x", e.merchantID, true)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("error = %d %s, want generic 500", rec.Code, rec.Body.String())
	}
}
