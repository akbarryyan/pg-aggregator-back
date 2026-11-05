package provider

import "errors"

var (
	ErrProviderNotAvailable     = errors.New("provider not available")
	ErrUnsupportedPaymentMethod = errors.New("unsupported payment method")
	ErrProviderAPIError         = errors.New("provider API error")
	// ErrProviderTimeout means the provider did not answer within the client
	// timeout; callers map it to 504 Gateway Timeout.
	ErrProviderTimeout         = errors.New("provider timeout")
	ErrInvalidWebhookSignature = errors.New("invalid webhook signature")
	ErrInvalidWebhookPayload   = errors.New("invalid webhook payload")
	ErrTestWebhookEvent        = errors.New("test webhook event ignored")
)
