package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
)

type refundFake struct {
	err        error
	calls      int
	lastID     uuid.UUID
	lastAmount int64
	lastReason string
	lastBy     *uuid.UUID
}

func (f *refundFake) Refund(ctx context.Context, id uuid.UUID, amount int64, reason string, by *uuid.UUID) (*service.RefundResult, error) {
	f.calls++
	f.lastID, f.lastAmount, f.lastReason, f.lastBy = id, amount, reason, by
	if f.err != nil {
		return nil, f.err
	}
	return &service.RefundResult{
		Refund:              &ledger.Refund{ID: uuid.New(), PaymentID: id, MerchantID: uuid.New(), Amount: amount, Reason: reason, CreatedBy: by},
		PaymentStatus:       payment.StatusPaid,
		RemainingRefundable: 500,
	}, nil
}

func (f *refundFake) ListRefunds(ctx context.Context, id uuid.UUID) (*ledger.RefundSummary, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return nil, f.err
	}
	return &ledger.RefundSummary{Items: []ledger.RefundItem{}, MerchantNetAmount: 9930, RemainingRefundable: 9930, Refundable: true}, nil
}

func (f *refundFake) Adjust(ctx context.Context, id uuid.UUID, amount int64, reason string, by *uuid.UUID) (*ledger.Entry, error) {
	f.calls++
	f.lastID, f.lastAmount, f.lastReason, f.lastBy = id, amount, reason, by
	if f.err != nil {
		return nil, f.err
	}
	return &ledger.Entry{ID: uuid.New(), MerchantID: id, EntryType: ledger.EntryAdjustment, Amount: amount, Note: reason, CreatedBy: by}, nil
}

func newRefundEnv() (*adminPayoutEnv, *refundFake) {
	e := newAdminPayoutEnv()
	f := &refundFake{}
	e.h.WithRefundService(f)
	return e, f
}

func TestAdminHandler_RefundPayment(t *testing.T) {
	id := uuid.NewString()
	cases := []struct {
		name    string
		body    string
		err     error
		want    int
		wantMsg string
	}{
		{"ok", `{"amount":3000,"reason":"dup"}`, nil, 201, ""},
		{"bad json", `{`, nil, 400, "Invalid request body"},
		{"float amount", `{"amount":1.5,"reason":"x"}`, nil, 400, "Invalid request body"},
		{"zero amount", `{"amount":0,"reason":"x"}`, ledger.ErrInvalidAmount, 400, "greater than 0"},
		{"no reason", `{"amount":5}`, ledger.ErrReasonRequired, 400, "A reason is required"},
		{"long reason", `{"amount":5,"reason":"x"}`, ledger.ErrTextTooLong, 400, "too long"},
		{"unknown payment", `{"amount":5,"reason":"x"}`, payment.ErrPaymentNotFound, 404, "Payment not found"},
		{"not refundable", `{"amount":5,"reason":"x"}`, ledger.ErrPaymentNotRefundable, 409, "cannot be refunded"},
		{"exceeds", `{"amount":5,"reason":"x"}`, ledger.ErrRefundExceedsNet, 422, "exceeds"},
		{"db error", `{"amount":5,"reason":"x"}`, errors.New("pq: secret detail"), 500, "Failed to record refund"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := newRefundEnv()
			f.err = tc.err
			rec := e.do(e.h.RefundPayment, http.MethodPost, "/x", tc.body, map[string]string{"id": id}, true)
			if rec.Code != tc.want {
				t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
			}
			if tc.wantMsg != "" && !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Fatalf("body = %s", rec.Body)
			}
			if strings.Contains(rec.Body.String(), "secret detail") {
				t.Fatal("raw error leaked")
			}
			if tc.want == 201 {
				if f.lastBy == nil || *f.lastBy != e.adminID || f.lastAmount != 3000 || f.lastID.String() != id {
					t.Fatalf("by=%v amount=%d id=%v", f.lastBy, f.lastAmount, f.lastID)
				}
				b := rec.Body.String()
				for _, k := range []string{`"refund"`, `"payment_status":"paid"`, `"remaining_refundable":500`} {
					if !strings.Contains(b, k) {
						t.Fatalf("missing %s in %s", k, b)
					}
				}
			}
		})
	}
}

