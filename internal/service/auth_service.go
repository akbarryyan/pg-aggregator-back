package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	adminTokenIssuer      = "pg-aggregator"
	adminTokenAudience    = "admin"
	merchantTokenAudience = "merchant"
	adminTokenTTL         = 24 * time.Hour
	merchantTokenTTL      = 24 * time.Hour
)

type AdminClaims struct {
	AdminID uuid.UUID `json:"admin_id"`
	Email   string    `json:"email"`
	Role    string    `json:"role"`
	jwt.RegisteredClaims
}

type MerchantClaims struct {
	UserID     uuid.UUID `json:"user_id"`
	MerchantID uuid.UUID `json:"merchant_id"`
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	jwt.RegisteredClaims
}

type AuthService struct {
	adminRepo        *repository.AdminRepository
	merchantUserRepo *repository.MerchantUserRepository
	merchantRepo     *repository.MerchantRepository
	jwtSecret        []byte
}

func NewAuthService(adminRepo *repository.AdminRepository, jwtSecret string) *AuthService {
	return &AuthService{
		adminRepo: adminRepo,
		jwtSecret: []byte(jwtSecret),
	}
}

func (s *AuthService) WithMerchantAuth(
	merchantUserRepo *repository.MerchantUserRepository,
	merchantRepo *repository.MerchantRepository,
) *AuthService {
	s.merchantUserRepo = merchantUserRepo
	s.merchantRepo = merchantRepo
	return s
}

func (s *AuthService) LoginAdmin(ctx context.Context, req *admin.LoginRequest) (*admin.LoginResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	a, err := s.adminRepo.GetByEmail(ctx, email)
	if err != nil {
		if err == admin.ErrAdminNotFound {
			return nil, admin.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("failed to load admin: %w", err)
	}

	if !a.IsActive {
		return nil, admin.ErrAdminInactive
	}

	if err := bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(req.Password)); err != nil {
		return nil, admin.ErrInvalidCredentials
	}

	now := time.Now().UTC()
	expiresAt := now.Add(adminTokenTTL)

	claims := AdminClaims{
		AdminID: a.ID,
		Email:   a.Email,
		Role:    "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    adminTokenIssuer,
			Audience:  []string{adminTokenAudience},
			Subject:   a.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign token: %w", err)
	}

	if err := s.adminRepo.UpdateLastLoginAt(ctx, a.ID, now); err != nil {
		return nil, fmt.Errorf("failed to update last login: %w", err)
	}
	a.LastLoginAt = &now
	a.UpdatedAt = now

	return &admin.LoginResponse{
		Token:     signed,
		TokenType: "Bearer",
		ExpiresIn: int64(adminTokenTTL.Seconds()),
		Admin:     admin.ToAdminResponse(a),
	}, nil
}

func (s *AuthService) ParseAdminToken(tokenString string) (*AdminClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &AdminClaims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, admin.ErrInvalidToken
		}
		return s.jwtSecret, nil
	}, jwt.WithAudience(adminTokenAudience), jwt.WithIssuer(adminTokenIssuer))
	if err != nil {
		return nil, admin.ErrInvalidToken
	}

	claims, ok := token.Claims.(*AdminClaims)
	if !ok || !token.Valid {
		return nil, admin.ErrInvalidToken
	}

	return claims, nil
}

func (s *AuthService) GetAdminByID(ctx context.Context, id uuid.UUID) (*admin.Admin, error) {
	return s.adminRepo.GetByID(ctx, id)
}

func (s *AuthService) UpdateProfile(ctx context.Context, adminID uuid.UUID, req *admin.UpdateProfileRequest) (*admin.Admin, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(req.Name)
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if name == "" {
		return nil, admin.ErrNameRequired
	}
	if email == "" {
		return nil, admin.ErrEmailRequired
	}

	current, err := s.adminRepo.GetByID(ctx, adminID)
	if err != nil {
		return nil, err
	}
	if !current.IsActive {
		return nil, admin.ErrAdminInactive
	}

	taken, err := s.adminRepo.ExistsByEmailExceptID(ctx, email, adminID)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, admin.ErrEmailAlreadyUsed
	}

	updated, err := s.adminRepo.UpdateProfile(ctx, adminID, name, email)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// VerifyAdminPassword checks the admin's current password without changing it.
func (s *AuthService) VerifyAdminPassword(ctx context.Context, adminID uuid.UUID, password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return admin.ErrInvalidCredentials
	}

	a, err := s.adminRepo.GetByID(ctx, adminID)
	if err != nil {
		return err
	}
	if !a.IsActive {
		return admin.ErrAdminInactive
	}
	if err := bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(password)); err != nil {
		return admin.ErrInvalidCredentials
	}
	return nil
}

