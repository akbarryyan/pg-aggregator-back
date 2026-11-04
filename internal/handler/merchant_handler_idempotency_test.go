package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/google/uuid"
)

// postMerchantCreate calls the dashboard handler as an authenticated merchant.
func postMerchantCreate(h *MerchantHandler, merchantID uuid.UUID, amount int, keys ...string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]interface{}{
		"amount": amount, "payment_method": "qris", "description": "idem", "environment": "sandbox",
	})
	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, merchantID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/merchant/payments", bytes.NewReader(raw)).WithContext(ctx)
	for _, k := range keys {
		req.Header.Add("Idempotency-Key", k)
	}
	rec := httptest.NewRecorder()
	h.CreatePayment(rec, req)
	return rec
}

func newIdemMerchantHandler() (*MerchantHandler, *fakeIdempotencyRepo, *fakePaymentRepo) {
	svc, repo, idem := newTestPaymentServiceWithIdem()
	return NewMerchantHandler(svc, nil, nil, "http://localhost:3000"), idem, repo
}

func TestMerchantHandler_CreatePayment_IdempotencyKeyReplays(t *testing.T) {
	h, _, repo := newIdemMerchantHandler()
	m := uuid.New()

	first := postMerchantCreate(h, m, 50000, "key-1")
	second := postMerchantCreate(h, m, 50000, "key-1")
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("want 201/201, got %d/%d: %s", first.Code, second.Code, second.Body.String())
	}
	var a, b map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if a["reference"] == nil || a["reference"] != b["reference"] {
		t.Errorf("replay must return the same reference: %v vs %v", a["reference"], b["reference"])
	}
	if first.Header().Get("Idempotent-Replayed") != "" {
		t.Error("the original response must not carry Idempotent-Replayed")
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("the replay must carry Idempotent-Replayed: true")
	}
	if len(repo.byID) != 1 {
		t.Errorf("want 1 payment, got %d", len(repo.byID))
	}
}

func TestMerchantHandler_CreatePayment_IdempotencyKeyReusedWithDifferentBody(t *testing.T) {
	h, _, _ := newIdemMerchantHandler()
	m := uuid.New()

	postMerchantCreate(h, m, 50000, "key-1")
	if rec := postMerchantCreate(h, m, 60000, "key-1"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMerchantHandler_CreatePayment_InvalidIdempotencyKey(t *testing.T) {
	h, _, repo := newIdemMerchantHandler()
	m := uuid.New()

	cases := map[string][]string{
		"space":      {"has space"},
		"too long":   {strings.Repeat("a", 129)},
		"empty":      {""},
		"two values": {"key-1", "key-2"},
	}
	for name, keys := range cases {
		if rec := postMerchantCreate(h, m, 50000, keys...); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d: %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(repo.byID) != 0 {
		t.Errorf("rejected keys must not create payments, got %d", len(repo.byID))
	}
	if rec := postMerchantCreate(h, m, 50000); rec.Code != http.StatusCreated {
		t.Errorf("absent header: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMerchantHandler_CreatePayment_InProgressIs409(t *testing.T) {
	h, idem, repo := newIdemMerchantHandler()
	m := uuid.New()

	if rec := postMerchantCreate(h, m, 50000, "key-1"); rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	idem.forgetPayments() // same hash, no payment: another request is still working
	rec := postMerchantCreate(h, m, 50000, "key-1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(repo.byID) != 1 {
		t.Errorf("in progress must not create another payment, got %d", len(repo.byID))
	}
}
