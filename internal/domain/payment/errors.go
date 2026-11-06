package payment

import "errors"

var (
	ErrPaymentNotFound          = errors.New("payment not found")
	ErrInvalidAmount            = errors.New("amount must be greater than 0 and at most 1000000000000")
	ErrUnsupportedCurrency      = errors.New("unsupported currency")
	ErrPaymentMethodRequired    = errors.New("payment method is required")
	ErrUnsupportedPaymentMethod = errors.New("unsupported payment method")
	ErrMerchantIDRequired       = errors.New("merchant ID is required")
	ErrDescriptionRequired      = errors.New("description is required")
	ErrInvalidExpiration        = errors.New("expiration must be between 1 and 1440 minutes")
	ErrInvalidStatus            = errors.New("invalid payment status")
	ErrInvalidStatusTransition  = errors.New("invalid status transition")
	ErrPaymentAlreadyPaid       = errors.New("payment already paid")
	ErrPaymentExpired           = errors.New("payment expired")
	ErrPaymentAlreadyTerminal   = errors.New("payment is in terminal status and cannot be updated")
	ErrProviderError            = errors.New("provider error occurred")
	ErrWebhookValidationFailed  = errors.New("webhook validation failed")
	ErrDuplicateWebhook         = errors.New("duplicate webhook event")
	ErrInvalidProviderReference = errors.New("invalid provider reference")
	ErrInvalidEnvironment       = errors.New("invalid environment: use sandbox or production")
	ErrProviderMismatch         = errors.New("webhook provider does not own this payment")

	// ErrAmountMismatch's text is the "amount_mismatch" prefix the reconcile
	// job uses to skip payments already flagged for a human.
	ErrAmountMismatch = errors.New("amount_mismatch")
	// ErrWebhookAmountMismatch: the provider confirms the stored amount but the
	// webhook claims another. The webhook is rejected, but the payment is NOT
	// excluded from reconcile (whose LIKE 'amount_mismatch%' filter this text
	// deliberately does not match) — check-status is the source of truth.
	ErrWebhookAmountMismatch      = errors.New("webhook_amount_mismatch")
	ErrNotSettledAtProvider       = errors.New("provider has not settled the payment")
	ErrProviderVerificationFailed = errors.New("could not confirm the payment with the provider")
	ErrWebhookIgnored             = errors.New("webhook event ignored: not a settlement")
)
