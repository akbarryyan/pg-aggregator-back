package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

func seedHandlerSimPayment(t *testing.T, repo *fakePaymentRepo, merchantID uuid.UUID, env, providerName string) *payment.Payment {
	t.Helper()
	ref := "SBX-" + uuid.NewString()[:8]
	p := &payment.Payment{
		ID: uuid.New(), Reference: "PAY-" + ref, MerchantID: merchantID, Amount: 2000,
		Currency: payment.CurrencyIDR, PaymentMethod: payment.PaymentMethodQRIS,
		ProviderName: providerName, ProviderReference: &ref, Status: payment.StatusPending,
		Environment: env, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return p
}

func TestPaymentHandler_SimulateByReference(t *testing.T) {
	for _, tc := range []struct {
		name, env, provider, body string
		wantCode                  int
		wantStatus                string
	}{
		{"paid", payment.EnvironmentSandbox, "sandbox", `{"outcome":"paid"}`, http.StatusOK, "paid"},
		{"expired", payment.EnvironmentSandbox, "sandbox", `{"outcome":"expired"}`, http.StatusOK, "expired"},
		{"production forbidden", payment.EnvironmentProduction, "cashi", `{"outcome":"paid"}`, http.StatusForbidden, ""},
		{"bad outcome", payment.EnvironmentSandbox, "sandbox", `{"outcome":"refunded"}`, http.StatusBadRequest, ""},
		{"bad body", payment.EnvironmentSandbox, "sandbox", `not json`, http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newTestPaymentService()
			h := NewPaymentHandler(svc, "http://localhost:3000")
			p := seedHandlerSimPayment(t, repo, uuid.New(), tc.env, tc.provider)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/public/payments/by-reference/"+p.Reference+"/simulate", strings.NewReader(tc.body))
			req = mux.SetURLVars(req, map[string]string{"reference": p.Reference})
			rec := httptest.NewRecorder()
			h.SimulateByReference(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantStatus != "" {
				var resp map[string]interface{}
				_ = json.Unmarshal(rec.Body.Bytes(), &resp)
				if resp["status"] != tc.wantStatus || resp["reference"] != p.Reference {
					t.Fatalf("unexpected body: %s", rec.Body.String())
				}
				if _, leaked := resp["merchant_id"]; leaked {
					t.Fatalf("public response must keep the public field set: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestPaymentHandler_SimulateByReference_NotFoundAndConflict(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	call := func(ref string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"outcome":"paid"}`))
		req = mux.SetURLVars(req, map[string]string{"reference": ref})
		rec := httptest.NewRecorder()
		h.SimulateByReference(rec, req)
		return rec
	}
	if rec := call("PAY-NOPE"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown reference: %d", rec.Code)
	}
	p := seedHandlerSimPayment(t, repo, uuid.New(), payment.EnvironmentSandbox, "sandbox")
	if rec := call(p.Reference); rec.Code != http.StatusOK {
		t.Fatalf("first simulate: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(p.Reference); rec.Code != http.StatusConflict {
		t.Fatalf("second simulate must be 409, got %d", rec.Code)
	}
}

func TestMerchantHandler_SimulatePayment(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewMerchantHandler(svc, nil, nil, "http://localhost:3000")
	owner := uuid.New()
	p := seedHandlerSimPayment(t, repo, owner, payment.EnvironmentSandbox, "sandbox")

	call := func(merchantID uuid.UUID, body string) *httptest.ResponseRecorder {
		ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, merchantID)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/merchant/payments/"+p.ID.String()+"/simulate", strings.NewReader(body)).WithContext(ctx)
		req = mux.SetURLVars(req, map[string]string{"id": p.ID.String()})
		rec := httptest.NewRecorder()
		h.SimulatePayment(rec, req)
		return rec
	}

	if rec := call(uuid.New(), `{"outcome":"paid"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("other merchant must get 404, got %d", rec.Code)
	}
	if stored, _ := repo.GetByID(context.Background(), p.ID); stored.Status != payment.StatusPending {
		t.Fatalf("other merchant's call mutated payment to %s", stored.Status)
	}
	if rec := call(owner, `{"outcome":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad outcome must be 400, got %d", rec.Code)
	}
	rec := call(owner, `{"outcome":"paid"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner simulate: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["status"] != "paid" || resp["merchant_net_amount"] != float64(1986) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
