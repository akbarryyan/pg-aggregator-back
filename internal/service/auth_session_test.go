package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestSessionRevoked(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) *jwt.NumericDate { return jwt.NewNumericDate(now.Add(d)) }
	cutoff := now
	tests := []struct {
		name       string
		iat        *jwt.NumericDate
		validAfter *time.Time
		want       bool
	}{
		{"no cutoff", at(-time.Hour), nil, false},
		{"issued before cutoff", at(-time.Second), &cutoff, true},
		{"issued at cutoff second", at(0), &cutoff, false},
		{"issued after cutoff", at(time.Second), &cutoff, false},
		{"cutoff with sub-second part keeps same-second token", at(0), ptrTime(now.Add(900 * time.Millisecond)), false},
		{"missing iat with cutoff", nil, &cutoff, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionRevoked(tc.iat, tc.validAfter); got != tc.want {
				t.Errorf("sessionRevoked = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---- admin fake ------------------------------------------------------------

type fakeSessAdminRepo struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]*admin.Admin
	getErr error
}

func (f *fakeSessAdminRepo) GetByEmail(ctx context.Context, email string) (*admin.Admin, error) {
	return nil, admin.ErrAdminNotFound
}

func (f *fakeSessAdminRepo) GetByID(ctx context.Context, id uuid.UUID) (*admin.Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	a, ok := f.byID[id]
	if !ok {
		return nil, admin.ErrAdminNotFound
	}
	cp := *a
	return &cp, nil
}

func (f *fakeSessAdminRepo) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	return nil
}

func (f *fakeSessAdminRepo) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return admin.ErrAdminNotFound
	}
	now := time.Now().UTC()
	a.PasswordHash = hash
	a.SessionsValidAfter = &now
	return nil
}

func (f *fakeSessAdminRepo) RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return admin.ErrAdminNotFound
	}
	a.SessionsValidAfter = &at
	return nil
}

func (f *fakeSessAdminRepo) ExistsByEmailExceptID(ctx context.Context, email string, id uuid.UUID) (bool, error) {
	return false, nil
}

func (f *fakeSessAdminRepo) UpdateProfile(ctx context.Context, id uuid.UUID, name, email string) (*admin.Admin, error) {
	return nil, admin.ErrAdminNotFound
}

func (f *fakeSessAdminRepo) setValidAfter(id uuid.UUID, t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].SessionsValidAfter = &t
}

// ---- helpers ---------------------------------------------------------------

const sessTestSecret = "test-secret"

func bcryptHash(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	return string(b), err
}

func adminClaimsAt(id uuid.UUID, iat time.Time) *AdminClaims {
	return &AdminClaims{
		AdminID:          id,
		RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(iat)},
	}
}

func merchantClaimsAt(u *merchant.User, iat time.Time) *MerchantClaims {
	return &MerchantClaims{
		UserID:           u.ID,
		MerchantID:       u.MerchantID,
		RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(iat)},
	}
}

type merchantFixture struct {
	svc      *AuthService
	userRepo *fakeAuthMerchantUserRepo
	mRepo    *fakeAuthMerchantRepo
	user     *merchant.User
	merchant *merchant.Merchant
}

func newMerchantFixture() *merchantFixture {
	mRepo := newFakeAuthMerchantRepo()
	uRepo := newFakeAuthMerchantUserRepo()
	m := &merchant.Merchant{ID: uuid.New(), Email: "m@x.id", IsActive: true}
	u := &merchant.User{ID: uuid.New(), MerchantID: m.ID, Email: "u@x.id", IsActive: true}
	mRepo.byID[m.ID] = m
	uRepo.byID[u.ID] = u
	return &merchantFixture{
		svc:      NewAuthService(nil, sessTestSecret).WithMerchantAuth(uRepo, mRepo),
		userRepo: uRepo, mRepo: mRepo, user: u, merchant: m,
	}
}

