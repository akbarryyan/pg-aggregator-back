package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestPaymentHandler_GetQRImage(t *testing.T) {
	pngBytes := tinyPNG(t)
	pngURI := payment.SandboxQRPNGPrefix + base64.StdEncoding.EncodeToString(pngBytes)
	corrupt := payment.SandboxQRPNGPrefix + "%%%"
	text := "00020101021226"

	cases := []struct {
		name   string
		env    string
		qr     *string
		ref    string // override lookup reference ("" = seeded one)
		wantOK bool
	}{
		{"sandbox png", payment.EnvironmentSandbox, &pngURI, "", true},
		{"production with png data", payment.EnvironmentProduction, &pngURI, "", false},
		{"unknown reference", payment.EnvironmentSandbox, &pngURI, "PAY-NOPE", false},
		{"sandbox without qris data", payment.EnvironmentSandbox, nil, "", false},
		{"sandbox corrupt base64", payment.EnvironmentSandbox, &corrupt, "", false},
		{"sandbox non-png data", payment.EnvironmentSandbox, &text, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newTestPaymentService()
			h := NewPaymentHandler(svc, "http://localhost:3000")
			p := &payment.Payment{
				ID: uuid.New(), Reference: "PAY-QR-" + uuid.NewString()[:8], MerchantID: uuid.New(), Amount: 2000,
				Currency: payment.CurrencyIDR, PaymentMethod: payment.PaymentMethodQRIS, Status: payment.StatusPending,
				Environment: tc.env, QRISData: tc.qr, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
			}
			if err := repo.Create(context.Background(), p); err != nil {
				t.Fatalf("seed: %v", err)
			}
			ref := p.Reference
			if tc.ref != "" {
				ref = tc.ref
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/public/payments/by-reference/"+ref+"/qr.png", nil)
			req = mux.SetURLVars(req, map[string]string{"reference": ref})
			rec := httptest.NewRecorder()
			h.GetQRImage(rec, req)

			if tc.wantOK {
				if rec.Code != http.StatusOK {
					t.Fatalf("code = %d, want 200: %s", rec.Code, rec.Body.String())
				}
				if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
					t.Errorf("Content-Type = %q", ct)
				}
				if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
					t.Errorf("Cache-Control = %q", cc)
				}
				if !bytes.Equal(rec.Body.Bytes(), pngBytes) {
					t.Errorf("body is not the decoded PNG bytes")
				}
				return
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("code = %d, want 404: %s", rec.Code, rec.Body.String())
			}
			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode 404 body: %v: %s", err, rec.Body.String())
			}
			if resp["message"] != "Payment QR not found" {
				t.Errorf("unexpected 404 body: %s", rec.Body.String())
			}
		})
	}
}

func TestPaymentHandler_CreatePayment_SandboxHasQRImageURL(t *testing.T) {
	svc, _ := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	raw, _ := json.Marshal(map[string]interface{}{
		"merchant_id": uuid.New().String(), "amount": 50000, "payment_method": "qris",
		"description": "QR url", "environment": "sandbox",
	})
	rec := httptest.NewRecorder()
	h.CreatePayment(rec, httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(raw)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	want := "http://localhost:8080/api/v1/public/payments/by-reference/" + resp["reference"].(string) + "/qr.png"
	if resp["qr_image_url"] != want {
		t.Fatalf("qr_image_url = %v, want %s", resp["qr_image_url"], want)
	}
}

func TestPaymentHandler_GetPaymentByReference_ProductionHasNoQRImageURL(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	uri := payment.SandboxQRPNGPrefix + base64.StdEncoding.EncodeToString(tinyPNG(t))
	if err := repo.Create(context.Background(), &payment.Payment{
		Reference: "PAY-PRODQR", MerchantID: uuid.New(), Amount: 2000, Currency: payment.CurrencyIDR,
		Status: payment.StatusPending, Environment: payment.EnvironmentProduction, QRISData: &uri,
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	req := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/x", nil), map[string]string{"reference": "PAY-PRODQR"})
	rec := httptest.NewRecorder()
	h.GetPaymentByReference(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if resp["reference"] != "PAY-PRODQR" {
		t.Fatalf("reference = %v, want PAY-PRODQR: %s", resp["reference"], rec.Body.String())
	}
	if _, ok := resp["qr_image_url"]; ok {
		t.Fatalf("production payment must not expose qr_image_url: %s", rec.Body.String())
	}
}
