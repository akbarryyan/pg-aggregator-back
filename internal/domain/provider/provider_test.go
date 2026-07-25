package provider

import "testing"

func TestUpsertMerchantProviderConfigRequest_Validate(t *testing.T) {
	t.Run("valid request passes and normalizes defaults", func(t *testing.T) {
		req := &UpsertMerchantProviderConfigRequest{
			ProviderName:  "cashi",
			PaymentMethod: "qris",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Priority != 1 {
			t.Errorf("expected priority to default to 1, got %d", req.Priority)
		}
		if req.Weight != 100 {
			t.Errorf("expected weight to default to 100, got %d", req.Weight)
		}
	})

	t.Run("provider name required", func(t *testing.T) {
		req := &UpsertMerchantProviderConfigRequest{PaymentMethod: "qris"}
		if err := req.Validate(); err != ErrProviderConfigProviderRequired {
			t.Fatalf("expected ErrProviderConfigProviderRequired, got %v", err)
		}
	})

	t.Run("payment method required", func(t *testing.T) {
		req := &UpsertMerchantProviderConfigRequest{ProviderName: "cashi"}
		if err := req.Validate(); err != ErrProviderConfigPaymentMethodRequired {
			t.Fatalf("expected ErrProviderConfigPaymentMethodRequired, got %v", err)
		}
	})

	t.Run("positive priority and weight preserved", func(t *testing.T) {
		req := &UpsertMerchantProviderConfigRequest{
			ProviderName:  "cashi",
			PaymentMethod: "qris",
			Priority:      5,
			Weight:        50,
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Priority != 5 || req.Weight != 50 {
			t.Errorf("expected explicit priority/weight to be preserved, got priority=%d weight=%d", req.Priority, req.Weight)
		}
	})
}

func TestUpdateProviderHealthRequest_Validate(t *testing.T) {
	validStatuses := []string{HealthStatusHealthy, HealthStatusDegraded, HealthStatusUnhealthy}
	for _, status := range validStatuses {
		req := &UpdateProviderHealthRequest{Status: status}
		if err := req.Validate(); err != nil {
			t.Errorf("expected status %q to be valid, got error %v", status, err)
		}
	}

	req := &UpdateProviderHealthRequest{Status: "on_fire"}
	if err := req.Validate(); err != ErrInvalidProviderHealthStatus {
		t.Errorf("expected ErrInvalidProviderHealthStatus, got %v", err)
	}
}
