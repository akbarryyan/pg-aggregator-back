package provider

import "errors"

var (
	ErrProviderConfigProviderRequired         = errors.New("provider name is required")
	ErrProviderConfigPaymentMethodRequired    = errors.New("payment method is required")
	ErrInvalidProviderHealthStatus            = errors.New("invalid provider health status")
	ErrProviderConfigSandboxNotAllowed        = errors.New("the sandbox provider cannot be configured for production routing")
	ErrProviderConfigUnknownProvider          = errors.New("unknown provider")
	ErrProviderConfigUnsupportedPaymentMethod = errors.New("unsupported payment method")
)
