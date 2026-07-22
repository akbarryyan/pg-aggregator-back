package admin

import "errors"

var (
	ErrEmailRequired            = errors.New("email is required")
	ErrNameRequired             = errors.New("name is required")
	ErrPasswordRequired         = errors.New("password is required")
	ErrCurrentPasswordRequired  = errors.New("current password is required")
	ErrNewPasswordRequired      = errors.New("new password is required")
	ErrNewPasswordTooShort      = errors.New("new password must be at least 8 characters")
	ErrNewPasswordSameAsCurrent = errors.New("new password must be different from current password")
	ErrCurrentPasswordInvalid   = errors.New("current password is incorrect")
	ErrAdminNotFound            = errors.New("admin not found")
	ErrEmailAlreadyUsed         = errors.New("email is already in use")
	ErrInvalidCredentials       = errors.New("invalid email or password")
	ErrAdminInactive            = errors.New("admin account is inactive")
	ErrInvalidToken             = errors.New("invalid or expired token")
)
