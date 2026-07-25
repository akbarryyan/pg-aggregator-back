package merchant

import "errors"

var (
	ErrMerchantNotFound      = errors.New("merchant not found")
	ErrMerchantNameRequired  = errors.New("merchant name is required")
	ErrMerchantEmailRequired = errors.New("merchant email is required")
	ErrBusinessNameRequired  = errors.New("business name is required")
	ErrInvalidEmail          = errors.New("invalid email format")
	ErrMerchantAlreadyExists = errors.New("merchant with this email already exists")
	ErrMerchantInactive      = errors.New("merchant is inactive")
	ErrMerchantNameTooLong   = errors.New("merchant name must be at most 150 characters")
	ErrMerchantEmailTooLong  = errors.New("merchant email must be at most 255 characters")
	ErrBusinessNameTooLong   = errors.New("business name must be at most 255 characters")
	ErrMerchantPhoneTooLong  = errors.New("merchant phone must be at most 50 characters")
)
