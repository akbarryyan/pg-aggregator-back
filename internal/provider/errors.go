package provider

import "errors"

var (
	ErrProviderNotAvailable     = errors.New("provider not available")
	ErrUnsupportedPaymentMethod = errors.New("unsupported payment method")
	ErrProviderAPIError         = errors.New("provider API error")
	ErrInvalidWebhookSignature  = errors.New("invalid webhook signature")
	ErrInvalidWebhookPayload    = errors.New("invalid webhook payload")
	ErrTestWebhookEvent         = errors.New("test webhook event ignored")
)