func TestAuthorizeMerchant(t *testing.T) {
	iat := time.Now().UTC().Add(-time.Minute)
	cutoffAfterIat := time.Now().UTC()

	tests := []struct {
		name    string
		mutate  func(f *merchantFixture)
		claimID func(f *merchantFixture) *MerchantClaims
		wantErr error // nil = success
	}{
		{name: "active user and merchant"},
		{name: "inactive user", mutate: func(f *merchantFixture) { f.user.IsActive = false }, wantErr: merchant.ErrMerchantInvalidCredentials},
		{name: "inactive merchant", mutate: func(f *merchantFixture) { f.merchant.IsActive = false }, wantErr: merchant.ErrMerchantInvalidCredentials},
		{name: "merchant missing", mutate: func(f *merchantFixture) { delete(f.mRepo.byID, f.merchant.ID) }, wantErr: merchant.ErrMerchantInvalidCredentials},
		{name: "user missing", claimID: func(f *merchantFixture) *MerchantClaims {
			c := merchantClaimsAt(f.user, iat)
			c.UserID = uuid.New()
			return c
		}, wantErr: merchant.ErrMerchantInvalidCredentials},
		{name: "token issued before cutoff", mutate: func(f *merchantFixture) { f.user.SessionsValidAfter = &cutoffAfterIat }, wantErr: merchant.ErrMerchantInvalidCredentials},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newMerchantFixture()
			if tc.mutate != nil {
				tc.mutate(f)
			}
			c := merchantClaimsAt(f.user, iat)
			if tc.claimID != nil {
				c = tc.claimID(f)
			}
			err := f.svc.AuthorizeMerchant(context.Background(), c)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// failingMerchantUserRepo returns a non-"not found" error from GetByID.
type failingMerchantUserRepo struct {
	*fakeAuthMerchantUserRepo
	err error
}

func (f failingMerchantUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*merchant.User, error) {
	return nil, f.err
}

func TestAuthorizeMerchant_RepoErrorIsNotAnInvalidToken(t *testing.T) {
	f := newMerchantFixture()
	dbDown := errors.New("db down")
	svc := NewAuthService(nil, sessTestSecret).WithMerchantAuth(failingMerchantUserRepo{f.userRepo, dbDown}, f.mRepo)
	err := svc.AuthorizeMerchant(context.Background(), merchantClaimsAt(f.user, time.Now()))
	if !errors.Is(err, dbDown) {
		t.Fatalf("err = %v, want db down passed through", err)
	}
	if errors.Is(err, merchant.ErrMerchantInvalidCredentials) {
		t.Fatal("repo error must not be reported as an invalid token (would be a 401, not 500)")
	}
}

func TestAuthorizeMerchant_PasswordChangeAndLogoutRevokeOldTokens(t *testing.T) {
	ctx := context.Background()
	old := time.Now().UTC().Add(-2 * time.Second) // earlier second than the revocation

	t.Run("change password", func(t *testing.T) {
		f := newMerchantFixture()
		hash, _ := bcryptHash("old-password-1")
		f.user.PasswordHash = hash
		if err := f.svc.AuthorizeMerchant(ctx, merchantClaimsAt(f.user, old)); err != nil {
			t.Fatalf("before change: %v", err)
		}
		err := f.svc.ChangeMerchantPassword(ctx, f.user.ID, &merchant.UserChangePasswordRequest{
			CurrentPassword: "old-password-1", NewPassword: "new-password-2",
		})
		if err != nil {
			t.Fatalf("change password: %v", err)
		}
		if f.userRepo.byID[f.user.ID].SessionsValidAfter == nil {
			t.Fatal("SessionsValidAfter not set by password change")
		}
		if err := f.svc.AuthorizeMerchant(ctx, merchantClaimsAt(f.user, old)); !errors.Is(err, merchant.ErrMerchantInvalidCredentials) {
			t.Fatalf("old token after change: err = %v, want invalid", err)
		}
		if err := f.svc.AuthorizeMerchant(ctx, merchantClaimsAt(f.user, time.Now().UTC())); err != nil {
			t.Fatalf("fresh token after change: %v", err)
		}
	})

	t.Run("logout", func(t *testing.T) {
		f := newMerchantFixture()
		if err := f.svc.LogoutMerchant(ctx, f.user.ID); err != nil {
			t.Fatalf("logout: %v", err)
		}
		if err := f.svc.AuthorizeMerchant(ctx, merchantClaimsAt(f.user, old)); !errors.Is(err, merchant.ErrMerchantInvalidCredentials) {
			t.Fatalf("old token after logout: err = %v, want invalid", err)
		}
		if err := f.svc.AuthorizeMerchant(ctx, merchantClaimsAt(f.user, time.Now().UTC())); err != nil {
			t.Fatalf("fresh token after logout: %v", err)
		}
	})
}

func TestAuthorizeAdmin(t *testing.T) {
	iat := time.Now().UTC().Add(-time.Minute)
	cutoff := time.Now().UTC()
	dbDown := errors.New("db down")

	tests := []struct {
		name    string
		mutate  func(a *admin.Admin, r *fakeSessAdminRepo)
		unknown bool
		wantErr error
	}{
		{name: "active admin"},
		{name: "inactive", mutate: func(a *admin.Admin, r *fakeSessAdminRepo) { a.IsActive = false }, wantErr: admin.ErrInvalidToken},
		{name: "unknown admin", unknown: true, wantErr: admin.ErrInvalidToken},
		{name: "token issued before cutoff", mutate: func(a *admin.Admin, r *fakeSessAdminRepo) { a.SessionsValidAfter = &cutoff }, wantErr: admin.ErrInvalidToken},
		{name: "repo error passes through", mutate: func(a *admin.Admin, r *fakeSessAdminRepo) { r.getErr = dbDown }, wantErr: dbDown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &admin.Admin{ID: uuid.New(), IsActive: true}
			r := &fakeSessAdminRepo{byID: map[uuid.UUID]*admin.Admin{a.ID: a}}
			if tc.mutate != nil {
				tc.mutate(a, r)
			}
			id := a.ID
			if tc.unknown {
				id = uuid.New()
			}
			svc := NewAuthService(r, sessTestSecret)
			err := svc.AuthorizeAdmin(context.Background(), adminClaimsAt(id, iat))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == dbDown && errors.Is(err, admin.ErrInvalidToken) {
				t.Fatal("repo error must not be reported as an invalid token")
			}
		})
	}
}

func TestAuthorizeAdmin_PasswordChangeAndLogoutRevokeOldTokens(t *testing.T) {
	ctx := context.Background()
	old := time.Now().UTC().Add(-2 * time.Second)

	t.Run("change password", func(t *testing.T) {
		hash, _ := bcryptHash("old-password-1")
		a := &admin.Admin{ID: uuid.New(), IsActive: true, PasswordHash: hash}
		r := &fakeSessAdminRepo{byID: map[uuid.UUID]*admin.Admin{a.ID: a}}
		svc := NewAuthService(r, sessTestSecret)
		if err := svc.AuthorizeAdmin(ctx, adminClaimsAt(a.ID, old)); err != nil {
			t.Fatalf("before change: %v", err)
		}
		if err := svc.ChangePassword(ctx, a.ID, &admin.ChangePasswordRequest{CurrentPassword: "old-password-1", NewPassword: "new-password-2"}); err != nil {
			t.Fatalf("change password: %v", err)
		}
		if err := svc.AuthorizeAdmin(ctx, adminClaimsAt(a.ID, old)); !errors.Is(err, admin.ErrInvalidToken) {
			t.Fatalf("old token after change: err = %v, want invalid", err)
		}
		if err := svc.AuthorizeAdmin(ctx, adminClaimsAt(a.ID, time.Now().UTC())); err != nil {
			t.Fatalf("fresh token after change: %v", err)
		}
	})

	t.Run("logout", func(t *testing.T) {
		a := &admin.Admin{ID: uuid.New(), IsActive: true}
		r := &fakeSessAdminRepo{byID: map[uuid.UUID]*admin.Admin{a.ID: a}}
		svc := NewAuthService(r, sessTestSecret)
		if err := svc.LogoutAdmin(ctx, a.ID); err != nil {
			t.Fatalf("logout: %v", err)
		}
		if err := svc.AuthorizeAdmin(ctx, adminClaimsAt(a.ID, old)); !errors.Is(err, admin.ErrInvalidToken) {
			t.Fatalf("old token after logout: err = %v, want invalid", err)
		}
		if err := svc.AuthorizeAdmin(ctx, adminClaimsAt(a.ID, time.Now().UTC())); err != nil {
			t.Fatalf("fresh token after logout: %v", err)
		}
	})
}
