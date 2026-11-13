package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type detailsWriter struct {
	mu sync.Mutex
	es []activity.Entry
}

func (w *detailsWriter) Insert(ctx context.Context, e *activity.Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.es = append(w.es, *e)
	return nil
}

// recordDetails runs fn behind the real activity middleware and returns the
// details of the single recorded entry.
func recordDetails(t *testing.T, fn http.HandlerFunc, req *http.Request) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	w := &detailsWriter{}
	rec := activity.NewRecorder(w, 8)
	out := httptest.NewRecorder()
	activity.Middleware(rec, nil, nil)(fn).ServeHTTP(out, req)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rec.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.es) != 1 {
		t.Fatalf("entries = %d, want 1", len(w.es))
	}
	return out, w.es[0].Details
}

func TestActivityDetails_FeeBearer(t *testing.T) {
	h, mock := newFeeBearerAdminHandler(t)
	id := uuid.New()
	now := time.Now()
	mock.ExpectExec(`UPDATE merchants SET fee_bearer`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM merchants`).WillReturnRows(sqlmock.NewRows(feeBearerCols).
		AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "customer", now, now))
	req := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"fee_bearer":"customer"}`))
	req = mux.SetURLVars(req, map[string]string{"id": id.String()})
	rec, d := recordDetails(t, h.SetMerchantFeeBearer, req)
	if rec.Code != 200 || d["to"] != "customer" {
		t.Fatalf("code=%d details=%v", rec.Code, d)
	}
}

func TestActivityDetails_FeeBearerFailureHasNoDetails(t *testing.T) {
	h, _ := newFeeBearerAdminHandler(t)
	req := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"fee_bearer":"x"}`))
	req = mux.SetURLVars(req, map[string]string{"id": uuid.New().String()})
	_, d := recordDetails(t, h.SetMerchantFeeBearer, req)
	if len(d) != 0 {
		t.Fatalf("details on failure: %v", d)
	}
}

func TestActivityDetails_AdjustBalance(t *testing.T) {
	e, _ := newRefundEnv()
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"amount":5000,"reason":"goodwill"}`))
	req = mux.SetURLVars(req, map[string]string{"id": uuid.NewString()})
	req = req.WithContext(adminCtx(req.Context(), e.adminID))
	rec, d := recordDetails(t, e.h.AdjustMerchantBalance, req)
	if rec.Code != 201 || d["amount"] != int64(5000) || d["reason"] != "goodwill" {
		t.Fatalf("code=%d details=%v", rec.Code, d)
	}
}

func TestActivityDetails_PayAndRejectPayout(t *testing.T) {
	e := newAdminPayoutEnv()
	vars := map[string]string{"id": uuid.NewString()}
	mk := func(body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		req = mux.SetURLVars(req, vars)
		return req.WithContext(adminCtx(req.Context(), e.adminID))
	}
	rec, d := recordDetails(t, e.h.PayPayout, mk(`{"reference":"TRX-9"}`))
	if rec.Code != 200 || d["amount"] != int64(60000) || d["reference"] != "TRX-9" {
		t.Fatalf("pay: code=%d details=%v", rec.Code, d)
	}
	rec, d = recordDetails(t, e.h.RejectPayout, mk(`{"reason":"bad account"}`))
	if rec.Code != 200 || d["amount"] != int64(60000) || d["reason"] != "bad account" {
		t.Fatalf("reject: code=%d details=%v", rec.Code, d)
	}
}

func TestActivityDetails_MerchantPayoutAndAccount(t *testing.T) {
	e := newPayoutEnv(t)
	mk := func(method, body string) *http.Request {
		req := httptest.NewRequest(method, "/x", strings.NewReader(body))
		ctx := merchantCtx(req.Context(), e.merchantID, e.userID)
		return req.WithContext(ctx)
	}
	rec, d := recordDetails(t, e.h.RequestPayout, mk(http.MethodPost, `{"amount":60000}`))
	if rec.Code != 201 || d["amount"] != int64(60000) {
		t.Fatalf("request payout: code=%d details=%v", rec.Code, d)
	}
	rec, d = recordDetails(t, e.h.UpdatePayoutAccount, mk(http.MethodPut,
		`{"password":"correct-password","bank_name":"BCA","account_number":"1234567890","account_holder":"Budi"}`))
	if rec.Code != 200 || d["bank"] != "BCA" || d["account_last4"] != "7890" {
		t.Fatalf("payout account: code=%d details=%v", rec.Code, d)
	}
	b, _ := json.Marshal(d)
	for _, bad := range []string{"1234567890", "correct-password", "Budi"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("details leak %q: %s", bad, b)
		}
	}
}

func TestActivityDetails_APIKeyNeverContainsSecret(t *testing.T) {
	keyID := uuid.New()
	res := &merchant.UpsertAPIKeyResponse{
		Key:         &merchant.APIKeyPublic{ID: keyID, KeyPrefix: "pk_live_abcd"},
		Secret:      "pk_live_SUPERSECRETVALUE",
		Hint:        "pk_live_abcd...",
		Environment: "production",
	}
	d := apiKeyDetails(res)
	if d["environment"] != "production" || d["key_id"] != keyID.String() {
		t.Fatalf("details = %v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "SUPERSECRET") || strings.Contains(string(b), "pk_live_abcd") {
		t.Fatalf("key material in details: %s", b)
	}
	if got := apiKeyDetails(&merchant.UpsertAPIKeyResponse{Environment: "sandbox"}); got["key_id"] != nil || got["environment"] != "sandbox" {
		t.Fatalf("nil key: %v", got)
	}
}

func TestActivityDetails_Last4AndUpdatedFields(t *testing.T) {
	if got := last4("12-34 5678 90"); got != "7890" {
		t.Errorf("last4 = %q", got)
	}
	if got := last4("12"); got != "12" {
		t.Errorf("last4 short = %q", got)
	}
	s := "x"
	got := updatedMerchantFields(&merchant.UpdateMerchantRequest{Name: &s, WebhookURL: &s})
	if len(got) != 2 || got[0] != "name" || got[1] != "webhook_url" {
		t.Errorf("fields = %v", got)
	}
}

func adminCtx(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, middleware.AdminIDContextKey, id)
}

func merchantCtx(ctx context.Context, merchantID, userID uuid.UUID) context.Context {
	ctx = context.WithValue(ctx, middleware.MerchantIDContextKey, merchantID)
	return context.WithValue(ctx, middleware.MerchantUserIDContextKey, userID)
}
