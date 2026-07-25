package service

import (
	"context"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/google/uuid"
)

func newTestPaymentService() (*PaymentService, *fakePaymentRepo, *fakeMerchantProviderConfigRepo, *providerPkg.ProviderRouter) {
	paymentRepo := newFakePaymentRepo()
	configRepo := &fakeMerchantProviderConfigRepo{}
	webhookRepo := newFakeWebhookEventRepo()
	router := providerPkg.NewProviderRouter()

	svc := NewPaymentService(paymentRepo, configRepo, webhookRepo, router, "http://localhost:8080")
	return svc, paymentRepo, configRepo, router
}

func baseCreateRequest(merchantID uuid.UUID) *payment.CreatePaymentRequest {
	return &payment.CreatePaymentRequest{
		MerchantID:    merchantID,
		Amount:        15000,
		Currency:      payment.CurrencyIDR,
		PaymentMethod: payment.PaymentMethodQRIS,
		Description:   "unit test payment",
	}
}

func TestCreatePayment_Sandbox_UsesSandboxProviderOnly(t *testing.T) {
	svc, paymentRepo, _, router := newTestPaymentService()
	sandbox := &fakeProvider{name: "sandbox"}
	svc = svc.WithSandboxProvider(sandbox)
	// No production providers registered at all — sandbox path must not touch the router.
	_ = router

	req := baseCreateRequest(uuid.New())
	req.Environment = payment.EnvironmentSandbox

	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ProviderName != "sandbox" {
		t.Errorf("expected sandbox provider, got %q", p.ProviderName)
	}
	if p.Environment != payment.EnvironmentSandbox {
		t.Errorf("expected sandbox environment, got %q", p.Environment)
	}
	if _, err := paymentRepo.GetByID(context.Background(), p.ID); err != nil {
		t.Errorf("expected payment to be persisted: %v", err)
	}
}

func TestCreatePayment_Sandbox_WithoutProviderConfigured(t *testing.T) {
	svc, _, _, _ := newTestPaymentService()
	// sandbox provider intentionally not wired via WithSandboxProvider

	req := baseCreateRequest(uuid.New())
	req.Environment = payment.EnvironmentSandbox

	_, err := svc.CreatePayment(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error when sandbox provider is not configured")
	}
}

func TestCreatePayment_Production_DefaultRouting(t *testing.T) {
	svc, paymentRepo, _, router := newTestPaymentService()
	cashi := &fakeProvider{name: "cashi"}
	router.RegisterProvider(cashi)
	router.RegisterPaymentMethodProvider("qris", "cashi")

	req := baseCreateRequest(uuid.New())
	req.Environment = payment.EnvironmentProduction

	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ProviderName != "cashi" {
		t.Errorf("expected default-routed cashi provider, got %q", p.ProviderName)
	}
	if _, err := paymentRepo.GetByID(context.Background(), p.ID); err != nil {
		t.Errorf("expected payment to be persisted: %v", err)
	}
}

func TestCreatePayment_Production_NoProviderForMethod(t *testing.T) {
	svc, _, _, _ := newTestPaymentService()
	// No provider registered for "qris" at all.

	req := baseCreateRequest(uuid.New())
	req.Environment = payment.EnvironmentProduction

	_, err := svc.CreatePayment(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error when no provider is available for payment method")
	}
}

func TestCreatePayment_Production_MerchantSpecificRouting(t *testing.T) {
	svc, _, configRepo, router := newTestPaymentService()
	cashi := &fakeProvider{name: "cashi"}
	midtrans := &fakeProvider{name: "midtrans"}
	router.RegisterProvider(cashi)
	router.RegisterProvider(midtrans)
	// Default router routing points at midtrans, but merchant override should win.
	router.RegisterPaymentMethodProvider("qris", "midtrans")

	merchantID := uuid.New()
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: merchantID, ProviderName: "cashi", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: false},
	}

	req := baseCreateRequest(merchantID)
	req.Environment = payment.EnvironmentProduction

	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ProviderName != "cashi" {
		t.Errorf("expected merchant-specific provider cashi to win over default routing, got %q", p.ProviderName)
	}
}

func TestCreatePayment_Production_FailoverToNextProvider(t *testing.T) {
	svc, _, configRepo, router := newTestPaymentService()
	failing := &fakeProvider{name: "cashi", createErr: providerPkg.ErrProviderAPIError}
	healthy := &fakeProvider{name: "midtrans"}
	router.RegisterProvider(failing)
	router.RegisterProvider(healthy)

	merchantID := uuid.New()
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: merchantID, ProviderName: "cashi", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 1},
		{MerchantID: merchantID, ProviderName: "midtrans", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 2},
	}

	req := baseCreateRequest(merchantID)
	req.Environment = payment.EnvironmentProduction

	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ProviderName != "midtrans" {
		t.Errorf("expected failover to midtrans after cashi failed, got %q", p.ProviderName)
	}
}

func TestCreatePayment_Production_FailoverDisabled_StopsOnFirstError(t *testing.T) {
	svc, _, configRepo, router := newTestPaymentService()
	failing := &fakeProvider{name: "cashi", createErr: providerPkg.ErrProviderAPIError}
	healthy := &fakeProvider{name: "midtrans"}
	router.RegisterProvider(failing)
	router.RegisterProvider(healthy)

	merchantID := uuid.New()
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: merchantID, ProviderName: "cashi", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: false, Priority: 1},
		{MerchantID: merchantID, ProviderName: "midtrans", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 2},
	}

	req := baseCreateRequest(merchantID)
	req.Environment = payment.EnvironmentProduction

	_, err := svc.CreatePayment(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error: failover disabled should not try the next candidate")
	}
}

func TestCreatePayment_Production_SkipsUnhealthyProvider(t *testing.T) {
	svc, _, _, router := newTestPaymentService()
	unhealthy := &fakeProvider{name: "cashi"}
	healthy := &fakeProvider{name: "midtrans"}
	router.RegisterProvider(unhealthy)
	router.RegisterProvider(healthy)
	router.RegisterPaymentMethodProvider("qris", "cashi")
	router.RegisterPaymentMethodProvider("qris", "midtrans")
	router.SetProviderHealth("cashi", domainProvider.HealthStatusUnhealthy, "down for maintenance")

	req := baseCreateRequest(uuid.New())
	req.Environment = payment.EnvironmentProduction

	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ProviderName != "midtrans" {
		t.Errorf("expected unhealthy cashi to be skipped in favor of midtrans, got %q", p.ProviderName)
	}
}

func TestCreatePayment_ValidationErrorDoesNotPersist(t *testing.T) {
	svc, paymentRepo, _, _ := newTestPaymentService()

	req := baseCreateRequest(uuid.New())
	req.Amount = 0 // invalid

	_, err := svc.CreatePayment(context.Background(), req)
	if err != payment.ErrInvalidAmount {
		t.Fatalf("expected ErrInvalidAmount, got %v", err)
	}
	if len(paymentRepo.byID) != 0 {
		t.Errorf("expected no payment to be persisted on validation failure")
	}
}
