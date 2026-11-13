package main

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
	"github.com/akbarryyan/pg-aggregator-back/internal/handler"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/gorilla/mux"
)

type memActivityWriter struct {
	mu      sync.Mutex
	entries []activity.Entry
}

func (w *memActivityWriter) Insert(ctx context.Context, e *activity.Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, *e)
	return nil
}

func (w *memActivityWriter) snapshot() []activity.Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]activity.Entry(nil), w.entries...)
}

// Every admin/merchant/auth route in the real router must have an action
// label, otherwise the activity log would show a raw route for it.
func TestActivity_EveryAuthAdminMerchantRouteHasLabel(t *testing.T) {
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	r := newRouter(nil, nil, handler.NewAuthHandler(nil), nil, nil, nil, nil, nil,
		limiter, limiter, limiter, limiter, func(context.Context) error { return nil })
	checked := 0
	err := r.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		tpl, err := route.GetPathTemplate()
		if err != nil {
			return nil
		}
		methods, err := route.GetMethods()
		if err != nil || len(methods) == 0 {
			return nil
		}
		if !strings.HasPrefix(tpl, "/api/v1/admin") && !strings.HasPrefix(tpl, "/api/v1/merchant") && !strings.HasPrefix(tpl, "/api/v1/auth") {
			return nil
		}
		for _, m := range methods {
			checked++
			if !activity.Known(m, tpl) {
				t.Errorf("no activity label for %s %s", m, tpl)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 60 {
		t.Fatalf("walked only %d routes; router walk looks broken", checked)
	}
}

func newActivityEnv(t *testing.T) (*sessEnv, http.Handler, *activity.Recorder, *memActivityWriter) {
	t.Helper()
	e := newSessEnv(t)
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	adminPayments := service.NewAdminPaymentService(repository.NewPaymentRepository(db), repository.NewMerchantRepository(db))
	adminHandler := handler.NewAdminHandler(nil, "http://localhost:3000").WithMerchantAndPaymentServices(adminPayments, nil)
	w := &memActivityWriter{}
	rec := activity.NewRecorder(w, 64)
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	router := setupRouter(nil, nil, handler.NewAuthHandler(e.authSvc), adminHandler, nil, nil,
		middleware.NewAuthMiddleware(e.authSvc), nil, limiter, limiter, limiter, limiter,
		func(context.Context) error { return nil },
		withActivity(rec, nil, nil))
	return e, router, rec, w
}

func flush(t *testing.T, rec *activity.Recorder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rec.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func serveJSON(router http.Handler, method, path, token string, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestActivity_AdminRequestIsRecorded(t *testing.T) {
	e, router, rec, w := newActivityEnv(t)
	serveJSON(router, http.MethodGet, "/api/v1/admin/payments", e.adminToken(t, time.Now().UTC()), "", nil)
	flush(t, rec)
	got := w.snapshot()
	if len(got) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(got), got)
	}
	if got[0].ActorType != activity.ActorAdmin || got[0].Action != "Viewed payments" {
		t.Errorf("entry = %+v", got[0])
	}
	if got[0].ActorID == nil || *got[0].ActorID != e.admin.ID {
		t.Errorf("actor id = %v, want %v", got[0].ActorID, e.admin.ID)
	}
}

func TestActivity_BackgroundRequestIsNotRecorded(t *testing.T) {
	e, router, rec, w := newActivityEnv(t)
	serveJSON(router, http.MethodGet, "/api/v1/admin/payments", e.adminToken(t, time.Now().UTC()), "",
		map[string]string{activity.BackgroundHeader: "1"})
	flush(t, rec)
	if got := w.snapshot(); len(got) != 0 {
		t.Fatalf("entries = %d, want 0", len(got))
	}
}

func TestActivity_UnauthenticatedAdminRequestIsNotRecorded(t *testing.T) {
	_, router, rec, w := newActivityEnv(t)
	serveJSON(router, http.MethodGet, "/api/v1/admin/payments", "", "", nil)
	flush(t, rec)
	if got := w.snapshot(); len(got) != 0 {
		t.Fatalf("entries = %d, want 0 (rejected by auth before the activity middleware)", len(got))
	}
}

func TestActivity_FailedAdminLoginIsAnonymousWithEmailNoPassword(t *testing.T) {
	_, router, rec, w := newActivityEnv(t)
	r := serveJSON(router, http.MethodPost, "/api/v1/auth/admin/login", "",
		`{"email":"admin@example.com","password":"super-secret-wrong"}`, nil)
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("login status = %d", r.Code)
	}
	flush(t, rec)
	got := w.snapshot()
	if len(got) != 1 {
		t.Fatalf("entries = %d, want 1", len(got))
	}
	if got[0].ActorType != activity.ActorAnonymous || got[0].ActorEmail != "admin@example.com" || got[0].StatusCode != 401 {
		t.Errorf("entry = %+v", got[0])
	}
	b, _ := json.Marshal(got[0])
	if strings.Contains(string(b), "super-secret-wrong") {
		t.Errorf("password leaked into entry: %s", b)
	}
}

func TestActivity_SuccessfulLoginsAndMeSetActor(t *testing.T) {
	e, router, rec, w := newActivityEnv(t)
	serveJSON(router, http.MethodPost, "/api/v1/auth/admin/login", "", `{"email":"admin@example.com","password":"correct-password"}`, nil)
	serveJSON(router, http.MethodPost, "/api/v1/auth/login", "", `{"email":"user@example.com","password":"correct-password"}`, nil)
	serveJSON(router, http.MethodGet, "/api/v1/auth/admin/me", e.adminToken(t, time.Now().UTC()), "", nil)
	serveJSON(router, http.MethodGet, "/api/v1/auth/me", e.merchantToken(t, time.Now().UTC()), "", nil)
	flush(t, rec)
	got := w.snapshot()
	if len(got) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(got), got)
	}
	want := []struct{ actor, email string }{
		{activity.ActorAdmin, "admin@example.com"},
		{activity.ActorMerchantUser, "user@example.com"},
		{activity.ActorAdmin, "admin@example.com"},
		{activity.ActorMerchantUser, "user@example.com"},
	}
	for i, wnt := range want {
		if got[i].ActorType != wnt.actor || got[i].ActorEmail != wnt.email || got[i].ActorID == nil {
			t.Errorf("entry %d = %+v, want actor %s email %s", i, got[i], wnt.actor, wnt.email)
		}
	}
	if got[1].MerchantID == nil || *got[1].MerchantID != e.merchant.ID {
		t.Errorf("merchant login merchant_id = %v", got[1].MerchantID)
	}
}

