package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/paymentlink"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// secretUpstream stands in for Cashi's raw response body, which ends up inside
// the wrapped error (and therefore the logs) but must never reach a client.
const secretUpstream = `{"message":"internal cashi detail 4711"}`

func TestRespondCreatePaymentError_ProviderFailures(t *testing.T) {
	wrap := func(inner error) error {
		return fmt.Errorf("failed to create payment with available providers: %w",
			fmt.Errorf("failed to create QRIS payment: %w: %s", inner, secretUpstream))
	}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"provider answered with an error", wrap(providerPkg.ErrProviderAPIError), http.StatusBadGateway, "Payment provider error"},
		{"legacy provider error sentinel", wrap(payment.ErrProviderError), http.StatusBadGateway, "Payment provider error"},
		{"provider timed out", wrap(providerPkg.ErrProviderTimeout), http.StatusGatewayTimeout, "Payment provider timed out"},
		{"provider not available", wrap(providerPkg.ErrProviderNotAvailable), http.StatusServiceUnavailable, "Payment provider is temporarily unavailable"},
		{"fee not supported", wrap(providerPkg.ErrFeeNotSupported), http.StatusServiceUnavailable, "Fee calculation is not available for this payment provider"},
		{"merchant inactive", merchant.ErrMerchantInactive, http.StatusForbidden, "Merchant account is inactive"},
		{"unsupported method", providerPkg.ErrUnsupportedPaymentMethod, http.StatusBadRequest, "Unsupported payment method"},
		{"unknown error stays 500", errors.New("pq: relation does not exist"), http.StatusInternalServerError, "Failed to create payment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			respondCreatePaymentError(rec, tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("body %q does not contain %q", rec.Body.String(), tc.wantMsg)
			}
			for _, leak := range []string{"4711", "cashi detail", "pq:", "relation"} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("response leaks internal text %q: %s", leak, rec.Body.String())
				}
			}
		})
	}
}

// TestCreatePayment_ProviderFailureStatusPerPath drives every create-payment
// entry point end to end (real PaymentService, failing sandbox provider) and
// checks they all answer with the same status and a fixed generic message.
func TestCreatePayment_ProviderFailureStatusPerPath(t *testing.T) {
	failures := []struct {
		name       string
		inner      error
		wantStatus int
		wantMsg    string
	}{
		{"api error", providerPkg.ErrProviderAPIError, http.StatusBadGateway, "Payment provider error"},
		{"timeout", providerPkg.ErrProviderTimeout, http.StatusGatewayTimeout, "Payment provider timed out"},
		{"not available", providerPkg.ErrProviderNotAvailable, http.StatusServiceUnavailable, "Payment provider is temporarily unavailable"},
	}

	newSvc := func(createErr error) *service.PaymentService {
		router := providerPkg.NewProviderRouter()
		return service.NewPaymentService(newFakePaymentRepo(), &fakeMerchantProviderConfigRepo{},
			newFakeWebhookEventRepo(), router, "http://localhost:8080").
			WithSandboxProvider(&fakeProvider{name: "sandbox", createErr: createErr})
	}

	for _, f := range failures {
		createErr := fmt.Errorf("failed to create payment with available providers: %w",
			fmt.Errorf("failed to create QRIS payment: %w: %s", f.inner, secretUpstream))

		paths := map[string]func(svc *service.PaymentService) *httptest.ResponseRecorder{
			"api key": func(svc *service.PaymentService) *httptest.ResponseRecorder {
				mid := uuid.New()
				ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, mid)
				ctx = context.WithValue(ctx, middleware.MerchantEnvironmentContextKey, "sandbox")
				req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", paymentBody(t, mid)).WithContext(ctx)
				rec := httptest.NewRecorder()
				NewPaymentHandler(svc, "http://localhost:3000").CreatePayment(rec, req)
				return rec
			},
			"dashboard": func(svc *service.PaymentService) *httptest.ResponseRecorder {
				mid := uuid.New()
				return postMerchantCreate(NewMerchantHandler(svc, nil, nil, "http://localhost:3000"), mid, 50000)
			},
			"admin": func(svc *service.PaymentService) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/payments", paymentBody(t, uuid.New()))
				rec := httptest.NewRecorder()
				NewAdminHandler(svc, "http://localhost:3000").CreatePayment(rec, req)
				return rec
			},
			"payment link checkout": func(svc *service.PaymentService) *httptest.ResponseRecorder {
				amount := int64(50000)
				link := &paymentlink.PaymentLink{
					ID: uuid.New(), MerchantID: uuid.New(), Slug: "abc123", Title: "Test link",
					AmountType: paymentlink.AmountTypeFixed, Amount: &amount, Currency: "IDR",
					IsActive: true, Environment: "sandbox", CreatedAt: time.Now(), UpdatedAt: time.Now(),
				}
				linkSvc := service.NewPaymentLinkService(slugLinkRepo{link}, newFakePaymentRepo(), svc)
				req := httptest.NewRequest(http.MethodPost, "/api/v1/public/payment-links/abc123/checkout", strings.NewReader(`{}`))
				req = mux.SetURLVars(req, map[string]string{"slug": "abc123"})
				rec := httptest.NewRecorder()
				NewPaymentLinkHandler(linkSvc, "http://localhost:3000").InitiatePaymentLinkCheckout(rec, req)
				return rec
			},
		}

		for pathName, call := range paths {
			t.Run(f.name+"/"+pathName, func(t *testing.T) {
				rec := call(newSvc(createErr))
				if rec.Code != f.wantStatus {
					t.Fatalf("status = %d, want %d (body %s)", rec.Code, f.wantStatus, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), f.wantMsg) {
					t.Errorf("body %q does not contain %q", rec.Body.String(), f.wantMsg)
				}
				for _, leak := range []string{"4711", "cashi detail", "failed to create"} {
					if strings.Contains(rec.Body.String(), leak) {
						t.Errorf("response leaks internal text %q: %s", leak, rec.Body.String())
					}
				}
			})
		}
	}
}
