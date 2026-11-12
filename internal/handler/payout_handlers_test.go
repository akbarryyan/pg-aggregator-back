package handler

import (
	"context"
	"encoding/json"
	"errors"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// payoutFake records which merchant / admin each call used, so tests can
// prove the handlers take identity from the authenticated context only.
type payoutFake struct {
	mu          sync.Mutex
	err         error
	calls       []string
	lastMerch   uuid.UUID
	lastBy      *uuid.UUID
	lastAmount  int64
	lastStatus  string
	lastText    string
	lastNeg     bool
	lastPayouts uuid.UUID
	account     *merchant.PayoutAccount
	entries     []*ledger.Entry
	payouts     []*ledger.Payout
	stats       *repository.PayoutStats
}

func (f *payoutFake) rec(name string) { f.mu.Lock(); f.calls = append(f.calls, name); f.mu.Unlock() }

func (f *payoutFake) Request(ctx context.Context, m uuid.UUID, amount, min int64, by *uuid.UUID) (*ledger.Payout, error) {
	f.rec("request")
	f.lastMerch, f.lastAmount, f.lastBy = m, amount, by
	if f.err != nil {
		return nil, f.err
	}
	return &ledger.Payout{ID: uuid.New(), MerchantID: m, Amount: amount, Status: ledger.PayoutRequested, RequestedBy: by}, nil
}
func (f *payoutFake) MarkPaid(ctx context.Context, id uuid.UUID, ref string, a *uuid.UUID) (*ledger.Payout, error) {
	f.rec("pay")
	f.lastText, f.lastBy = ref, a
	if f.err != nil {
		return nil, f.err
	}
	return &ledger.Payout{ID: id, Status: ledger.PayoutPaid, Amount: 60000}, nil
}
func (f *payoutFake) Reject(ctx context.Context, id uuid.UUID, reason string, a *uuid.UUID) (*ledger.Payout, error) {
	f.rec("reject")
	f.lastText, f.lastBy = reason, a
	if f.err != nil {
		return nil, f.err
	}
	return &ledger.Payout{ID: id, Status: ledger.PayoutRejected, Amount: 60000}, nil
}
func (f *payoutFake) ListByMerchant(ctx context.Context, m uuid.UUID, l, o int) ([]*ledger.Payout, int64, error) {
	f.rec("list-merchant")
	f.lastMerch = m
	if f.payouts != nil {
		return f.payouts, int64(len(f.payouts)), f.err
	}
	return []*ledger.Payout{}, 0, f.err
}
func (f *payoutFake) ListAdmin(ctx context.Context, status string, l, o int) ([]*ledger.Payout, int64, error) {
	f.rec("list-admin")
	f.lastStatus = status
	if f.payouts != nil {
		return f.payouts, int64(len(f.payouts)), f.err
	}
	return []*ledger.Payout{}, 0, f.err
}
func (f *payoutFake) Stats(ctx context.Context, days int) (*repository.PayoutStats, error) {
	f.rec("stats")
	if f.err != nil {
		return nil, f.err
	}
	if f.stats != nil {
		return f.stats, nil
	}
	return &repository.PayoutStats{}, nil
}
func (f *payoutFake) HasOpen(ctx context.Context, m uuid.UUID) (bool, error) { return false, f.err }
func (f *payoutFake) GetPayoutAccount(ctx context.Context, id uuid.UUID) (*merchant.PayoutAccount, error) {
	return f.account, f.err
}
func (f *payoutFake) SetPayoutAccount(ctx context.Context, id uuid.UUID, bank, number, holder string) error {
	f.rec("set-account")
	if f.err != nil {
		return f.err
	}
	f.account = &merchant.PayoutAccount{BankName: bank, AccountNumber: number, AccountHolder: holder}
	return nil
}
func (f *payoutFake) Summary(ctx context.Context, m uuid.UUID) (*ledger.Summary, error) {
	f.rec("summary")
	f.lastMerch = m
	return &ledger.Summary{Balance: 70000}, f.err
}
func (f *payoutFake) List(ctx context.Context, m uuid.UUID, l, o int) ([]*ledger.Entry, int64, error) {
	f.rec("ledger")
	f.lastMerch = m
	if f.entries != nil {
		return f.entries, int64(len(f.entries)), f.err
	}
	return []*ledger.Entry{}, 0, f.err
}
func (f *payoutFake) ListBalances(ctx context.Context, neg bool, l, o int) ([]*ledger.MerchantBalance, int64, error) {
	f.rec("balances")
	f.lastNeg = neg
	return []*ledger.MerchantBalance{}, 0, f.err
}

type payoutEnv struct {
	h          *MerchantHandler
	fake       *payoutFake
	m          *merchant.Merchant
	merchantID uuid.UUID
	userID     uuid.UUID
}

func newPayoutEnv(t *testing.T) *payoutEnv {
	t.Helper()
	merchantID, userID := uuid.New(), uuid.New()
	m := &merchant.Merchant{ID: merchantID, IsActive: true}
	user := &merchant.User{ID: userID, MerchantID: merchantID, Email: "u@example.com", PasswordHash: mustHash(t, "correct-password"), Role: "owner", IsActive: true}
	authSvc := service.NewAuthService(newFakeAdminRepo(), testJWTSecret).
		WithMerchantAuth(newFakeMerchantUserRepo(user), newFakeAuthMerchantRepo(m))
	fake := &payoutFake{}
	payoutSvc := service.NewPayoutService(fake, fake, 50000)
	balanceSvc := service.NewBalanceService(fake, fake, fake, 50000)
	h := NewMerchantHandler(nil, nil, authSvc, "http://localhost:3000").WithBalanceServices(balanceSvc, payoutSvc)
	return &payoutEnv{h: h, fake: fake, m: m, merchantID: merchantID, userID: userID}
}

func (e *payoutEnv) do(fn http.HandlerFunc, method, target, body string, withCtx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if withCtx {
		ctx := context.WithValue(req.Context(), middleware.MerchantIDContextKey, e.merchantID)
		ctx = context.WithValue(ctx, middleware.MerchantUserIDContextKey, e.userID)
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func TestMerchantHandler_UpdatePayoutAccount_RequiresPassword(t *testing.T) {
	good := `"bank_name":"BCA","account_number":"1234567890","account_holder":"Budi"`
	tests := []struct {
		name        string
		body        string
		wantCode    int
		wantMessage string
		wantChanged bool
	}{
		{"empty body", ``, http.StatusBadRequest, "Invalid request body", false},
		{"missing password", `{` + good + `}`, http.StatusBadRequest, "Password is required to change payout account", false},
		{"blank password", `{"password":"  ",` + good + `}`, http.StatusBadRequest, "Password is required to change payout account", false},
		{"wrong password", `{"password":"nope",` + good + `}`, http.StatusUnauthorized, "Invalid password", false},
		{"empty holder", `{"password":"correct-password","bank_name":"BCA","account_number":"1234567890","account_holder":""}`, http.StatusBadRequest, "account holder", false},
		{"bad number", `{"password":"correct-password","bank_name":"BCA","account_number":"12-34","account_holder":"Budi"}`, http.StatusBadRequest, "account number", false},
		{"correct password", `{"password":"correct-password",` + good + `}`, http.StatusOK, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newPayoutEnv(t)
			rec := e.do(e.h.UpdatePayoutAccount, http.MethodPut, "/api/v1/merchant/payout-account", tc.body, true)
			if rec.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if tc.wantMessage != "" && !strings.Contains(rec.Body.String(), tc.wantMessage) {
				t.Errorf("body %s lacks %q", rec.Body.String(), tc.wantMessage)
			}
			if changed := e.fake.account != nil; changed != tc.wantChanged {
				t.Errorf("account changed = %v, want %v", changed, tc.wantChanged)
			}
			if tc.wantChanged {
				want := merchant.PayoutAccount{BankName: "BCA", AccountNumber: "1234567890", AccountHolder: "Budi"}
				if *e.fake.account != want {
					t.Errorf("stored %+v", *e.fake.account)
				}
			}
		})
	}
}

func TestMerchantHandler_RequestPayout_ErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{"ok", nil, http.StatusCreated, ""},
		{"below minimum", ledger.ErrBelowMinimumPayout, http.StatusBadRequest, "Amount is below the minimum payout"},
		{"open exists", ledger.ErrOpenPayoutExists, http.StatusConflict, "already waiting"},
		{"insufficient", ledger.ErrInsufficientBalance, http.StatusUnprocessableEntity, "Insufficient balance"},
		{"no account", ledger.ErrPayoutAccountMissing, http.StatusUnprocessableEntity, "Set a payout account first"},
		{"merchant gone", merchant.ErrMerchantNotFound, http.StatusNotFound, "Merchant not found"},
		{"db error", errors.New("pq: connection refused host=10.0.0.5"), http.StatusInternalServerError, "Failed to request payout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newPayoutEnv(t)
			e.fake.err = tc.err
			rec := e.do(e.h.RequestPayout, http.MethodPost, "/api/v1/merchant/payouts", `{"amount":60000}`, true)
			if rec.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if tc.wantMsg != "" && !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("body %s lacks %q", rec.Body.String(), tc.wantMsg)
			}
			if strings.Contains(rec.Body.String(), "10.0.0.5") || strings.Contains(rec.Body.String(), "pq:") {
				t.Errorf("raw error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestMerchantHandler_RequestPayout_UsesSessionIdentity(t *testing.T) {
	e := newPayoutEnv(t)
	other := uuid.New()
	body := `{"amount":60000,"merchant_id":"` + other.String() + `"}`
	rec := e.do(e.h.RequestPayout, http.MethodPost, "/api/v1/merchant/payouts?merchant_id="+other.String(), body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if e.fake.lastMerch != e.merchantID || e.fake.lastAmount != 60000 || e.fake.lastBy == nil || *e.fake.lastBy != e.userID {
		t.Fatalf("merchant=%s amount=%d by=%v, want session merchant/user", e.fake.lastMerch, e.fake.lastAmount, e.fake.lastBy)
	}
	if rec := e.do(e.h.RequestPayout, http.MethodPost, "/x", `not json`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", rec.Code)
	}
}

// List/balance endpoints must never read the merchant from query or body.
func TestMerchantHandler_ReadEndpoints_ScopedToSessionMerchant(t *testing.T) {
	other := uuid.New().String()
	for name, fn := range map[string]func(e *payoutEnv) http.HandlerFunc{
		"payouts": func(e *payoutEnv) http.HandlerFunc { return e.h.ListPayouts },
		"ledger":  func(e *payoutEnv) http.HandlerFunc { return e.h.ListLedger },
		"balance": func(e *payoutEnv) http.HandlerFunc { return e.h.GetBalance },
	} {
		t.Run(name, func(t *testing.T) {
			e := newPayoutEnv(t)
			rec := e.do(fn(e), http.MethodGet, "/x?merchant_id="+other+"&limit=5", `{"merchant_id":"`+other+`"}`, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
			}
			if e.fake.lastMerch != e.merchantID {
				t.Fatalf("queried merchant %s, want session merchant %s", e.fake.lastMerch, e.merchantID)
			}
			// Without a session context: 401, repo untouched.
			e2 := newPayoutEnv(t)
			if rec := e2.do(fn(e2), http.MethodGet, "/x", "", false); rec.Code != http.StatusUnauthorized || len(e2.fake.calls) != 0 {
				t.Fatalf("no context: %d calls=%v", rec.Code, e2.fake.calls)
			}
		})
	}
}

func TestMerchantHandler_ListEndpoints_EmptyIsJSONArray(t *testing.T) {
	e := newPayoutEnv(t)
	for name, fn := range map[string]http.HandlerFunc{"payouts": e.h.ListPayouts, "ledger": e.h.ListLedger} {
		rec := e.do(fn, http.MethodGet, "/x", "", true)
		var resp map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || string(resp["items"]) != "[]" {
			t.Fatalf("%s: items = %s (err %v), want []", name, resp["items"], err)
		}
	}
}

func TestMerchantHandler_GetBalance_Shape(t *testing.T) {
	e := newPayoutEnv(t)
	e.fake.account = &merchant.PayoutAccount{BankName: "BCA", AccountNumber: "1234567890", AccountHolder: "Budi"}
	rec := e.do(e.h.GetBalance, http.MethodGet, "/x", "", true)
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["balance"].(float64) != 70000 || resp["min_payout_amount"].(float64) != 50000 || resp["can_request"] != true {
		t.Fatalf("unexpected %v", resp)
	}
	acc := resp["payout_account"].(map[string]any)
	if acc["account_number"] != "1234567890" || acc["bank_name"] != "BCA" {
		t.Fatalf("account = %v", acc)
	}
	for _, k := range []string{"total_credited", "total_paid_out", "total_refunded"} {
		if _, ok := resp[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	e.fake.account = nil
	rec = e.do(e.h.GetBalance, http.MethodGet, "/x", "", true)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["payout_account"] != nil || resp["can_request"] != false {
		t.Fatalf("no account: %v", resp)
	}
}

// ---- admin ----

type adminPayoutEnv struct {
	h       *AdminHandler
	fake    *payoutFake
	adminID uuid.UUID
}

func newAdminPayoutEnv() *adminPayoutEnv {
	fake := &payoutFake{}
	h := NewAdminHandler(nil, "http://localhost:3000").
		WithPayoutServices(service.NewBalanceService(fake, fake, fake, 50000), service.NewPayoutService(fake, fake, 50000))
	return &adminPayoutEnv{h: h, fake: fake, adminID: uuid.New()}
}

func (e *adminPayoutEnv) do(fn http.HandlerFunc, method, target, body string, vars map[string]string, withAdmin bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if withAdmin {
		req = req.WithContext(context.WithValue(req.Context(), middleware.AdminIDContextKey, e.adminID))
	}
	if vars != nil {
		req = mux.SetURLVars(req, vars)
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func TestAdminHandler_PayAndReject(t *testing.T) {
	id := uuid.New().String()
	vars := map[string]string{"id": id}
	tests := []struct {
		name     string
		fn       func(e *adminPayoutEnv) http.HandlerFunc
		body     string
		err      error
		wantCode int
		wantText string
		wantMsg  string
	}{
		{"pay ok", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.PayPayout }, `{"reference":"TRX-9"}`, nil, 200, "TRX-9", ""},
		{"pay blank", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.PayPayout }, `{}`, ledger.ErrReasonRequired, 400, "", "A reference is required"},
		{"pay long", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.PayPayout }, `{"reference":"x"}`, ledger.ErrTextTooLong, 400, "", "too long"},
		{"pay unknown", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.PayPayout }, `{"reference":"x"}`, ledger.ErrPayoutNotFound, 404, "", "Payout not found"},
		{"pay twice", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.PayPayout }, `{"reference":"x"}`, ledger.ErrPayoutAlreadyProcessed, 409, "", "already been processed"},
		{"pay db", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.PayPayout }, `{"reference":"x"}`, errors.New("pq: secret detail"), 500, "", "Failed to process payout"},
		{"reject ok", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.RejectPayout }, `{"reason":"bad account"}`, nil, 200, "bad account", ""},
		{"reject blank", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.RejectPayout }, `{"reason":"  "}`, ledger.ErrReasonRequired, 400, "", "A reason is required"},
		{"reject twice", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.RejectPayout }, `{"reason":"x"}`, ledger.ErrPayoutAlreadyProcessed, 409, "", ""},
		{"wrong type field", func(e *adminPayoutEnv) http.HandlerFunc { return e.h.PayPayout }, `{"reference":123}`, ledger.ErrReasonRequired, 400, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newAdminPayoutEnv()
			e.fake.err = tc.err
			rec := e.do(tc.fn(e), http.MethodPost, "/x", tc.body, vars, true)
			if rec.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if tc.wantMsg != "" && !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("body %s lacks %q", rec.Body.String(), tc.wantMsg)
			}
			if strings.Contains(rec.Body.String(), "secret detail") {
				t.Errorf("raw error leaked: %s", rec.Body.String())
			}
			if tc.wantCode == 200 {
				if e.fake.lastText != tc.wantText || e.fake.lastBy == nil || *e.fake.lastBy != e.adminID {
					t.Errorf("text=%q by=%v, want %q by admin %s", e.fake.lastText, e.fake.lastBy, tc.wantText, e.adminID)
				}
			}
		})
	}
}