func TestActivity_UnresolvedActorOnNonLoginAuthRoutesRecordsNothing(t *testing.T) {
	e, router, rec, w := newActivityEnv(t)
	_ = e
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/admin/me"},
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodPost, "/api/v1/auth/admin/logout"},
		{http.MethodPut, "/api/v1/auth/profile"},
		{http.MethodPost, "/api/v1/auth/change-password"},
	} {
		serveJSON(router, rt.method, rt.path, "", "{}", nil)
		serveJSON(router, rt.method, rt.path, "bad-token", "{}", nil)
	}
	// Failed registration (400, invalid body) must not be logged either.
	if r := serveJSON(router, http.MethodPost, "/api/v1/auth/register", "", `{}`, nil); r.Code != http.StatusBadRequest && r.Code < 400 {
		t.Fatalf("register status = %d", r.Code)
	}
	flush(t, rec)
	if got := w.snapshot(); len(got) != 0 {
		t.Fatalf("entries = %d, want 0: %+v", len(got), got)
	}
}

func TestActivity_FailedMerchantLoginStillAnonymous(t *testing.T) {
	_, router, rec, w := newActivityEnv(t)
	serveJSON(router, http.MethodPost, "/api/v1/auth/login", "", `{"email":"user@example.com","password":"nope"}`, nil)
	flush(t, rec)
	got := w.snapshot()
	if len(got) != 1 || got[0].ActorType != activity.ActorAnonymous || got[0].ActorEmail != "user@example.com" {
		t.Fatalf("entries = %+v", got)
	}
}

func TestActivity_AdminActivityRoutesAuth(t *testing.T) {
	e := newSessEnv(t)
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	adminHandler := handler.NewAdminHandler(nil, "http://localhost:3000").
		WithActivityLogs(service.NewActivityLogService(repository.NewActivityLogRepository(db)))
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	router := setupRouter(nil, nil, handler.NewAuthHandler(e.authSvc), adminHandler, nil, nil,
		middleware.NewAuthMiddleware(e.authSvc), nil, limiter, limiter, limiter, limiter,
		func(context.Context) error { return nil })
	for _, p := range []string{"/api/v1/admin/activity-logs", "/api/v1/admin/activity-logs/stats", "/api/v1/admin/activity-logs/export"} {
		if rec := serveJSON(router, http.MethodGet, p, "", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s no token = %d", p, rec.Code)
		}
		if rec := serveJSON(router, http.MethodGet, p, e.merchantToken(t, time.Now().UTC()), "", nil); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Errorf("%s merchant token = %d", p, rec.Code)
		}
		// Admin reaches the handler (sqlmock has no expectations, so the
		// handler's own generic 500 proves routing, not a 404/405).
		if rec := serveJSON(router, http.MethodGet, p, e.adminToken(t, time.Now().UTC()), "", nil); rec.Code == http.StatusNotFound || rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s admin token = %d", p, rec.Code)
		}
	}
}