func (s *AuthService) ChangePassword(ctx context.Context, adminID uuid.UUID, req *admin.ChangePasswordRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}

	a, err := s.adminRepo.GetByID(ctx, adminID)
	if err != nil {
		return err
	}

	if !a.IsActive {
		return admin.ErrAdminInactive
	}

	if err := bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		return admin.ErrCurrentPasswordInvalid
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	if err := s.adminRepo.UpdatePasswordHash(ctx, adminID, string(hash)); err != nil {
		return fmt.Errorf("failed to save new password: %w", err)
	}

	return nil
}

// --- Merchant dashboard auth ---

func (s *AuthService) LoginMerchant(ctx context.Context, req *merchant.UserLoginRequest) (*merchant.UserLoginResponse, error) {
	if s.merchantUserRepo == nil {
		return nil, fmt.Errorf("merchant auth not configured")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	u, err := s.merchantUserRepo.GetByEmail(ctx, email)
	if err != nil {
		if err == merchant.ErrMerchantUserNotFound {
			return nil, merchant.ErrMerchantInvalidCredentials
		}
		return nil, err
	}
	if !u.IsActive {
		return nil, merchant.ErrMerchantUserInactive
	}

	// Ensure parent merchant is active
	if s.merchantRepo != nil {
		m, err := s.merchantRepo.GetByID(ctx, u.MerchantID)
		if err != nil {
			return nil, err
		}
		if !m.IsActive {
			return nil, merchant.ErrMerchantUserInactive
		}
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		return nil, merchant.ErrMerchantInvalidCredentials
	}

	now := time.Now().UTC()
	expiresAt := now.Add(merchantTokenTTL)
	claims := MerchantClaims{
		UserID:     u.ID,
		MerchantID: u.MerchantID,
		Email:      u.Email,
		Role:       u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    adminTokenIssuer,
			Audience:  []string{merchantTokenAudience},
			Subject:   u.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign merchant token: %w", err)
	}

	_ = s.merchantUserRepo.UpdateLastLoginAt(ctx, u.ID, now)
	u.LastLoginAt = &now

	respUser := merchant.ToUserResponse(u)
	if s.merchantRepo != nil {
		if m, err := s.merchantRepo.GetByID(ctx, u.MerchantID); err == nil {
			respUser.BusinessName = m.BusinessName
			respUser.WebhookURL = m.WebhookURL
		}
	}

	return &merchant.UserLoginResponse{
		Token:     signed,
		TokenType: "Bearer",
		ExpiresIn: int64(merchantTokenTTL.Seconds()),
		User:      respUser,
	}, nil
}

func (s *AuthService) ParseMerchantToken(tokenString string) (*MerchantClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &MerchantClaims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, merchant.ErrMerchantInvalidCredentials
		}
		return s.jwtSecret, nil
	}, jwt.WithAudience(merchantTokenAudience), jwt.WithIssuer(adminTokenIssuer))
	if err != nil {
		return nil, merchant.ErrMerchantInvalidCredentials
	}
	claims, ok := token.Claims.(*MerchantClaims)
	if !ok || !token.Valid {
		return nil, merchant.ErrMerchantInvalidCredentials
	}
	return claims, nil
}

func (s *AuthService) GetMerchantUserByID(ctx context.Context, id uuid.UUID) (*merchant.User, error) {
	if s.merchantUserRepo == nil {
		return nil, merchant.ErrMerchantUserNotFound
	}
	return s.merchantUserRepo.GetByID(ctx, id)
}

func (s *AuthService) GetMerchantUserProfile(ctx context.Context, userID uuid.UUID) (*merchant.UserResponse, error) {
	u, err := s.GetMerchantUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	resp := merchant.ToUserResponse(u)
	if s.merchantRepo != nil {
		if m, err := s.merchantRepo.GetByID(ctx, u.MerchantID); err == nil {
			resp.BusinessName = m.BusinessName
			resp.WebhookURL = m.WebhookURL
		}
	}
	return resp, nil
}

func (s *AuthService) UpdateMerchantUserProfile(ctx context.Context, userID uuid.UUID, req *merchant.UserUpdateProfileRequest) (*merchant.UserResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	u, err := s.GetMerchantUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !u.IsActive {
		return nil, merchant.ErrMerchantUserInactive
	}
	name := strings.TrimSpace(req.Name)
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if err := s.merchantUserRepo.UpdateProfile(ctx, userID, name, email); err != nil {
		return nil, err
	}
	return s.GetMerchantUserProfile(ctx, userID)
}

func (s *AuthService) ChangeMerchantPassword(ctx context.Context, userID uuid.UUID, req *merchant.UserChangePasswordRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	u, err := s.GetMerchantUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if !u.IsActive {
		return merchant.ErrMerchantUserInactive
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		return merchant.ErrMerchantCurrentPasswordInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.merchantUserRepo.UpdatePasswordHash(ctx, userID, string(hash))
}

func (s *AuthService) VerifyMerchantPassword(ctx context.Context, userID uuid.UUID, password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return merchant.ErrMerchantInvalidCredentials
	}
	u, err := s.GetMerchantUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if !u.IsActive {
		return merchant.ErrMerchantUserInactive
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return merchant.ErrMerchantInvalidCredentials
	}
	return nil
}
