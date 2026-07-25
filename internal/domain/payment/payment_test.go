package payment

import (
	"testing"

	"github.com/google/uuid"
)

func validRequest() *CreatePaymentRequest {
	return &CreatePaymentRequest{
		MerchantID:    uuid.New(),
		Amount:        15000,
		Currency:      CurrencyIDR,
		PaymentMethod: PaymentMethodQRIS,
		Description:   "Test payment",
	}
}

func TestCreatePaymentRequest_Validate(t *testing.T) {
	t.Run("valid request passes", func(t *testing.T) {
		req := validRequest()
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("amount must be positive", func(t *testing.T) {
		req := validRequest()
		req.Amount = 0
		if err := req.Validate(); err != ErrInvalidAmount {
			t.Fatalf("expected ErrInvalidAmount, got %v", err)
		}

		req.Amount = -100
		if err := req.Validate(); err != ErrInvalidAmount {
			t.Fatalf("expected ErrInvalidAmount, got %v", err)
		}
	})

	t.Run("empty currency defaults to IDR", func(t *testing.T) {
		req := validRequest()
		req.Currency = ""
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Currency != CurrencyIDR {
			t.Fatalf("expected currency to default to IDR, got %q", req.Currency)
		}
	})

	t.Run("non-IDR currency rejected", func(t *testing.T) {
		req := validRequest()
		req.Currency = "USD"
		if err := req.Validate(); err != ErrUnsupportedCurrency {
			t.Fatalf("expected ErrUnsupportedCurrency, got %v", err)
		}
	})

	t.Run("payment method required", func(t *testing.T) {
		req := validRequest()
		req.PaymentMethod = ""
		if err := req.Validate(); err != ErrPaymentMethodRequired {
			t.Fatalf("expected ErrPaymentMethodRequired, got %v", err)
		}
	})

	t.Run("only qris supported for now", func(t *testing.T) {
		req := validRequest()
		req.PaymentMethod = "virtual_account"
		if err := req.Validate(); err != ErrUnsupportedPaymentMethod {
			t.Fatalf("expected ErrUnsupportedPaymentMethod, got %v", err)
		}
	})

	t.Run("merchant id required", func(t *testing.T) {
		req := validRequest()
		req.MerchantID = uuid.Nil
		if err := req.Validate(); err != ErrMerchantIDRequired {
			t.Fatalf("expected ErrMerchantIDRequired, got %v", err)
		}
	})

	t.Run("description required", func(t *testing.T) {
		req := validRequest()
		req.Description = ""
		if err := req.Validate(); err != ErrDescriptionRequired {
			t.Fatalf("expected ErrDescriptionRequired, got %v", err)
		}
	})

	t.Run("expires_in_minutes defaults to 30 when unset", func(t *testing.T) {
		req := validRequest()
		req.ExpiresInMinutes = 0
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.ExpiresInMinutes != 30 {
			t.Fatalf("expected default 30 minutes, got %d", req.ExpiresInMinutes)
		}
	})

	t.Run("expires_in_minutes above 1440 rejected", func(t *testing.T) {
		req := validRequest()
		req.ExpiresInMinutes = 1441
		if err := req.Validate(); err != ErrInvalidExpiration {
			t.Fatalf("expected ErrInvalidExpiration, got %v", err)
		}
	})

	t.Run("expires_in_minutes at boundary accepted", func(t *testing.T) {
		req := validRequest()
		req.ExpiresInMinutes = 1440
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error at boundary, got %v", err)
		}
	})

	t.Run("environment normalized as part of validation", func(t *testing.T) {
		req := validRequest()
		req.Environment = "test"
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Environment != EnvironmentSandbox {
			t.Fatalf("expected environment normalized to sandbox, got %q", req.Environment)
		}
	})
}

func TestNormalizeEnvironment(t *testing.T) {
	cases := map[string]string{
		"sandbox":     EnvironmentSandbox,
		"test":        EnvironmentSandbox,
		"dev":         EnvironmentSandbox,
		"development": EnvironmentSandbox,
		"production":  EnvironmentProduction,
		"prod":        EnvironmentProduction,
		"live":        EnvironmentProduction,
		"":            EnvironmentProduction,
		"garbage":     EnvironmentProduction,
	}

	for input, want := range cases {
		if got := NormalizeEnvironment(input); got != want {
			t.Errorf("NormalizeEnvironment(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsTerminalStatus(t *testing.T) {
	terminal := []string{StatusPaid, StatusExpired, StatusFailed, StatusCancelled}
	for _, s := range terminal {
		if !IsTerminalStatus(s) {
			t.Errorf("expected %q to be terminal", s)
		}
	}

	if IsTerminalStatus(StatusPending) {
		t.Errorf("expected pending to not be terminal")
	}
	if IsTerminalStatus("unknown") {
		t.Errorf("expected unknown status to not be terminal")
	}
}

func TestIsValidStatus(t *testing.T) {
	valid := []string{StatusPending, StatusPaid, StatusExpired, StatusFailed, StatusCancelled}
	for _, s := range valid {
		if !IsValidStatus(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	if IsValidStatus("bogus") {
		t.Errorf("expected bogus status to be invalid")
	}
}

func TestCanTransitionTo(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		to      string
		allowed bool
	}{
		{"pending to paid allowed", StatusPending, StatusPaid, true},
		{"pending to expired allowed", StatusPending, StatusExpired, true},
		{"pending to failed allowed", StatusPending, StatusFailed, true},
		{"pending to cancelled allowed", StatusPending, StatusCancelled, true},
		{"same status not a transition", StatusPending, StatusPending, false},
		{"paid is terminal, cannot move to expired", StatusPaid, StatusExpired, false},
		{"expired is terminal, cannot move to paid", StatusExpired, StatusPaid, false},
		{"failed is terminal, cannot move to pending", StatusFailed, StatusPending, false},
		{"cancelled is terminal, cannot move to paid", StatusCancelled, StatusPaid, false},
		{"unknown source status rejected", "unknown", StatusPaid, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanTransitionTo(tc.from, tc.to); got != tc.allowed {
				t.Errorf("CanTransitionTo(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.allowed)
			}
		})
	}
}

func TestToPaymentResponse_DefaultsEnvironmentAndBuildsCheckoutURL(t *testing.T) {
	p := &Payment{
		ID:        uuid.New(),
		Reference: "PAY-123",
		Status:    StatusPending,
	}

	resp := ToPaymentResponse(p, "https://app.example.com")
	if resp.Environment != EnvironmentProduction {
		t.Errorf("expected empty environment to default to production, got %q", resp.Environment)
	}
	if resp.CheckoutURL != "https://app.example.com/pay/PAY-123" {
		t.Errorf("unexpected checkout URL: %q", resp.CheckoutURL)
	}
}
