package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/handler"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Session-revocation tests over the real router. /auth/me and friends parse
// the JWT inside AuthHandler (no middleware), while /merchant/* and /admin/*
// go through RequireMerchant/RequireAdmin, so both enforcement paths are
// covered here.

const sessSecret = "session-test-secret"

type sessAdminRepo struct {
	mu sync.Mutex
	a  *admin.Admin
}

func (r *sessAdminRepo) GetByEmail(ctx context.Context, email string) (*admin.Admin, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.a.Email == email {
		cp := *r.a
		return &cp, nil
	}
	return nil, admin.ErrAdminNotFound
}
func (r *sessAdminRepo) GetByID(ctx context.Context, id uuid.UUID) (*admin.Admin, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.a.ID != id {
		return nil, admin.ErrAdminNotFound
	}
	cp := *r.a
	return &cp, nil
}
func (r *sessAdminRepo) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	return nil
}
func (r *sessAdminRepo) UpdatePasswordHash(ctx context.Context, id uuid.UUID, h string) error {
	return nil
}
func (r *sessAdminRepo) ExistsByEmailExceptID(ctx context.Context, e string, id uuid.UUID) (bool, error) {
	return false, nil
}
func (r *sessAdminRepo) UpdateProfile(ctx context.Context, id uuid.UUID, n, e string) (*admin.Admin, error) {
	return nil, admin.ErrAdminNotFound
}
func (r *sessAdminRepo) RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.a.SessionsValidAfter = &at
	return nil
}

type sessMerchantRepo struct {
	mu sync.Mutex
	m  *merchant.Merchant
}

func (r *sessMerchantRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *r.m
	return &cp, nil
}
func (r *sessMerchantRepo) GetByEmail(ctx context.Context, e string) (*merchant.Merchant, error) {
	return nil, merchant.ErrMerchantNotFound
}
func (r *sessMerchantRepo) Create(ctx context.Context, req *merchant.CreateMerchantRequest) (*merchant.Merchant, error) {
	return nil, nil
}
func (r *sessMerchantRepo) Delete(ctx context.Context, id uuid.UUID) error { return nil }

type sessUserRepo struct {
	mu sync.Mutex
	u  *merchant.User
}

func (r *sessUserRepo) GetByEmail(ctx context.Context, email string) (*merchant.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.u.Email == email {
		cp := *r.u
		return &cp, nil
	}
	return nil, merchant.ErrMerchantUserNotFound
}
func (r *sessUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.u.ID != id {
		return nil, merchant.ErrMerchantUserNotFound
	}
	cp := *r.u
	return &cp, nil
}
func (r *sessUserRepo) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	return nil
}
func (r *sessUserRepo) UpdateProfile(ctx context.Context, id uuid.UUID, n, e string) error {
	return nil
}
func (r *sessUserRepo) UpdatePasswordHash(ctx context.Context, id uuid.UUID, h string) error {
	return nil
}
func (r *sessUserRepo) Create(ctx context.Context, u *merchant.User) (*merchant.User, error) {
	return nil, nil
}
func (r *sessUserRepo) RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.u.SessionsValidAfter = &at
	return nil
}

type sessEnv struct {
	router   http.Handler
	authSvc  *service.AuthService
	admin    *admin.Admin
	aRepo    *sessAdminRepo
	user     *merchant.User
	uRepo    *sessUserRepo
	merchant *merchant.Merchant
	mRepo    *sessMerchantRepo
}

func newSessEnv(t *testing.T) *sessEnv {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	m := &merchant.Merchant{ID: uuid.New(), Email: "shop@example.com", IsActive: true}
	u := &merchant.User{ID: uuid.New(), MerchantID: m.ID, Name: "U", Email: "user@example.com", PasswordHash: string(hash), Role: "owner", IsActive: true}
	a := &admin.Admin{ID: uuid.New(), Name: "A", Email: "admin@example.com", PasswordHash: string(hash), IsActive: true}
	aRepo, uRepo, mRepo := &sessAdminRepo{a: a}, &sessUserRepo{u: u}, &sessMerchantRepo{m: m}

	authSvc := service.NewAuthService(aRepo, sessSecret).WithMerchantAuth(uRepo, mRepo)
	limiter := middleware.NewIPRateLimiter(1000, 1000)
	// merchant/admin/payment handlers stay nil: these tests only reach routes
	// that are rejected before the handler (or are served by AuthHandler).
	router := setupRouter(nil, nil, handler.NewAuthHandler(authSvc), nil, nil, nil,
		middleware.NewAuthMiddleware(authSvc), nil, limiter, limiter, limiter, limiter,
		func(context.Context) error { return nil })
	return &sessEnv{router: router, authSvc: authSvc, admin: a, aRepo: aRepo, user: u, uRepo: uRepo, merchant: m, mRepo: mRepo}
}

