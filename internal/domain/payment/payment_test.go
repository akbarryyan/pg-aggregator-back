package payment

import (
	"errors"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/fee"
	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
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

	t.Run("amount upper bound", func(t *testing.T) {
		req := validRequest()
		req.Amount = fee.MaxNominal
		if err := req.Validate(); err != nil {
			t.Fatalf("MaxNominal must be accepted, got %v", err)
		}
		req = validRequest()
		req.Amount = fee.MaxNominal + 1
		if err := req.Validate(); err != ErrInvalidAmount {
			t.Fatalf("MaxNominal+1 must be rejected with ErrInvalidAmount, got %v", err)
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
	terminal := []string{StatusPaid, StatusExpired, StatusFailed, StatusCancelled, StatusRefunded}
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
	valid := []string{StatusPending, StatusPaid, StatusExpired, StatusFailed, StatusCancelled, StatusRefunded}
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
		{"expired to paid allowed (late settlement)", StatusExpired, StatusPaid, true},
		{"expired cannot move to failed", StatusExpired, StatusFailed, false},
		{"paid to paid is not a transition", StatusPaid, StatusPaid, false},
		{"failed is terminal, cannot move to pending", StatusFailed, StatusPending, false},
		{"cancelled is terminal, cannot move to paid", StatusCancelled, StatusPaid, false},
		{"unknown source status rejected", "unknown", StatusPaid, false},
		{"paid to refunded allowed", StatusPaid, StatusRefunded, true},
		{"pending to refunded rejected", StatusPending, StatusRefunded, false},
		{"expired to refunded rejected", StatusExpired, StatusRefunded, false},
		{"failed to refunded rejected", StatusFailed, StatusRefunded, false},
		{"cancelled to refunded rejected", StatusCancelled, StatusRefunded, false},
		{"refunded to refunded rejected", StatusRefunded, StatusRefunded, false},
		{"refunded to paid rejected", StatusRefunded, StatusPaid, false},
		{"refunded to pending rejected", StatusRefunded, StatusPending, false},
		{"refunded to expired rejected", StatusRefunded, StatusExpired, false},
		{"refunded to failed rejected", StatusRefunded, StatusFailed, false},
		{"refunded to cancelled rejected", StatusRefunded, StatusCancelled, false},
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

func TestAllowedSourceStatuses(t *testing.T) {
	cases := map[string][]string{
		StatusPaid:      {StatusPending, StatusExpired},
		StatusExpired:   {StatusPending},
		StatusFailed:    {StatusPending},
		StatusCancelled: {StatusPending},
		StatusRefunded:  {StatusPaid},
		StatusPending:   nil,
		"unknown":       nil,
	}
	for to, want := range cases {
		got := AllowedSourceStatuses(to)
		if len(got) != len(want) {
			t.Fatalf("AllowedSourceStatuses(%q) = %v, want %v", to, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("AllowedSourceStatuses(%q) = %v, want %v", to, got, want)
			}
		}
	}
}

func TestParseEnvironment(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", EnvironmentSandbox, false},
		{"   ", EnvironmentSandbox, false},
		{"sandbox", EnvironmentSandbox, false},
		{"test", EnvironmentSandbox, false},
		{"dev", EnvironmentSandbox, false},
		{"development", EnvironmentSandbox, false},
		{"production", EnvironmentProduction, false},
		{"prod", EnvironmentProduction, false},
		{"live", EnvironmentProduction, false},
		{" Production ", EnvironmentProduction, false},
		{"prodution", "", true},
		{"garbage", "", true},
	}
	for _, tc := range cases {
		got, err := ParseEnvironment(tc.in)
		if tc.wantErr {
			if !errors.Is(err, ErrInvalidEnvironment) {
				t.Errorf("ParseEnvironment(%q) error = %v, want ErrInvalidEnvironment", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseEnvironment(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestCreatePaymentRequest_ValidateEnvironment(t *testing.T) {
	base := func() *CreatePaymentRequest {
		return &CreatePaymentRequest{MerchantID: uuid.New(), Amount: 10000, PaymentMethod: PaymentMethodQRIS, Description: "x"}
	}
	r := base()
	if err := r.Validate(); err != nil || r.Environment != EnvironmentSandbox {
		t.Fatalf("empty environment: Validate() = %v, env %q; want nil, sandbox", err, r.Environment)
	}
	r = base()
	r.Environment = "nope"
	if err := r.Validate(); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("unknown environment: Validate() = %v, want ErrInvalidEnvironment", err)
	}
}

func TestCreatePaymentRequest_ValidateCallbackURL(t *testing.T) {
	base := func(env, cb string) *CreatePaymentRequest {
		r := &CreatePaymentRequest{MerchantID: uuid.New(), Amount: 10000, PaymentMethod: PaymentMethodQRIS, Description: "x", Environment: env}
		if cb != "NIL" {
			r.CallbackURL = &cb
		}
		return r
	}
	cases := []struct {
		name    string
		env     string
		cb      string
		wantErr bool
		wantNil bool
	}{
		{"production https ok", EnvironmentProduction, "https://shop.example.com/cb", false, false},
		{"production http rejected", EnvironmentProduction, "http://shop.example.com/cb", true, false},
		{"production internal rejected", EnvironmentProduction, "https://169.254.169.254/x", true, false},
		{"sandbox http public ok", EnvironmentSandbox, "http://shop.example.com/cb", false, false},
		{"sandbox http localhost rejected", EnvironmentSandbox, "http://localhost:3000/cb", true, false},
		{"sandbox internal ip rejected", EnvironmentSandbox, "http://169.254.169.254/x", true, false},
		{"empty env means sandbox, http public ok", "", "http://shop.example.com/cb", false, false},
		{"blank callback becomes nil", EnvironmentProduction, "   ", false, true},
		{"absent callback ok", EnvironmentProduction, "NIL", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base(tc.env, tc.cb)
			err := r.Validate()
			if tc.wantErr {
				if !errors.Is(err, netsafe.ErrInvalidCallbackURL) {
					t.Fatalf("want ErrInvalidCallbackURL, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil && r.CallbackURL != nil {
				t.Errorf("CallbackURL = %q, want nil", *r.CallbackURL)
			}
		})
	}
}

func TestRefundedIsTerminalAndLeavesNoExit(t *testing.T) {
	if !IsTerminalStatus(StatusRefunded) {
		t.Fatal("refunded must be terminal")
	}
	for _, to := range []string{StatusPending, StatusPaid, StatusExpired, StatusFailed, StatusCancelled, StatusRefunded} {
		if CanTransitionTo(StatusRefunded, to) {
			t.Errorf("refunded -> %s must not be allowed", to)
		}
	}
	for _, from := range []string{StatusPending, StatusExpired, StatusFailed, StatusCancelled, StatusRefunded} {
		if CanTransitionTo(from, StatusRefunded) {
			t.Errorf("%s -> refunded must not be allowed", from)
		}
	}
}
