package paymentlink

import (
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

func int64Ptr(v int64) *int64 { return &v }

func validFixedRequest() *CreatePaymentLinkRequest {
	return &CreatePaymentLinkRequest{
		MerchantID: uuid.New(),
		Title:      "Coffee",
		AmountType: AmountTypeFixed,
		Amount:     int64Ptr(15000),
	}
}

func validOpenRequest() *CreatePaymentLinkRequest {
	return &CreatePaymentLinkRequest{
		MerchantID: uuid.New(),
		Title:      "Donation",
		AmountType: AmountTypeOpen,
	}
}

func TestCreatePaymentLinkRequest_Validate(t *testing.T) {
	t.Run("valid fixed request passes", func(t *testing.T) {
		if err := validFixedRequest().Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("valid open request passes", func(t *testing.T) {
		if err := validOpenRequest().Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("title required", func(t *testing.T) {
		req := validFixedRequest()
		req.Title = ""
		if err := req.Validate(); err != ErrTitleRequired {
			t.Fatalf("expected ErrTitleRequired, got %v", err)
		}
	})

	t.Run("empty currency defaults to IDR", func(t *testing.T) {
		req := validFixedRequest()
		req.Currency = ""
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Currency != payment.CurrencyIDR {
			t.Errorf("expected currency to default to IDR, got %q", req.Currency)
		}
	})

	t.Run("non-IDR currency rejected", func(t *testing.T) {
		req := validFixedRequest()
		req.Currency = "USD"
		if err := req.Validate(); err != payment.ErrUnsupportedCurrency {
			t.Fatalf("expected payment.ErrUnsupportedCurrency, got %v", err)
		}
	})

	t.Run("invalid amount_type rejected", func(t *testing.T) {
		req := validFixedRequest()
		req.AmountType = "subscription"
		if err := req.Validate(); err != ErrInvalidAmountType {
			t.Fatalf("expected ErrInvalidAmountType, got %v", err)
		}
	})

	t.Run("fixed without amount rejected", func(t *testing.T) {
		req := validFixedRequest()
		req.Amount = nil
		if err := req.Validate(); err != ErrFixedAmountRequired {
			t.Fatalf("expected ErrFixedAmountRequired, got %v", err)
		}
	})

	t.Run("fixed with zero amount rejected", func(t *testing.T) {
		req := validFixedRequest()
		req.Amount = int64Ptr(0)
		if err := req.Validate(); err != ErrFixedAmountRequired {
			t.Fatalf("expected ErrFixedAmountRequired, got %v", err)
		}
	})

	t.Run("fixed amount below platform minimum rejected", func(t *testing.T) {
		req := validFixedRequest()
		req.Amount = int64Ptr(1999)
		if err := req.Validate(); err != ErrAmountOutOfPlatformBounds {
			t.Fatalf("expected ErrAmountOutOfPlatformBounds, got %v", err)
		}
	})

	t.Run("fixed amount above platform maximum rejected", func(t *testing.T) {
		req := validFixedRequest()
		req.Amount = int64Ptr(10000001)
		if err := req.Validate(); err != ErrAmountOutOfPlatformBounds {
			t.Fatalf("expected ErrAmountOutOfPlatformBounds, got %v", err)
		}
	})

	t.Run("open with client-supplied amount rejected", func(t *testing.T) {
		req := validOpenRequest()
		req.Amount = int64Ptr(5000)
		if err := req.Validate(); err != ErrAmountNotAllowedForOpenLink {
			t.Fatalf("expected ErrAmountNotAllowedForOpenLink, got %v", err)
		}
	})

	t.Run("min greater than max rejected", func(t *testing.T) {
		req := validOpenRequest()
		req.MinAmount = int64Ptr(50000)
		req.MaxAmount = int64Ptr(10000)
		if err := req.Validate(); err != ErrInvalidAmountBounds {
			t.Fatalf("expected ErrInvalidAmountBounds, got %v", err)
		}
	})

	t.Run("min below platform bounds rejected", func(t *testing.T) {
		req := validOpenRequest()
		req.MinAmount = int64Ptr(100)
		if err := req.Validate(); err != ErrInvalidAmountBounds {
			t.Fatalf("expected ErrInvalidAmountBounds, got %v", err)
		}
	})

	t.Run("max above platform bounds rejected", func(t *testing.T) {
		req := validOpenRequest()
		req.MaxAmount = int64Ptr(99999999)
		if err := req.Validate(); err != ErrInvalidAmountBounds {
			t.Fatalf("expected ErrInvalidAmountBounds, got %v", err)
		}
	})

	t.Run("min/max within bounds accepted", func(t *testing.T) {
		req := validOpenRequest()
		req.MinAmount = int64Ptr(5000)
		req.MaxAmount = int64Ptr(500000)
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("merchant id required", func(t *testing.T) {
		req := validFixedRequest()
		req.MerchantID = uuid.Nil
		if err := req.Validate(); err != ErrMerchantIDRequired {
			t.Fatalf("expected ErrMerchantIDRequired, got %v", err)
		}
	})

	t.Run("environment normalized as part of validation", func(t *testing.T) {
		req := validFixedRequest()
		req.Environment = "test"
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Environment != payment.EnvironmentSandbox {
			t.Errorf("expected environment normalized to sandbox, got %q", req.Environment)
		}
	})
}

func TestPaymentLink_IsAvailable(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("active with no expiry is available", func(t *testing.T) {
		l := &PaymentLink{IsActive: true}
		ok, reason := l.IsAvailable(now)
		if !ok || reason != "" {
			t.Errorf("expected available, got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("inactive is unavailable", func(t *testing.T) {
		l := &PaymentLink{IsActive: false}
		ok, reason := l.IsAvailable(now)
		if ok || reason != "inactive" {
			t.Errorf("expected unavailable/inactive, got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("active but past expiry is unavailable", func(t *testing.T) {
		past := now.Add(-1 * time.Hour)
		l := &PaymentLink{IsActive: true, ExpiresAt: &past}
		ok, reason := l.IsAvailable(now)
		if ok || reason != "expired" {
			t.Errorf("expected unavailable/expired, got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("active with future expiry is available", func(t *testing.T) {
		future := now.Add(1 * time.Hour)
		l := &PaymentLink{IsActive: true, ExpiresAt: &future}
		ok, reason := l.IsAvailable(now)
		if !ok || reason != "" {
			t.Errorf("expected available, got ok=%v reason=%q", ok, reason)
		}
	})
}

func TestPaymentLink_EffectiveBounds(t *testing.T) {
	t.Run("nil min/max fall back to platform defaults", func(t *testing.T) {
		l := &PaymentLink{}
		min, max := l.EffectiveBounds()
		if min != PlatformMinAmount || max != PlatformMaxAmount {
			t.Errorf("expected platform defaults (%d, %d), got (%d, %d)", PlatformMinAmount, PlatformMaxAmount, min, max)
		}
	})

	t.Run("narrower per-link bounds are respected", func(t *testing.T) {
		l := &PaymentLink{MinAmount: int64Ptr(10000), MaxAmount: int64Ptr(50000)}
		min, max := l.EffectiveBounds()
		if min != 10000 || max != 50000 {
			t.Errorf("expected (10000, 50000), got (%d, %d)", min, max)
		}
	})

	t.Run("a per-link bound wider than platform bounds is clamped back", func(t *testing.T) {
		// Security-critical: a merchant (or a bug elsewhere) setting
		// min/max outside platform limits must never widen what a
		// customer can actually be charged.
		l := &PaymentLink{MinAmount: int64Ptr(1), MaxAmount: int64Ptr(999999999)}
		min, max := l.EffectiveBounds()
		if min != PlatformMinAmount {
			t.Errorf("expected min clamped to platform minimum %d, got %d", PlatformMinAmount, min)
		}
		if max != PlatformMaxAmount {
			t.Errorf("expected max clamped to platform maximum %d, got %d", PlatformMaxAmount, max)
		}
	})
}

func TestToPublicPaymentLinkResponse(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("fixed link omits min/max bounds", func(t *testing.T) {
		l := &PaymentLink{IsActive: true, AmountType: AmountTypeFixed, Amount: int64Ptr(15000)}
		resp := ToPublicPaymentLinkResponse(l, now)
		if resp.MinAmount != 0 || resp.MaxAmount != 0 {
			t.Errorf("expected fixed link to omit bounds, got min=%d max=%d", resp.MinAmount, resp.MaxAmount)
		}
		if !resp.IsAvailable {
			t.Errorf("expected available")
		}
	})

	t.Run("open link includes effective bounds", func(t *testing.T) {
		l := &PaymentLink{IsActive: true, AmountType: AmountTypeOpen, MinAmount: int64Ptr(5000)}
		resp := ToPublicPaymentLinkResponse(l, now)
		if resp.MinAmount != 5000 || resp.MaxAmount != PlatformMaxAmount {
			t.Errorf("expected min=5000 max=%d, got min=%d max=%d", PlatformMaxAmount, resp.MinAmount, resp.MaxAmount)
		}
	})

	t.Run("inactive link reports reason", func(t *testing.T) {
		l := &PaymentLink{IsActive: false, AmountType: AmountTypeFixed, Amount: int64Ptr(15000)}
		resp := ToPublicPaymentLinkResponse(l, now)
		if resp.IsAvailable || resp.Reason != "inactive" {
			t.Errorf("expected unavailable/inactive, got available=%v reason=%q", resp.IsAvailable, resp.Reason)
		}
	})
}