func TestAdminHandler_PayoutGuards(t *testing.T) {
	e := newAdminPayoutEnv()
	vars := map[string]string{"id": uuid.NewString()}
	if rec := e.do(e.h.PayPayout, http.MethodPost, "/x", `{"reference":"x"}`, vars, false); rec.Code != http.StatusUnauthorized || len(e.fake.calls) != 0 {
		t.Fatalf("no admin: %d calls=%v", rec.Code, e.fake.calls)
	}
	if rec := e.do(e.h.PayPayout, http.MethodPost, "/x", `{"reference":"x"}`, map[string]string{"id": "nope"}, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := e.do(e.h.RejectPayout, http.MethodPost, "/x", `garbage`, vars, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
	if len(e.fake.calls) != 0 {
		t.Fatalf("service reached on invalid input: %v", e.fake.calls)
	}
}

func TestAdminHandler_ListEndpoints(t *testing.T) {
	e := newAdminPayoutEnv()
	if rec := e.do(e.h.ListPayouts, http.MethodGet, "/x?status=bogus", "", nil, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus status: %d", rec.Code)
	}
	rec := e.do(e.h.ListPayouts, http.MethodGet, "/x?status=requested", "", nil, true)
	if rec.Code != http.StatusOK || e.fake.lastStatus != "requested" {
		t.Fatalf("status filter: %d %q", rec.Code, e.fake.lastStatus)
	}
	var resp map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if string(resp["items"]) != "[]" {
		t.Fatalf("items = %s", resp["items"])
	}
	e.do(e.h.ListBalances, http.MethodGet, "/x?negative=true", "", nil, true)
	if !e.fake.lastNeg {
		t.Fatal("negative=true not passed")
	}
	e.do(e.h.ListBalances, http.MethodGet, "/x", "", nil, true)
	if e.fake.lastNeg {
		t.Fatal("negative must default to false")
	}
	merchantID := uuid.New()
	if rec := e.do(e.h.ListMerchantLedger, http.MethodGet, "/x", "", map[string]string{"id": merchantID.String()}, true); rec.Code != http.StatusOK || e.fake.lastMerch != merchantID {
		t.Fatalf("merchant ledger: %d merchant %s", rec.Code, e.fake.lastMerch)
	}
	if rec := e.do(e.h.ListMerchantLedger, http.MethodGet, "/x", "", map[string]string{"id": "zzz"}, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad merchant id: %d", rec.Code)
	}
}

// Merchant responses carry no admin/user identifiers; admin responses keep them.
func TestMerchantAndAdminResponses_AdminIdentifiers(t *testing.T) {
	adminUUID, userUUID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	reference := "TRX-1"
	entries := []*ledger.Entry{{ID: uuid.New(), MerchantID: uuid.New(), EntryType: ledger.EntryAdjustment, Amount: 5, Note: "n", CreatedBy: &adminUUID, CreatedAt: now}}
	payouts := []*ledger.Payout{{ID: uuid.New(), MerchantID: uuid.New(), Amount: 60000, Status: ledger.PayoutPaid, BankName: "BCA",
		AccountNumber: "1", AccountHolder: "B", RequestedBy: &userUUID, RequestedAt: now, ProcessedBy: &adminUUID, ProcessedAt: &now, Reference: &reference}}
	banned := []string{"created_by", "requested_by", "processed_by", adminUUID.String(), userUUID.String()}

	me := newPayoutEnv(t)
	me.fake.entries, me.fake.payouts = entries, payouts
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"ledger":  me.do(me.h.ListLedger, http.MethodGet, "/x", "", true),
		"payouts": me.do(me.h.ListPayouts, http.MethodGet, "/x", "", true),
		"request": me.do(me.h.RequestPayout, http.MethodPost, "/x", `{"amount":60000}`, true),
	} {
		if rec.Code != 200 && rec.Code != 201 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
		for _, k := range banned {
			if strings.Contains(rec.Body.String(), k) {
				t.Errorf("merchant %s response contains %q: %s", name, k, rec.Body)
			}
		}
	}
	// Still useful: the merchant sees the visible fields.
	rec := me.do(me.h.ListPayouts, http.MethodGet, "/x", "", true)
	for _, k := range []string{`"status":"paid"`, `"reference":"TRX-1"`, `"bank_name":"BCA"`, `"requested_at"`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("merchant payouts lack %s: %s", k, rec.Body)
		}
	}

	ae := newAdminPayoutEnv()
	ae.fake.entries, ae.fake.payouts = entries, payouts
	rec = ae.do(ae.h.ListMerchantLedger, http.MethodGet, "/x", "", map[string]string{"id": uuid.NewString()}, true)
	if !strings.Contains(rec.Body.String(), `"created_by":"`+adminUUID.String()+`"`) {
		t.Errorf("admin ledger lacks created_by: %s", rec.Body)
	}
	rec = ae.do(ae.h.ListPayouts, http.MethodGet, "/x", "", nil, true)
	for _, k := range []string{`"requested_by":"` + userUUID.String(), `"processed_by":"` + adminUUID.String()} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("admin payouts lack %s: %s", k, rec.Body)
		}
	}
}

