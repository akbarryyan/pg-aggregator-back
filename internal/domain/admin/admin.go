package admin

import (
	"time"

	"github.com/google/uuid"
)

type Admin struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Name         string     `json:"name" db:"name"`
	Email        string     `json:"email" db:"email"`
	PasswordHash string     `json:"-" db:"password_hash"`
	IsActive     bool       `json:"is_active" db:"is_active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty" db:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at" db:"updated_at"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r *LoginRequest) Validate() error {
	if r.Email == "" {
		return ErrEmailRequired
	}
	if r.Password == "" {
		return ErrPasswordRequired
	}
	return nil
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (r *ChangePasswordRequest) Validate() error {
	if r.CurrentPassword == "" {
		return ErrCurrentPasswordRequired
	}
	if r.NewPassword == "" {
		return ErrNewPasswordRequired
	}
	if len(r.NewPassword) < 8 {
		return ErrNewPasswordTooShort
	}
	if r.CurrentPassword == r.NewPassword {
		return ErrNewPasswordSameAsCurrent
	}
	return nil
}

type UpdateProfileRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (r *UpdateProfileRequest) Validate() error {
	if r.Name == "" {
		return ErrNameRequired
	}
	if r.Email == "" {
		return ErrEmailRequired
	}
	return nil
}

type AdminResponse struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Email       string     `json:"email"`
	IsActive    bool       `json:"is_active"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type LoginResponse struct {
	Token     string         `json:"token"`
	TokenType string         `json:"token_type"`
	ExpiresIn int64          `json:"expires_in"`
	Admin     *AdminResponse `json:"admin"`
}

func ToAdminResponse(a *Admin) *AdminResponse {
	return &AdminResponse{
		ID:          a.ID,
		Name:        a.Name,
		Email:       a.Email,
		IsActive:    a.IsActive,
		LastLoginAt: a.LastLoginAt,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
}
