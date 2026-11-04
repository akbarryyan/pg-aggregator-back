package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider/sandbox"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const leakyDBError = `pq: relation "payments" does not exist`

// failingCreateRepo makes the payment INSERT fail with raw driver text, the
// kind of error that must never be echoed to an API client.
type failingCreateRepo struct {
	*fakePaymentRepo
}

func (failingCreateRepo) Create(ctx context.Context, p *payment.Payment) error {
	return errors.New(leakyDBError)
}

func newFailingPaymentService() *service.PaymentService {
	router := providerPkg.NewProviderRouter()
	sb := sandbox.NewAdapter()
	router.RegisterProvider(sb)
	return service.NewPaymentService(
		failingCreateRepo{newFakePaymentRepo()},
		&fakeMerchantProviderConfigRepo{},
		newFakeWebhookEventRepo(),
		router,
		"http://localhost:8080",
	).WithSandboxProvider(sb)
}

func assertNoLeak(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("expected %d, got %d: %s", wantStatus, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, bad := range []string{"pq:", "relation", "payments", "does not exist"} {
		if strings.Contains(body, bad) {
			t.Fatalf("response leaks internal error text %q: %s", bad, body)
		}
	}
}

func paymentBody(t *testing.T, merchantID uuid.UUID) *bytes.Reader {
	t.Helper()
	raw, _ := json.Marshal(map[string]interface{}{
		"merchant_id":    merchantID.String(),
		"amount":         50000,
		"payment_method": "qris",
		"description":    "Test payment",
		"environment":    "sandbox",
	})
	return bytes.NewReader(raw)
}

func TestMerchantHandler_CreatePayment_InternalErrorIsGeneric(t *testing.T) {
	h := NewMerchantHandler(newFailingPaymentService(), nil, nil, "http://localhost:3000")
	mid := uuid.New()
	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, mid)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/merchant/payments", paymentBody(t, mid)).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	assertNoLeak(t, rec, http.StatusInternalServerError)
}

func TestAdminHandler_CreatePayment_InternalErrorIsGeneric(t *testing.T) {
	h := NewAdminHandler(newFailingPaymentService(), "http://localhost:3000")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/payments", paymentBody(t, uuid.New()))
	rec := httptest.NewRecorder()

	h.CreatePayment(rec, req)

	assertNoLeak(t, rec, http.StatusInternalServerError)
}

func newBusinessHandler(t *testing.T) (*MerchantHandler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := service.NewAdminMerchantService(repository.NewMerchantRepository(db), nil, nil)
	h := NewMerchantHandler(nil, nil, nil, "http://localhost:3000").
		WithMerchantAndPaymentServices(nil, svc)
	return h, mock
}

func doUpdateBusiness(h *MerchantHandler, body string) *httptest.ResponseRecorder {
	ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, uuid.New())
	req := httptest.NewRequest(http.MethodPut, "/api/v1/merchant/business", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.UpdateBusiness(rec, req)
	return rec
}

func TestMerchantHandler_UpdateBusiness_ErrorMapping(t *testing.T) {
	t.Run("unknown db error is generic 500", func(t *testing.T) {
		h, mock := newBusinessHandler(t)
		mock.ExpectQuery(`FROM merchants`).WillReturnError(errors.New(leakyDBError))
		rec := doUpdateBusiness(h, `{"name":"Acme"}`)
		assertNoLeak(t, rec, http.StatusInternalServerError)
	})
	t.Run("known validation error stays 400 with message", func(t *testing.T) {
		h, _ := newBusinessHandler(t)
		rec := doUpdateBusiness(h, `{"name":"   "}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), merchant.ErrMerchantNameRequired.Error()) {
			t.Fatalf("expected 400 with domain message, got %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("business name validation stays 400", func(t *testing.T) {
		h, _ := newBusinessHandler(t)
		rec := doUpdateBusiness(h, `{"business_name":""}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), merchant.ErrBusinessNameRequired.Error()) {
			t.Fatalf("expected 400 with domain message, got %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("missing merchant is 404", func(t *testing.T) {
		h, mock := newBusinessHandler(t)
		mock.ExpectQuery(`FROM merchants`).WillReturnRows(sqlmock.NewRows(nil))
		rec := doUpdateBusiness(h, `{"name":"Acme"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestAdminHandler_UpdateMerchant_UnknownErrorIsGeneric(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`FROM merchants`).WillReturnError(errors.New(leakyDBError))
	svc := service.NewAdminMerchantService(repository.NewMerchantRepository(db), nil, nil)
	h := NewAdminHandler(nil, "http://localhost:3000").WithMerchantAndPaymentServices(nil, svc)

	id := uuid.New()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/merchants/"+id.String(), strings.NewReader(`{"name":"Acme"}`))
	req = mux.SetURLVars(req, map[string]string{"id": id.String()})
	rec := httptest.NewRecorder()
	h.UpdateMerchant(rec, req)

	assertNoLeak(t, rec, http.StatusInternalServerError)
}

func TestRespondValidationOrInternal(t *testing.T) {
	known := errors.New("known validation")
	wrapped := errors.Join(errors.New("ctx"), known)

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"known", known, http.StatusBadRequest},
		{"wrapped known", wrapped, http.StatusBadRequest},
		{"unknown", errors.New(leakyDBError), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			respondValidationOrInternal(rec, tc.err, known)
			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, rec.Code)
			}
			if tc.want == http.StatusInternalServerError {
				assertNoLeak(t, rec, tc.want)
			}
		})
	}
}
