package provider

import "errors"

var (
	ErrProviderConfigProviderRequired      = errors.New("provider name is required")
	ErrProviderConfigPaymentMethodRequired = errors.New("payment method is required")
	ErrInvalidProviderHealthStatus         = errors.New("invalid provider health status")
)
