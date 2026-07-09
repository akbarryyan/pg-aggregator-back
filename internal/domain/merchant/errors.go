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
)
