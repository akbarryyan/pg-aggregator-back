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
	ErrMerchantPasswordTooShort        = errors.New("password must be at least 8 characters")
	ErrMerchantPasswordTooLong         = errors.New("password must be at most 72 characters")
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

type RegisterRequest struct {
	Name         string `json:"name"`
	BusinessName string `json:"business_name"`
	Email        string `json:"email"`
	Phone        string `json:"phone,omitempty"`
	Password     string `json:"password"`
}

func (r *RegisterRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return ErrMerchantNameRequired
	}
	if len(r.Name) > 150 {
		return ErrMerchantNameTooLong
	}
	if strings.TrimSpace(r.BusinessName) == "" {
		return ErrBusinessNameRequired
	}
	if len(r.BusinessName) > 255 {
		return ErrBusinessNameTooLong
	}
	if strings.TrimSpace(r.Email) == "" {
		return ErrMerchantEmailRequired
	}
	if len(r.Email) > 255 {
		return ErrMerchantEmailTooLong
	}
	if r.Phone != "" && len(r.Phone) > 50 {
		return ErrMerchantPhoneTooLong
	}
	if r.Password == "" {
		return ErrMerchantPasswordRequired
	}
	if len(r.Password) < 8 {
		return ErrMerchantPasswordTooShort
	}
	if len(r.Password) > 72 {
		return ErrMerchantPasswordTooLong
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
