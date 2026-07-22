package merchant

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrMerchantUserNotFound            = errors.New("merchant user not found")
	ErrMerchantUserInactive            = errors.New("merchant user is inactive")
	ErrMerchantInvalidCredentials      = errors.New("invalid email or password")
	ErrMerchantPasswordRequired        = errors.New("password is required")
	ErrMerchantCurrentPasswordRequired = errors.New("current password is required")
	ErrMerchantNewPasswordRequired     = errors.New("new password is required")
	ErrMerchantNewPasswordTooShort     = errors.New("new password must be at least 8 characters")
	ErrMerchantNewPasswordSame         = errors.New("new password must differ from current password")
	ErrMerchantCurrentPasswordInvalid  = errors.New("current password is incorrect")
)

type User struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	MerchantID   uuid.UUID  `json:"merchant_id" db:"merchant_id"`
	Name         string     `json:"name" db:"name"`
	Email        string     `json:"email" db:"email"`
	PasswordHash string     `json:"-" db:"password_hash"`
	Role         string     `json:"role" db:"role"`
	IsActive     bool       `json:"is_active" db:"is_active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty" db:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at" db:"updated_at"`
}

type UserLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r *UserLoginRequest) Validate() error {
	if strings.TrimSpace(r.Email) == "" {
		return ErrMerchantEmailRequired
	}
	if r.Password == "" {
		return ErrMerchantPasswordRequired
	}
	return nil
}

type UserUpdateProfileRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (r *UserUpdateProfileRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return ErrMerchantNameRequired
	}
	if strings.TrimSpace(r.Email) == "" {
		return ErrMerchantEmailRequired
	}
	return nil
}

type UserChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (r *UserChangePasswordRequest) Validate() error {
	if r.CurrentPassword == "" {
		return ErrMerchantCurrentPasswordRequired
	}
	if r.NewPassword == "" {
		return ErrMerchantNewPasswordRequired
	}
	if len(r.NewPassword) < 8 {
		return ErrMerchantNewPasswordTooShort
	}
	if r.CurrentPassword == r.NewPassword {
		return ErrMerchantNewPasswordSame
	}
	return nil
}

type UserResponse struct {
	ID           uuid.UUID  `json:"id"`
	MerchantID   uuid.UUID  `json:"merchant_id"`
	Name         string     `json:"name"`
	Email        string     `json:"email"`
	Role         string     `json:"role"`
	IsActive     bool       `json:"is_active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// Business fields from joined merchant (optional on login)
	BusinessName string  `json:"business_name,omitempty"`
	WebhookURL   *string `json:"webhook_url,omitempty"`
}

type UserLoginResponse struct {
	Token     string        `json:"token"`
	TokenType string        `json:"token_type"`
	ExpiresIn int64         `json:"expires_in"`
	User      *UserResponse `json:"user"`
}

func ToUserResponse(u *User) *UserResponse {
	return &UserResponse{
		ID:          u.ID,
		MerchantID:  u.MerchantID,
		Name:        u.Name,
		Email:       u.Email,
		Role:        u.Role,
		IsActive:    u.IsActive,
		LastLoginAt: u.LastLoginAt,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}
