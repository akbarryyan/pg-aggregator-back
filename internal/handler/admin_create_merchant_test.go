package handler

import (
	"database/sql"
	"errors"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminHandler_CreateMerchant_PasswordErrors(t *testing.T) {
	cases := []struct {
		name, pw, msg string
	}{
		{"missing", ``, "password is required"},
		{"short", `"1234567"`, "password must be at least 8 characters"},
		{"long", `"` + strings.Repeat("a", 73) + `"`, "password must be at most 72 bytes"},
		{"long multi-byte", `"` + strings.Repeat("é", 37) + `"`, "password must be at most 72 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, mock := newFeeBearerAdminHandler(t)
			body := `{"name":"Acme","email":"a@example.com","business_name":"Acme"`
			if tc.pw != "" {
				body += `,"password":` + tc.pw
			}
			body += `}`
			rec := httptest.NewRecorder()
			h.CreateMerchant(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/merchants", strings.NewReader(body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.msg) {
				t.Errorf("want message %q, got %s", tc.msg, rec.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unexpected SQL: %v", err)
			}
		})
	}
}

func TestAdminHandler_CreateMerchant_OwnerFailureDoesNotLeak(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := service.NewAdminMerchantService(repository.NewMerchantRepository(db), nil, repository.NewMerchantUserRepository(db), nil)
	h := NewAdminHandler(nil, "http://localhost:3000").WithMerchantAndPaymentServices(nil, svc)

	now := time.Now()
	mid := uuid.New()
	mock.ExpectQuery(`FROM merchants`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`FROM merchant_users`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO merchants`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(mid, now, now))
	mock.ExpectQuery(`INSERT INTO merchant_users`).WillReturnError(errors.New(leakyDBError))
	mock.ExpectExec(`DELETE FROM merchants`).WillReturnResult(sqlmock.NewResult(0, 1))

	body := `{"name":"Acme","email":"a@example.com","business_name":"Acme","password":"s3cret-pass"}`
	rec := httptest.NewRecorder()
	h.CreateMerchant(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/merchants", strings.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	out := rec.Body.String()
	if strings.Contains(out, "s3cret-pass") || strings.Contains(out, "payments") || strings.Contains(out, "pq:") {
		t.Errorf("response leaks detail: %s", out)
	}
}
