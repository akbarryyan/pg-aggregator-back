package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

var feeBearerCols = []string{
	"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "fee_bearer", "created_at", "updated_at",
}

func newFeeBearerAdminHandler(t *testing.T) (*AdminHandler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := service.NewAdminMerchantService(repository.NewMerchantRepository(db), nil, nil, nil)
	return NewAdminHandler(nil, "http://localhost:3000").WithMerchantAndPaymentServices(nil, svc), mock
}

func doSetFeeBearer(h *AdminHandler, idVar, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/merchants/"+idVar+"/fee-bearer", strings.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"id": idVar})
	rec := httptest.NewRecorder()
	h.SetMerchantFeeBearer(rec, req)
	return rec
}

func TestAdminHandler_SetMerchantFeeBearer(t *testing.T) {
	t.Run("valid value returns 200 with new fee_bearer", func(t *testing.T) {
		h, mock := newFeeBearerAdminHandler(t)
		id := uuid.New()
		now := time.Now()
		mock.ExpectExec(`UPDATE merchants SET fee_bearer`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`FROM merchants`).WillReturnRows(sqlmock.NewRows(feeBearerCols).
			AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "customer", now, now))

		rec := doSetFeeBearer(h, id.String(), `{"fee_bearer":"customer"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["fee_bearer"] != "customer" {
			t.Errorf("expected fee_bearer customer, got %v", resp["fee_bearer"])
		}
	})
	t.Run("invalid value is 400", func(t *testing.T) {
		h, mock := newFeeBearerAdminHandler(t)
		rec := doSetFeeBearer(h, uuid.New().String(), `{"fee_bearer":"x"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("repo touched: %v", err)
		}
	})
	t.Run("missing field is 400", func(t *testing.T) {
		h, _ := newFeeBearerAdminHandler(t)
		rec := doSetFeeBearer(h, uuid.New().String(), `{}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})
	t.Run("non-UUID id is 400", func(t *testing.T) {
		h, _ := newFeeBearerAdminHandler(t)
		rec := doSetFeeBearer(h, "not-a-uuid", `{"fee_bearer":"customer"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})
	t.Run("malformed body is 400", func(t *testing.T) {
		h, _ := newFeeBearerAdminHandler(t)
		rec := doSetFeeBearer(h, uuid.New().String(), `{not json`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})
	t.Run("unknown merchant is 404", func(t *testing.T) {
		h, mock := newFeeBearerAdminHandler(t)
		mock.ExpectExec(`UPDATE merchants SET fee_bearer`).WillReturnResult(sqlmock.NewResult(0, 0))
		rec := doSetFeeBearer(h, uuid.New().String(), `{"fee_bearer":"customer"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("unknown repo error is generic 500", func(t *testing.T) {
		h, mock := newFeeBearerAdminHandler(t)
		mock.ExpectExec(`UPDATE merchants SET fee_bearer`).WillReturnError(errors.New(leakyDBError))
		rec := doSetFeeBearer(h, uuid.New().String(), `{"fee_bearer":"customer"}`)
		assertNoLeak(t, rec, http.StatusInternalServerError)
	})
}

// A merchant must not be able to change who pays the fee through their own
// PUT /merchant/business: the unknown "fee_bearer" key is ignored (like any
// unknown JSON field there), no 500, and the UPDATE statement must not touch
// fee_bearer at all.
func TestMerchantHandler_UpdateBusiness_CannotChangeFeeBearer(t *testing.T) {
	h, mock := newBusinessHandler(t)
	id := uuid.New()
	now := time.Now()
	row := func() *sqlmock.Rows {
		return sqlmock.NewRows(feeBearerCols).
			AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "merchant", now, now)
	}
	// GetByID in repo.Update, then the UPDATE whose SET list is pinned to the
	// four merchant-editable columns (a fee_bearer assignment would not match).
	mock.ExpectQuery(`FROM merchants`).WillReturnRows(row())
	mock.ExpectQuery(`UPDATE merchants\s+SET name = \$1, phone = \$2, business_name = \$3, webhook_url = \$4, updated_at = \$5\s+WHERE id = \$6`).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(now))

	rec := doUpdateBusiness(h, `{"name":"Acme","fee_bearer":"customer"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (unknown field ignored), got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["fee_bearer"] != "merchant" {
		t.Errorf("fee_bearer must remain merchant, got %v", resp["fee_bearer"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAdminHandler_SetMerchantFeeBearer_LogsAuditLine(t *testing.T) {
	var buf bytes.Buffer
	logger.Init("info")
	logger.SetOutput(&buf, &buf)
	t.Cleanup(func() { logger.SetOutput(os.Stdout, os.Stderr) })

	h, mock := newFeeBearerAdminHandler(t)
	id, adminID := uuid.New(), uuid.New()
	now := time.Now()
	mock.ExpectExec(`UPDATE merchants SET fee_bearer`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM merchants`).WillReturnRows(sqlmock.NewRows(feeBearerCols).
		AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, "customer", now, now))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/merchants/"+id.String()+"/fee-bearer", strings.NewReader(`{"fee_bearer":"customer"}`))
	req = mux.SetURLVars(req, map[string]string{"id": id.String()})
	req = req.WithContext(context.WithValue(req.Context(), middleware.AdminIDContextKey, adminID))
	rec := httptest.NewRecorder()
	h.SetMerchantFeeBearer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	out := buf.String()
	for _, want := range []string{id.String(), adminID.String(), "fee_bearer=customer"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log missing %q; got %q", want, out)
		}
	}
}