// The reject handler maps an empty reason through the REAL service and
// repository validation (no fake error is injected): the fake repo here is
// the real PayoutRepository on a sqlmock DB that must never be queried.
func TestAdminHandler_RejectEmptyReasonViaRealRepository(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := repository.NewPayoutRepository(db)
	h := NewAdminHandler(nil, "http://localhost:3000").
		WithPayoutServices(service.NewBalanceService(nil, nil, nil, 50000), service.NewPayoutService(repo, nil, 50000))
	e := &adminPayoutEnv{h: h, adminID: uuid.New()}
	vars := map[string]string{"id": uuid.NewString()}
	for _, body := range []string{`{}`, `{"reason":"   "}`} {
		rec := e.do(h.RejectPayout, http.MethodPost, "/x", body, vars, true)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "A reason is required") {
			t.Fatalf("body %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	rec := e.do(h.RejectPayout, http.MethodPost, "/x", `{"reason":"`+strings.Repeat("x", 501)+`"}`, vars, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "too long") {
		t.Fatalf("501 runes: %d %s", rec.Code, rec.Body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB must not be touched: %v", err)
	}
}

func TestAdminHandler_GetPayoutStats(t *testing.T) {
	e := newAdminPayoutEnv()
	e.fake.stats = &repository.PayoutStats{
		ByStatus: map[string]repository.PayoutStatusAgg{
			"requested": {Count: 2, Amount: 150_000},
			"paid":      {Count: 1, Amount: 50_000},
		},
		Breakdown:  []repository.StatusCount{{Status: "paid", Count: 1}, {Status: "requested", Count: 2}},
		DailyByDay: map[string]repository.PayoutDayAgg{time.Now().Format("2006-01-02"): {Count: 3, Amount: 200_000}},
	}
	rec := e.do(e.h.GetPayoutStats, http.MethodGet, "/x", "", nil, true)
	if rec.Code != 200 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"days", "requested_count", "requested_amount", "paid_count", "paid_amount", "rejected_count", "rejected_amount", "daily", "breakdown"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing key %q in %s", k, rec.Body.String())
		}
	}
	if got["days"] != float64(14) || got["requested_count"] != float64(2) || got["requested_amount"] != float64(150_000) ||
		got["paid_amount"] != float64(50_000) || got["rejected_count"] != float64(0) {
		t.Fatalf("values wrong: %s", rec.Body.String())
	}
	daily := got["daily"].([]any)
	if len(daily) != 14 {
		t.Fatalf("daily = %d points, want 14", len(daily))
	}
	last := daily[13].(map[string]any)
	if last["count"] != float64(3) || last["amount"] != float64(200_000) || last["date"] == "" || last["label"] == "" {
		t.Fatalf("today's point = %v", last)
	}
}

