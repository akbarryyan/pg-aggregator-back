package payment

import "errors"

var (
	ErrPaymentNotFound            = errors.New("payment not found")
	ErrInvalidAmount              = errors.New("amount must be greater than 0")
	ErrUnsupportedCurrency        = errors.New("unsupported currency")
	ErrPaymentMethodRequired      = errors.New("payment method is required")
	ErrUnsupportedPaymentMethod   = errors.New("unsupported payment method")
	ErrMerchantIDRequired         = errors.New("merchant ID is required")
	ErrDescriptionRequired        = errors.New("description is required")
	ErrInvalidExpiration          = errors.New("expiration must be between 1 and 1440 minutes")
	ErrInvalidStatus              = errors.New("invalid payment status")
	ErrInvalidStatusTransition    = errors.New("invalid status transition")
	ErrPaymentAlreadyPaid         = errors.New("payment already paid")
	ErrPaymentExpired             = errors.New("payment expired")
	ErrPaymentAlreadyTerminal     = errors.New("payment is in terminal status and cannot be updated")
	ErrProviderError              = errors.New("provider error occurred")
	ErrWebhookValidationFailed    = errors.New("webhook validation failed")
	ErrDuplicateWebhook           = errors.New("duplicate webhook event")
	ErrInvalidProviderReference   = errors.New("invalid provider reference")
)
