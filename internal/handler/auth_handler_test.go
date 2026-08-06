package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const testJWTSecret = "test-secret-do-not-use-in-production"

func mustHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	return string(hash)
}

func newTestAuthHandler(t *testing.T, admins []*admin.Admin, merchants []*merchant.Merchant, users []*merchant.User) *AuthHandler {
	t.Helper()
	svc := service.NewAuthService(newFakeAdminRepo(admins...), testJWTSecret).
		WithMerchantAuth(newFakeMerchantUserRepo(users...), newFakeAuthMerchantRepo(merchants...))
	return NewAuthHandler(svc)
}

func TestAuthHandler_LoginAdmin_Success(t *testing.T) {
	a := &admin.Admin{
		ID:           uuid.New(),
		Name:         "Admin One",
		Email:        "admin@example.com",
		PasswordHash: mustHash(t, "correct-password"),
		IsActive:     true,
	}
	h := newTestAuthHandler(t, []*admin.Admin{a}, nil, nil)

	body, _ := json.Marshal(map[string]string{"email": "admin@example.com", "password": "correct-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginAdmin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp admin.LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
	if resp.Admin == nil || resp.Admin.Email != a.Email {
		t.Errorf("expected admin email %s in response, got %+v", a.Email, resp.Admin)
	}
}

func TestAuthHandler_LoginAdmin_WrongPassword(t *testing.T) {
	a := &admin.Admin{
		ID:           uuid.New(),
		Email:        "admin@example.com",
		PasswordHash: mustHash(t, "correct-password"),
		IsActive:     true,
	}
	h := newTestAuthHandler(t, []*admin.Admin{a}, nil, nil)

	body, _ := json.Marshal(map[string]string{"email": "admin@example.com", "password": "wrong-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginAdmin(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthHandler_LoginAdmin_UnknownEmailDoesNotLeakExistence(t *testing.T) {
	h := newTestAuthHandler(t, nil, nil, nil)

	body, _ := json.Marshal(map[string]string{"email": "nobody@example.com", "password": "whatever"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginAdmin(rec, req)

	// Same 401 as wrong-password case — must not distinguish "no such user"
	// from "wrong password" (this is what admin.ErrInvalidCredentials, not
	// ErrAdminNotFound, achieves in AuthService.LoginAdmin).
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthHandler_LoginAdmin_InactiveAccount(t *testing.T) {
	a := &admin.Admin{
		ID:           uuid.New(),
		Email:        "admin@example.com",
		PasswordHash: mustHash(t, "correct-password"),
		IsActive:     false,
	}
	h := newTestAuthHandler(t, []*admin.Admin{a}, nil, nil)

	body, _ := json.Marshal(map[string]string{"email": "admin@example.com", "password": "correct-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginAdmin(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthHandler_LoginAdmin_MissingFields(t *testing.T) {
	h := newTestAuthHandler(t, nil, nil, nil)

	body, _ := json.Marshal(map[string]string{"email": "", "password": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginAdmin(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthHandler_LoginAdmin_InvalidBody(t *testing.T) {
	h := newTestAuthHandler(t, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin/login", bytes.NewReader([]byte("{bad json")))
	rec := httptest.NewRecorder()

	h.LoginAdmin(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthHandler_GetMe_RequiresToken(t *testing.T) {
	h := newTestAuthHandler(t, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/admin/me", nil)
	rec := httptest.NewRecorder()

	h.GetMe(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthHandler_GetMe_InvalidToken(t *testing.T) {
	h := newTestAuthHandler(t, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/admin/me", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	rec := httptest.NewRecorder()

	h.GetMe(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// TestAuthHandler_GetMe_ValidTokenRoundtrip logs in to obtain a real JWT,
// then uses it to call GetMe — exercises the full issue → parse → resolve
// identity path through the actual AuthService (not mocked).
func TestAuthHandler_GetMe_ValidTokenRoundtrip(t *testing.T) {
	a := &admin.Admin{
		ID:           uuid.New(),
		Name:         "Admin One",
		Email:        "admin@example.com",
		PasswordHash: mustHash(t, "correct-password"),
		IsActive:     true,
	}
	h := newTestAuthHandler(t, []*admin.Admin{a}, nil, nil)

	loginBody, _ := json.Marshal(map[string]string{"email": "admin@example.com", "password": "correct-password"})
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin/login", bytes.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	h.LoginAdmin(loginRec, loginReq)

	var loginResp admin.LoginResponse
	if err := json.Unmarshal(loginRec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("failed to decode login response: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/admin/me", nil)
	req.Header.Set("Authorization", "Bearer "+loginResp.Token)
	rec := httptest.NewRecorder()

	h.GetMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp admin.AdminResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Email != a.Email {
		t.Errorf("expected email %s, got %s", a.Email, resp.Email)
	}
}

// --- Merchant dashboard auth ---

func TestAuthHandler_LoginMerchant_Success(t *testing.T) {
	m := &merchant.Merchant{
		ID:       uuid.New(),
		Name:     "Acme Merchant",
		Email:    "merchant@example.com",
		IsActive: true,
	}
	u := &merchant.User{
		ID:           uuid.New(),
		MerchantID:   m.ID,
		Name:         "Merchant User",
		Email:        "user@example.com",
		PasswordHash: mustHash(t, "correct-password"),
		Role:         "owner",
		IsActive:     true,
	}
	h := newTestAuthHandler(t, nil, []*merchant.Merchant{m}, []*merchant.User{u})

	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "password": "correct-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginMerchant(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp merchant.UserLoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
}

func TestAuthHandler_LoginMerchant_InactiveUser(t *testing.T) {
	m := &merchant.Merchant{ID: uuid.New(), IsActive: true}
	u := &merchant.User{
		ID:           uuid.New(),
		MerchantID:   m.ID,
		Email:        "user@example.com",
		PasswordHash: mustHash(t, "correct-password"),
		IsActive:     false,
	}
	h := newTestAuthHandler(t, nil, []*merchant.Merchant{m}, []*merchant.User{u})

	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "password": "correct-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginMerchant(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAuthHandler_LoginMerchant_InactiveParentMerchantBlocksLogin verifies a
// user tied to a deactivated merchant account cannot log in even with
// correct credentials — enforced via the merchantRepo lookup in
// AuthService.LoginMerchant.
func TestAuthHandler_LoginMerchant_InactiveParentMerchantBlocksLogin(t *testing.T) {
	m := &merchant.Merchant{ID: uuid.New(), IsActive: false}
	u := &merchant.User{
		ID:           uuid.New(),
		MerchantID:   m.ID,
		Email:        "user@example.com",
		PasswordHash: mustHash(t, "correct-password"),
		IsActive:     true,
	}
	h := newTestAuthHandler(t, nil, []*merchant.Merchant{m}, []*merchant.User{u})

	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "password": "correct-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginMerchant(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthHandler_LoginMerchant_WrongPassword(t *testing.T) {
	m := &merchant.Merchant{ID: uuid.New(), IsActive: true}
	u := &merchant.User{
		ID:           uuid.New(),
		MerchantID:   m.ID,
		Email:        "user@example.com",
		PasswordHash: mustHash(t, "correct-password"),
		IsActive:     true,
	}
	h := newTestAuthHandler(t, nil, []*merchant.Merchant{m}, []*merchant.User{u})

	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "password": "wrong-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.LoginMerchant(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthHandler_GetMerchantMe_RequiresToken(t *testing.T) {
	h := newTestAuthHandler(t, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()

	h.GetMerchantMe(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
