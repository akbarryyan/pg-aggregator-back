package provider

import (
	"context"

	"pg-aggregator/internal/domain/provider"
)

type PaymentProvider interface {
	GetName() string
	
	CreatePayment(ctx context.Context, req *provider.ProviderPaymentRequest) (*provider.ProviderPaymentResponse, error)
	
	GetPaymentStatus(ctx context.Context, providerReference string) (*provider.NormalizedPaymentStatus, error)
	
	ValidateWebhook(rawPayload []byte, signature string) error
	
	ParseWebhook(rawPayload []byte) (*provider.ProviderWebhookPayload, error)
	
	NormalizeStatus(providerStatus string) string
}

type ProviderRouter struct {
	providers map[string]PaymentProvider
}

func NewProviderRouter() *ProviderRouter {
	return &ProviderRouter{
		providers: make(map[string]PaymentProvider),
	}
}

func (r *ProviderRouter) RegisterProvider(provider PaymentProvider) {
	r.providers[provider.GetName()] = provider
}

func (r *ProviderRouter) GetProvider(name string) (PaymentProvider, bool) {
	provider, exists := r.providers[name]
	return provider, exists
}

func (r *ProviderRouter) SelectProvider(paymentMethod string) (PaymentProvider, error) {
	switch paymentMethod {
	case "qris":
		provider, exists := r.GetProvider(provider.ProviderKlikQris)
		if !exists {
			return nil, ErrProviderNotAvailable
		}
		return provider, nil
	default:
		return nil, ErrUnsupportedPaymentMethod
	}
}