// mint signs a token with an explicit iat so tests can place it in an earlier
// second than a later revocation (iat has one-second precision).
func (e *sessEnv) merchantToken(t *testing.T, iat time.Time) string {
	t.Helper()
	claims := service.MerchantClaims{
		UserID: e.user.ID, MerchantID: e.merchant.ID, Email: e.user.Email, Role: "owner",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "pg-aggregator", Audience: []string{"merchant"}, Subject: e.user.ID.String(),
			IssuedAt: jwt.NewNumericDate(iat), ExpiresAt: jwt.NewNumericDate(iat.Add(time.Hour)),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(sessSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *sessEnv) adminToken(t *testing.T, iat time.Time) string {
	t.Helper()
	claims := service.AdminClaims{
		AdminID: e.admin.ID, Email: e.admin.Email, Role: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "pg-aggregator", Audience: []string{"admin"}, Subject: e.admin.ID.String(),
			IssuedAt: jwt.NewNumericDate(iat), ExpiresAt: jwt.NewNumericDate(iat.Add(time.Hour)),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(sessSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *sessEnv) do(method, path, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *sessEnv) login(t *testing.T, path, email string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": "correct-password"})
	rec := e.do(http.MethodPost, path, "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", path, rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Token == "" {
		t.Fatalf("login %s: bad body %s", path, rec.Body.String())
	}
	return resp.Token
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, code int, what string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("%s: status = %d, want %d (%s)", what, rec.Code, code, strings.TrimSpace(rec.Body.String()))
	}
}

func TestSession_MerchantLogoutRevokesOldTokens(t *testing.T) {
	e := newSessEnv(t)
	old := e.merchantToken(t, time.Now().UTC().Add(-5*time.Second))

	wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/me", old, nil), http.StatusOK, "/auth/me before logout")
	wantStatus(t, e.do(http.MethodPost, "/api/v1/auth/logout", old, nil), http.StatusOK, "logout")

	// Handler-authenticated route (no middleware).
	wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/me", old, nil), http.StatusUnauthorized, "/auth/me after logout")
	// Middleware-authenticated route; rejected before reaching the (nil) handler.
	wantStatus(t, e.do(http.MethodGet, "/api/v1/merchant/dashboard/summary", old, nil), http.StatusUnauthorized, "merchant route after logout")

	// A fresh login right after logout works (same-second token survives).
	fresh := e.login(t, "/api/v1/auth/login", "user@example.com")
	wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/me", fresh, nil), http.StatusOK, "/auth/me with fresh token")
}

func TestSession_AdminLogoutRevokesOldTokens(t *testing.T) {
	e := newSessEnv(t)
	old := e.adminToken(t, time.Now().UTC().Add(-5*time.Second))

	wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/admin/me", old, nil), http.StatusOK, "/auth/admin/me before logout")
	wantStatus(t, e.do(http.MethodPost, "/api/v1/auth/admin/logout", old, nil), http.StatusOK, "admin logout")

	wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/admin/me", old, nil), http.StatusUnauthorized, "/auth/admin/me after logout")
	wantStatus(t, e.do(http.MethodGet, "/api/v1/admin/dashboard/summary", old, nil), http.StatusUnauthorized, "admin route after logout")

	fresh := e.login(t, "/api/v1/auth/admin/login", "admin@example.com")
	wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/admin/me", fresh, nil), http.StatusOK, "/auth/admin/me with fresh token")
}

func TestSession_DeactivatedAccountsAreRejected(t *testing.T) {
	t.Run("merchant deactivated", func(t *testing.T) {
		e := newSessEnv(t)
		tok := e.merchantToken(t, time.Now().UTC())
		e.mRepo.mu.Lock()
		e.mRepo.m.IsActive = false
		e.mRepo.mu.Unlock()
		wantStatus(t, e.do(http.MethodGet, "/api/v1/merchant/dashboard/summary", tok, nil), http.StatusUnauthorized, "middleware route")
		wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/me", tok, nil), http.StatusUnauthorized, "handler route")
	})
	t.Run("merchant user deactivated", func(t *testing.T) {
		e := newSessEnv(t)
		tok := e.merchantToken(t, time.Now().UTC())
		e.uRepo.mu.Lock()
		e.uRepo.u.IsActive = false
		e.uRepo.mu.Unlock()
		wantStatus(t, e.do(http.MethodGet, "/api/v1/merchant/dashboard/summary", tok, nil), http.StatusUnauthorized, "middleware route")
		wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/me", tok, nil), http.StatusUnauthorized, "handler route")
	})
	t.Run("admin deactivated", func(t *testing.T) {
		e := newSessEnv(t)
		tok := e.adminToken(t, time.Now().UTC())
		e.aRepo.mu.Lock()
		e.aRepo.a.IsActive = false
		e.aRepo.mu.Unlock()
		wantStatus(t, e.do(http.MethodGet, "/api/v1/admin/dashboard/summary", tok, nil), http.StatusUnauthorized, "middleware route")
		wantStatus(t, e.do(http.MethodGet, "/api/v1/auth/admin/me", tok, nil), http.StatusUnauthorized, "handler route")
	})
}

// The middleware must let a healthy session through to the next handler.
func TestSession_MiddlewareAllowsValidSession(t *testing.T) {
	e := newSessEnv(t)
	mw := middleware.NewAuthMiddleware(e.authSvc)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	for name, tc := range map[string]struct {
		h     http.Handler
		token string
	}{
		"merchant": {mw.RequireMerchant(ok), e.merchantToken(t, time.Now().UTC())},
		"admin":    {mw.RequireAdmin(ok), e.adminToken(t, time.Now().UTC())},
	} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		rec := httptest.NewRecorder()
		tc.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s: status = %d, want 204", name, rec.Code)
		}
	}
}
