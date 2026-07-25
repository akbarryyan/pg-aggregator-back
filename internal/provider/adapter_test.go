package provider

import (
	"context"
	"testing"

	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
)

// fakeProvider is a minimal PaymentProvider used only to exercise ProviderRouter
// routing/failover/health logic without depending on Cashi or sandbox adapters.
type fakeProvider struct {
	name string
}

func (f *fakeProvider) GetName() string { return f.name }
func (f *fakeProvider) CreatePayment(ctx context.Context, req *domainProvider.ProviderPaymentRequest) (*domainProvider.ProviderPaymentResponse, error) {
	return &domainProvider.ProviderPaymentResponse{ProviderName: f.name}, nil
}
func (f *fakeProvider) GetPaymentStatus(ctx context.Context, providerReference string) (*domainProvider.NormalizedPaymentStatus, error) {
	return &domainProvider.NormalizedPaymentStatus{Status: "pending"}, nil
}
func (f *fakeProvider) ValidateWebhook(rawPayload []byte, signature string) error { return nil }
func (f *fakeProvider) ParseWebhook(rawPayload []byte) (*domainProvider.ProviderWebhookPayload, error) {
	return nil, nil
}
func (f *fakeProvider) NormalizeStatus(providerStatus string) string { return providerStatus }

func TestRegisterProvider_And_GetProvider(t *testing.T) {
	r := NewProviderRouter()
	p := &fakeProvider{name: "Cashi"}
	r.RegisterProvider(p)

	got, exists := r.GetProvider("cashi")
	if !exists {
		t.Fatalf("expected provider to be found with lowercase lookup")
	}
	if got.GetName() != "Cashi" {
		t.Errorf("expected registered provider to be returned, got %q", got.GetName())
	}

	if _, exists := r.GetProvider("unknown"); exists {
		t.Errorf("expected unregistered provider to not be found")
	}
}

func TestRegisterProvider_DefaultsToHealthy(t *testing.T) {
	r := NewProviderRouter()
	r.RegisterProvider(&fakeProvider{name: "cashi"})

	health := r.GetProviderHealth("cashi")
	if health.Status != domainProvider.HealthStatusHealthy {
		t.Errorf("expected newly registered provider to default to healthy, got %q", health.Status)
	}
}

func TestGetProviderHealth_UnregisteredDefaultsHealthy(t *testing.T) {
	r := NewProviderRouter()
	health := r.GetProviderHealth("never-registered")
	if health.Status != domainProvider.HealthStatusHealthy {
		t.Errorf("expected default healthy status for unregistered provider, got %q", health.Status)
	}
}

func TestSelectProviders_UnsupportedPaymentMethod(t *testing.T) {
	r := NewProviderRouter()
	_, err := r.SelectProviders("qris")
	if err != ErrUnsupportedPaymentMethod {
		t.Fatalf("expected ErrUnsupportedPaymentMethod, got %v", err)
	}
}

func TestSelectProviders_ReturnsRegisteredProvidersInOrder(t *testing.T) {
	r := NewProviderRouter()
	r.RegisterProvider(&fakeProvider{name: "cashi"})
	r.RegisterProvider(&fakeProvider{name: "midtrans"})
	r.RegisterPaymentMethodProvider("qris", "cashi")
	r.RegisterPaymentMethodProvider("qris", "midtrans")

	providers, err := r.SelectProviders("qris")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}
	if providers[0].GetName() != "cashi" || providers[1].GetName() != "midtrans" {
		t.Errorf("expected providers in registration order, got %q then %q", providers[0].GetName(), providers[1].GetName())
	}
}

func TestRegisterPaymentMethodProvider_IgnoresDuplicates(t *testing.T) {
	r := NewProviderRouter()
	r.RegisterProvider(&fakeProvider{name: "cashi"})
	r.RegisterPaymentMethodProvider("qris", "cashi")
	r.RegisterPaymentMethodProvider("qris", "cashi")
	r.RegisterPaymentMethodProvider("qris", "Cashi") // different case, same key

	providers, err := r.SelectProviders("qris")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected duplicate registrations to collapse to 1 provider, got %d", len(providers))
	}
}

func TestSelectProviders_ExcludesUnhealthyProviders(t *testing.T) {
	r := NewProviderRouter()
	r.RegisterProvider(&fakeProvider{name: "cashi"})
	r.RegisterProvider(&fakeProvider{name: "midtrans"})
	r.RegisterPaymentMethodProvider("qris", "cashi")
	r.RegisterPaymentMethodProvider("qris", "midtrans")

	r.SetProviderHealth("cashi", domainProvider.HealthStatusUnhealthy, "timeout")

	providers, err := r.SelectProviders("qris")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(providers) != 1 || providers[0].GetName() != "midtrans" {
		t.Fatalf("expected only healthy midtrans to be selected, got %+v", providers)
	}
}

func TestSelectProviders_AllUnhealthyReturnsUnavailable(t *testing.T) {
	r := NewProviderRouter()
	r.RegisterProvider(&fakeProvider{name: "cashi"})
	r.RegisterPaymentMethodProvider("qris", "cashi")
	r.SetProviderHealth("cashi", domainProvider.HealthStatusUnhealthy, "down")

	_, err := r.SelectProviders("qris")
	if err != ErrProviderNotAvailable {
		t.Fatalf("expected ErrProviderNotAvailable, got %v", err)
	}
}

func TestSelectProvider_ReturnsFirstCandidate(t *testing.T) {
	r := NewProviderRouter()
	r.RegisterProvider(&fakeProvider{name: "cashi"})
	r.RegisterPaymentMethodProvider("qris", "cashi")

	p, err := r.SelectProvider("qris")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.GetName() != "cashi" {
		t.Errorf("expected cashi, got %q", p.GetName())
	}
}

func TestListProviderHealths_SortedByName(t *testing.T) {
	r := NewProviderRouter()
	r.RegisterProvider(&fakeProvider{name: "zeta"})
	r.RegisterProvider(&fakeProvider{name: "alpha"})

	healths := r.ListProviderHealths()
	if len(healths) != 2 {
		t.Fatalf("expected 2 healths, got %d", len(healths))
	}
	if healths[0].ProviderName != "alpha" || healths[1].ProviderName != "zeta" {
		t.Errorf("expected alphabetical order, got %q then %q", healths[0].ProviderName, healths[1].ProviderName)
	}
}

func TestListProviders_IncludesPaymentMethodsAndHealth(t *testing.T) {
	r := NewProviderRouter()
	r.RegisterProvider(&fakeProvider{name: "cashi"})
	r.RegisterPaymentMethodProvider("qris", "cashi")

	infos := r.ListProviders()
	if len(infos) != 1 {
		t.Fatalf("expected 1 provider info, got %d", len(infos))
	}
	info := infos[0]
	if info.Name != "cashi" {
		t.Errorf("expected name cashi, got %q", info.Name)
	}
	if !info.IsRegistered {
		t.Errorf("expected IsRegistered true")
	}
	if len(info.PaymentMethods) != 1 || info.PaymentMethods[0] != "qris" {
		t.Errorf("expected payment methods [qris], got %v", info.PaymentMethods)
	}
	if info.Health.Status != domainProvider.HealthStatusHealthy {
		t.Errorf("expected default healthy status, got %q", info.Health.Status)
	}
}
