package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
)

// ---- fakeAuthMerchantRepo -----------------------------------------------

type fakeAuthMerchantRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*merchant.Merchant
}

func newFakeAuthMerchantRepo() *fakeAuthMerchantRepo {
	return &fakeAuthMerchantRepo{byID: map[uuid.UUID]*merchant.Merchant{}}
}

func (f *fakeAuthMerchantRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.byID[id]
	if !ok {
		return nil, merchant.ErrMerchantNotFound
	}
	cp := *m
	return &cp, nil
}

func (f *fakeAuthMerchantRepo) GetByEmail(ctx context.Context, email string) (*merchant.Merchant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.byID {
		if strings.EqualFold(m.Email, email) {
			cp := *m
			return &cp, nil
		}
	}
	return nil, merchant.ErrMerchantNotFound
}

func (f *fakeAuthMerchantRepo) Create(ctx context.Context, req *merchant.CreateMerchantRequest) (*merchant.Merchant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	m := &merchant.Merchant{
		ID:           uuid.New(),
		Name:         req.Name,
		Email:        req.Email,
		Phone:        req.Phone,
		BusinessName: req.BusinessName,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.byID[m.ID] = m
	return m, nil
}

func (f *fakeAuthMerchantRepo) Delete(ctx context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byID, id)
	return nil
}

// ---- fakeAuthMerchantUserRepo -------------------------------------------

type fakeAuthMerchantUserRepo struct {
	mu       sync.Mutex
	byID     map[uuid.UUID]*merchant.User
	failNext bool
}

func newFakeAuthMerchantUserRepo() *fakeAuthMerchantUserRepo {
	return &fakeAuthMerchantUserRepo{byID: map[uuid.UUID]*merchant.User{}}
}

func (f *fakeAuthMerchantUserRepo) GetByEmail(ctx context.Context, email string) (*merchant.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byID {
		if strings.EqualFold(u.Email, email) {
			cp := *u
			return &cp, nil
		}
	}
	return nil, merchant.ErrMerchantUserNotFound
}

func (f *fakeAuthMerchantUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil, merchant.ErrMerchantUserNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *fakeAuthMerchantUserRepo) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	return nil
}

func (f *fakeAuthMerchantUserRepo) UpdateProfile(ctx context.Context, id uuid.UUID, name, email string) error {
	return nil
}

func (f *fakeAuthMerchantUserRepo) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	return nil
}

func (f *fakeAuthMerchantUserRepo) Create(ctx context.Context, u *merchant.User) (*merchant.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return nil, errors.New("simulated insert failure")
	}
	now := time.Now().UTC()
	cp := *u
	cp.ID = uuid.New()
	cp.CreatedAt = now
	cp.UpdatedAt = now
	f.byID[cp.ID] = &cp
	return &cp, nil
}

// ---- tests ---------------------------------------------------------------

func newTestAuthServiceForRegister() (*AuthService, *fakeAuthMerchantRepo, *fakeAuthMerchantUserRepo) {
	merchantRepo := newFakeAuthMerchantRepo()
	userRepo := newFakeAuthMerchantUserRepo()
	svc := NewAuthService(nil, "test-secret").WithMerchantAuth(userRepo, merchantRepo)
	return svc, merchantRepo, userRepo
}

func validRegisterRequest() *merchant.RegisterRequest {
	return &merchant.RegisterRequest{
		Name:         "Budi Santoso",
		BusinessName: "Toko Budi Jaya",
		Email:        "budi@tokobudi.id",
		Phone:        "08123456789",
		Password:     "supersecret1",
	}
}

func TestAuthService_RegisterMerchant_HappyPath(t *testing.T) {
	svc, merchantRepo, userRepo := newTestAuthServiceForRegister()

	resp, err := svc.RegisterMerchant(context.Background(), validRegisterRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.BusinessName != "Toko Budi Jaya" {
		t.Errorf("expected business name to match, got %q", resp.BusinessName)
	}
	if len(merchantRepo.byID) != 1 {
		t.Fatalf("expected 1 merchant created, got %d", len(merchantRepo.byID))
	}
	if len(userRepo.byID) != 1 {
		t.Fatalf("expected 1 owner user created, got %d", len(userRepo.byID))
	}
	for _, u := range userRepo.byID {
		if u.Role != "owner" {
			t.Errorf("expected role owner, got %q", u.Role)
		}
		if !u.IsActive {
			t.Errorf("expected user to be active")
		}
		if u.MerchantID != resp.ID {
			t.Errorf("expected user merchant_id %s to match created merchant %s", u.MerchantID, resp.ID)
		}
	}
}

func TestAuthService_RegisterMerchant_DuplicateMerchantEmail(t *testing.T) {
	svc, merchantRepo, _ := newTestAuthServiceForRegister()
	merchantRepo.byID[uuid.New()] = &merchant.Merchant{ID: uuid.New(), Email: "budi@tokobudi.id"}

	_, err := svc.RegisterMerchant(context.Background(), validRegisterRequest())
	if err != merchant.ErrMerchantAlreadyExists {
		t.Fatalf("expected ErrMerchantAlreadyExists, got %v", err)
	}
}

func TestAuthService_RegisterMerchant_DuplicateUserEmail(t *testing.T) {
	svc, _, userRepo := newTestAuthServiceForRegister()
	userRepo.byID[uuid.New()] = &merchant.User{ID: uuid.New(), Email: "budi@tokobudi.id"}

	_, err := svc.RegisterMerchant(context.Background(), validRegisterRequest())
	if err != merchant.ErrMerchantAlreadyExists {
		t.Fatalf("expected ErrMerchantAlreadyExists, got %v", err)
	}
}

func TestAuthService_RegisterMerchant_RollsBackMerchantWhenUserCreateFails(t *testing.T) {
	svc, merchantRepo, userRepo := newTestAuthServiceForRegister()
	userRepo.failNext = true

	_, err := svc.RegisterMerchant(context.Background(), validRegisterRequest())
	if err == nil {
		t.Fatal("expected error when owner user creation fails")
	}
	if len(merchantRepo.byID) != 0 {
		t.Fatalf("expected merchant to be rolled back, got %d remaining", len(merchantRepo.byID))
	}
}