func TestAdminHandler_RefundAndAdjustGuards(t *testing.T) {
	e, f := newRefundEnv()
	id := uuid.NewString()
	if rec := e.do(e.h.RefundPayment, http.MethodPost, "/x", `{"amount":5,"reason":"x"}`, map[string]string{"id": id}, false); rec.Code != 401 {
		t.Fatalf("refund no admin: %d", rec.Code)
	}
	if rec := e.do(e.h.AdjustMerchantBalance, http.MethodPost, "/x", `{"amount":5,"reason":"x"}`, map[string]string{"id": id}, false); rec.Code != 401 {
		t.Fatalf("adjust no admin: %d", rec.Code)
	}
	if rec := e.do(e.h.RefundPayment, http.MethodPost, "/x", `{"amount":5,"reason":"x"}`, map[string]string{"id": "nope"}, true); rec.Code != 400 {
		t.Fatalf("refund bad id: %d", rec.Code)
	}
	if rec := e.do(e.h.AdjustMerchantBalance, http.MethodPost, "/x", `{"amount":5,"reason":"x"}`, map[string]string{"id": "nope"}, true); rec.Code != 400 {
		t.Fatalf("adjust bad id: %d", rec.Code)
	}
	if f.calls != 0 {
		t.Fatalf("service called %d times", f.calls)
	}
}

func TestAdminHandler_AdjustMerchantBalance(t *testing.T) {
	id := uuid.NewString()
	cases := []struct {
		name    string
		body    string
		err     error
		want    int
		wantMsg string
	}{
		{"credit", `{"amount":5000,"reason":"goodwill"}`, nil, 201, ""},
		{"debit", `{"amount":-5000,"reason":"chargeback"}`, nil, 201, ""},
		{"bad json", `[]`, nil, 400, "Invalid request body"},
		{"zero", `{"amount":0,"reason":"x"}`, ledger.ErrInvalidAdjustment, 400, "non-zero"},
		{"no reason", `{"amount":5}`, ledger.ErrReasonRequired, 400, "A reason is required"},
		{"long", `{"amount":5,"reason":"x"}`, ledger.ErrTextTooLong, 400, "too long"},
		{"unknown merchant", `{"amount":5,"reason":"x"}`, merchant.ErrMerchantNotFound, 404, "Merchant not found"},
		{"db", `{"amount":5,"reason":"x"}`, errors.New("pq: secret detail"), 500, "Failed to record adjustment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := newRefundEnv()
			f.err = tc.err
			rec := e.do(e.h.AdjustMerchantBalance, http.MethodPost, "/x", tc.body, map[string]string{"id": id}, true)
			if rec.Code != tc.want {
				t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
			}
			if tc.wantMsg != "" && !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Fatalf("body = %s", rec.Body)
			}
			if strings.Contains(rec.Body.String(), "secret detail") {
				t.Fatal("raw error leaked")
			}
			if tc.want == 201 && (f.lastBy == nil || *f.lastBy != e.adminID || f.lastID.String() != id) {
				t.Fatalf("by=%v id=%v", f.lastBy, f.lastID)
			}
		})
	}
}

func TestAdminHandler_ListPaymentRefunds(t *testing.T) {
	id := uuid.NewString()
	e, f := newRefundEnv()
	rec := e.do(e.h.ListPaymentRefunds, http.MethodGet, "/x", "", map[string]string{"id": id}, true)
	if rec.Code != 200 || f.lastID.String() != id {
		t.Fatalf("ok: %d %s", rec.Code, rec.Body)
	}
	for _, k := range []string{`"items":[]`, `"merchant_net_amount":9930`, `"refunded_amount":0`, `"remaining_refundable":9930`, `"refundable":true`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("missing %s in %s", k, rec.Body)
		}
	}
	if rec := e.do(e.h.ListPaymentRefunds, http.MethodGet, "/x", "", map[string]string{"id": "zz"}, true); rec.Code != 400 {
		t.Fatalf("bad id: %d", rec.Code)
	}
	f.err = payment.ErrPaymentNotFound
	if rec := e.do(e.h.ListPaymentRefunds, http.MethodGet, "/x", "", map[string]string{"id": id}, true); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	f.err = errors.New("pq: secret detail")
	rec = e.do(e.h.ListPaymentRefunds, http.MethodGet, "/x", "", map[string]string{"id": id}, true)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret detail") {
		t.Fatalf("db: %d %s", rec.Code, rec.Body)
	}
}