func TestAdminHandler_GetPayoutStats_EmptyAndError(t *testing.T) {
	e := newAdminPayoutEnv()
	rec := e.do(e.h.GetPayoutStats, http.MethodGet, "/x", "", nil, true)
	if !strings.Contains(rec.Body.String(), `"breakdown":[]`) || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("empty body must use [] never null: %s", rec.Body.String())
	}

	e.fake.err = errors.New("pq: secret detail")
	rec = e.do(e.h.GetPayoutStats, http.MethodGet, "/x", "", nil, true)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("error = %d %s, want generic 500", rec.Code, rec.Body.String())
	}
}

func newStatsHandler(t *testing.T) (*AdminHandler, sqlmock.Sqlmock, sqlmock.Sqlmock) {
	t.Helper()
	cdb, cmock, _ := sqlmock.New()
	ldb, lmock, _ := sqlmock.New()
	t.Cleanup(func() { cdb.Close(); ldb.Close() })
	h := NewAdminHandler(nil, "http://localhost:3000").WithReportingServices(nil, nil,
		service.NewAdminLogService(nil, repository.NewWebhookEventRepository(ldb)),
		service.NewAdminCallbackService(repository.NewMerchantCallbackRepository(cdb)))
	return h, cmock, lmock
}

func TestAdminHandler_GetCallbackStats(t *testing.T) {
	h, cmock, _ := newStatsHandler(t)
	today := time.Now().Format("2006-01-02")
	cmock.ExpectQuery("GROUP BY status").WillReturnRows(sqlmock.NewRows([]string{"status", "count"}).
		AddRow("failed", 1).AddRow("pending", 2).AddRow("sending", 1).AddRow("skipped", 1).AddRow("success", 5))
	cmock.ExpectQuery("GROUP BY day").WillReturnRows(sqlmock.NewRows([]string{"day", "s", "f", "o"}).
		AddRow(time.Now(), 5, 1, 4))
	rec := httptest.NewRecorder()
	h.GetCallbackStats(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["total"] != float64(10) || got["success"] != float64(5) || got["failed"] != float64(1) || got["pending"] != float64(3) || got["skipped"] != float64(1) || got["days"] != float64(14) {
		t.Fatalf("values: %s", rec.Body.String())
	}
	daily := got["daily"].([]any)
	last := daily[13].(map[string]any)
	if len(daily) != 14 || last["date"] != today || last["success"] != float64(5) || last["other"] != float64(4) {
		t.Fatalf("daily: %v", daily)
	}
	if len(got["breakdown"].([]any)) != 5 {
		t.Fatalf("breakdown: %v", got["breakdown"])
	}
}

func TestAdminHandler_GetCallbackStats_EmptyAndError(t *testing.T) {
	h, cmock, _ := newStatsHandler(t)
	cmock.ExpectQuery("GROUP BY status").WillReturnRows(sqlmock.NewRows([]string{"status", "count"}))
	cmock.ExpectQuery("GROUP BY day").WillReturnRows(sqlmock.NewRows([]string{"day", "s", "f", "o"}))
	rec := httptest.NewRecorder()
	h.GetCallbackStats(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !strings.Contains(rec.Body.String(), `"breakdown":[]`) || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("empty body: %s", rec.Body.String())
	}
	cmock.ExpectQuery("GROUP BY status").WillReturnError(errors.New("pq: secret detail"))
	rec = httptest.NewRecorder()
	h.GetCallbackStats(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "pq") {
		t.Fatalf("error = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminHandler_GetLogStats(t *testing.T) {
	h, _, lmock := newStatsHandler(t)
	lmock.ExpectQuery("GROUP BY status").WillReturnRows(sqlmock.NewRows([]string{"status", "n", "p", "f", "i"}).
		AddRow("ignored", 2, 0, 0, 2).AddRow("paid", 5, 5, 0, 0).AddRow("rejected", 3, 0, 3, 0))
	lmock.ExpectQuery("GROUP BY day").WillReturnRows(sqlmock.NewRows([]string{"day", "p", "f"}).AddRow(time.Now(), 5, 3))
	rec := httptest.NewRecorder()
	h.GetLogStats(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["total"] != float64(10) || got["processed"] != float64(5) || got["failed"] != float64(3) || got["ignored"] != float64(2) || got["days"] != float64(14) {
		t.Fatalf("values: %s", rec.Body.String())
	}
	daily := got["daily"].([]any)
	last := daily[13].(map[string]any)
	if len(daily) != 14 || last["processed"] != float64(5) || last["failed"] != float64(3) {
		t.Fatalf("daily: %v", daily)
	}
	if _, has := last["ignored"]; has {
		t.Fatal("daily must not carry ignored")
	}
}

func TestAdminHandler_GetLogStats_EmptyAndError(t *testing.T) {
	h, _, lmock := newStatsHandler(t)
	lmock.ExpectQuery("GROUP BY status").WillReturnRows(sqlmock.NewRows([]string{"status", "n", "p", "f", "i"}))
	lmock.ExpectQuery("GROUP BY day").WillReturnRows(sqlmock.NewRows([]string{"day", "p", "f"}))
	rec := httptest.NewRecorder()
	h.GetLogStats(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !strings.Contains(rec.Body.String(), `"breakdown":[]`) || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("empty body: %s", rec.Body.String())
	}
	lmock.ExpectQuery("GROUP BY status").WillReturnError(errors.New("pq: secret detail"))
	rec = httptest.NewRecorder()
	h.GetLogStats(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "pq") {
		t.Fatalf("error = %d %s", rec.Code, rec.Body.String())
	}
}
