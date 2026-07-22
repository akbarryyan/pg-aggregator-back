package provider

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
)

type PaymentProvider interface {
	GetName() string

	CreatePayment(ctx context.Context, req *domainProvider.ProviderPaymentRequest) (*domainProvider.ProviderPaymentResponse, error)

	GetPaymentStatus(ctx context.Context, providerReference string) (*domainProvider.NormalizedPaymentStatus, error)

	ValidateWebhook(rawPayload []byte, signature string) error

	ParseWebhook(rawPayload []byte) (*domainProvider.ProviderWebhookPayload, error)

	NormalizeStatus(providerStatus string) string
}

type ProviderRouter struct {
	providers              map[string]PaymentProvider
	paymentMethodProviders map[string][]string
	providerHealths        map[string]domainProvider.ProviderHealth
	mu                     sync.RWMutex
}

func NewProviderRouter() *ProviderRouter {
	return &ProviderRouter{
		providers:              make(map[string]PaymentProvider),
		paymentMethodProviders: make(map[string][]string),
		providerHealths:        make(map[string]domainProvider.ProviderHealth),
	}
}

func (r *ProviderRouter) RegisterProvider(provider PaymentProvider) {
	providerKey := normalizeKey(provider.GetName())

	r.mu.Lock()
	defer r.mu.Unlock()

	r.providers[providerKey] = provider
	if _, exists := r.providerHealths[providerKey]; !exists {
		r.providerHealths[providerKey] = domainProvider.ProviderHealth{
			ProviderName: provider.GetName(),
			Status:       domainProvider.HealthStatusHealthy,
			UpdatedAt:    time.Now(),
		}
	}
}

func (r *ProviderRouter) RegisterPaymentMethodProvider(paymentMethod, providerName string) {
	methodKey := normalizeKey(paymentMethod)
	providerKey := normalizeKey(providerName)

	r.mu.Lock()
	defer r.mu.Unlock()

	existingProviders := r.paymentMethodProviders[methodKey]
	for _, existingProvider := range existingProviders {
		if existingProvider == providerKey {
			return
		}
	}

	r.paymentMethodProviders[methodKey] = append(existingProviders, providerKey)
}

func (r *ProviderRouter) GetProvider(name string) (PaymentProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	paymentProvider, exists := r.providers[normalizeKey(name)]
	return paymentProvider, exists
}

func (r *ProviderRouter) SelectProvider(paymentMethod string) (PaymentProvider, error) {
	providers, err := r.SelectProviders(paymentMethod)
	if err != nil {
		return nil, err
	}

	return providers[0], nil
}

func (r *ProviderRouter) SelectProviders(paymentMethod string) ([]PaymentProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerNames, exists := r.paymentMethodProviders[normalizeKey(paymentMethod)]
	if !exists {
		return nil, ErrUnsupportedPaymentMethod
	}

	selectedProviders := make([]PaymentProvider, 0, len(providerNames))
	for _, providerName := range providerNames {
		selectedProvider, exists := r.providers[normalizeKey(providerName)]
		if !exists {
			continue
		}
		if !r.isProviderAvailableLocked(providerName) {
			continue
		}
		selectedProviders = append(selectedProviders, selectedProvider)
	}

	if len(selectedProviders) == 0 {
		return nil, ErrProviderNotAvailable
	}

	return selectedProviders, nil
}

func (r *ProviderRouter) SetProviderHealth(providerName, status, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	providerKey := normalizeKey(providerName)
	r.providerHealths[providerKey] = domainProvider.ProviderHealth{
		ProviderName: providerName,
		Status:       status,
		Reason:       reason,
		UpdatedAt:    time.Now(),
	}
}

func (r *ProviderRouter) GetProviderHealth(providerName string) domainProvider.ProviderHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerKey := normalizeKey(providerName)
	health, exists := r.providerHealths[providerKey]
	if !exists {
		return domainProvider.ProviderHealth{
			ProviderName: providerName,
			Status:       domainProvider.HealthStatusHealthy,
		}
	}
	return health
}

func (r *ProviderRouter) ListProviderHealths() []domainProvider.ProviderHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()

	healths := make([]domainProvider.ProviderHealth, 0, len(r.providerHealths))
	for _, health := range r.providerHealths {
		healths = append(healths, health)
	}

	sort.Slice(healths, func(i, j int) bool {
		return normalizeKey(healths[i].ProviderName) < normalizeKey(healths[j].ProviderName)
	})

	return healths
}

// ProviderInfo is a safe admin-facing summary of a registered provider.
// Credentials are never included.
type ProviderInfo struct {
	Name            string                       `json:"name"`
	PaymentMethods  []string                     `json:"payment_methods"`
	Health          domainProvider.ProviderHealth `json:"health"`
	IsRegistered    bool                         `json:"is_registered"`
}

func (r *ProviderRouter) ListProviders() []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	methodByProvider := make(map[string][]string)
	for method, providerNames := range r.paymentMethodProviders {
		for _, providerName := range providerNames {
			methodByProvider[providerName] = append(methodByProvider[providerName], method)
		}
	}

	infos := make([]ProviderInfo, 0, len(r.providers))
	for key, p := range r.providers {
		methods := methodByProvider[key]
		if methods == nil {
			methods = []string{}
		}
		sort.Strings(methods)

		health, exists := r.providerHealths[key]
		if !exists {
			health = domainProvider.ProviderHealth{
				ProviderName: p.GetName(),
				Status:       domainProvider.HealthStatusHealthy,
				UpdatedAt:    time.Now(),
			}
		}

		infos = append(infos, ProviderInfo{
			Name:           p.GetName(),
			PaymentMethods: methods,
			Health:         health,
			IsRegistered:   true,
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		return normalizeKey(infos[i].Name) < normalizeKey(infos[j].Name)
	})

	return infos
}

func (r *ProviderRouter) isProviderAvailableLocked(providerName string) bool {
	health, exists := r.providerHealths[normalizeKey(providerName)]
	if !exists {
		return true
	}

	return health.Status != domainProvider.HealthStatusUnhealthy
}

func normalizeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
