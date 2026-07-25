package paymentlink

import (
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

const (
	AmountTypeFixed = "fixed"
	AmountTypeOpen  = "open"
)

// Platform-wide amount bounds for open-amount links, mirroring Cashi's
// documented QRIS limits (docs/cashi-api.md: "Min. 2.000, Max. 10.000.000").
// Per-link MinAmount/MaxAmount may only narrow this range, never widen it —
// see EffectiveBounds.
const (
	PlatformMinAmount int64 = 2000
	PlatformMaxAmount int64 = 10000000
)

// PaymentLink is a reusable, multi-use payment template. It is never a
// payment itself — checking out through it spawns a new one-time
// payment.Payment (with its own freshly-generated QRIS) each time. See
// PaymentLinkService.InitiateCheckout, the only place that happens.
type PaymentLink struct {
	ID          uuid.UUID
	MerchantID  uuid.UUID
	Slug        string
	Title       string
	Description string
	AmountType  string
	Amount      *int64 // nil when AmountType == open
	Currency    string
	MinAmount   *int64
	MaxAmount   *int64
	IsActive    bool
	ExpiresAt   *time.Time
	Environment string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IsAvailable reports whether a customer can currently check out through
// this link, and why not if not ("inactive" | "expired").
func (l *PaymentLink) IsAvailable(now time.Time) (bool, string) {
	if !l.IsActive {
		return false, "inactive"
	}
	if l.ExpiresAt != nil && now.After(*l.ExpiresAt) {
		return false, "expired"
	}
	return true, ""
}

// EffectiveBounds returns the min/max this link enforces for open-amount
// checkout, clamped to the platform-wide bounds. A per-link bound can only
// narrow the range, never widen it — this is what keeps a merchant from
// configuring an open link outside what the provider actually supports.
func (l *PaymentLink) EffectiveBounds() (min, max int64) {
	min, max = PlatformMinAmount, PlatformMaxAmount
	if l.MinAmount != nil && *l.MinAmount > min {
		min = *l.MinAmount
	}
	if l.MaxAmount != nil && *l.MaxAmount < max {
		max = *l.MaxAmount
	}
	return min, max
}

type CreatePaymentLinkRequest struct {
	MerchantID  uuid.UUID  `json:"merchant_id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	AmountType  string     `json:"amount_type"`
	Amount      *int64     `json:"amount,omitempty"`
	Currency    string     `json:"currency,omitempty"`
	MinAmount   *int64     `json:"min_amount,omitempty"`
	MaxAmount   *int64     `json:"max_amount,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Environment string     `json:"environment,omitempty"`
}

func (r *CreatePaymentLinkRequest) Validate() error {
	if r.Title == "" {
		return ErrTitleRequired
	}

	if r.Currency == "" {
		r.Currency = payment.CurrencyIDR
	}
	if r.Currency != payment.CurrencyIDR {
		return payment.ErrUnsupportedCurrency
	}

	switch r.AmountType {
	case AmountTypeFixed:
		if r.Amount == nil || *r.Amount <= 0 {
			return ErrFixedAmountRequired
		}
		if *r.Amount < PlatformMinAmount || *r.Amount > PlatformMaxAmount {
			return ErrAmountOutOfPlatformBounds
		}
	case AmountTypeOpen:
		if r.Amount != nil {
			return ErrAmountNotAllowedForOpenLink
		}
	default:
		return ErrInvalidAmountType
	}

	if r.MinAmount != nil && (*r.MinAmount < PlatformMinAmount || *r.MinAmount > PlatformMaxAmount) {
		return ErrInvalidAmountBounds
	}
	if r.MaxAmount != nil && (*r.MaxAmount < PlatformMinAmount || *r.MaxAmount > PlatformMaxAmount) {
		return ErrInvalidAmountBounds
	}
	if r.MinAmount != nil && r.MaxAmount != nil && *r.MinAmount > *r.MaxAmount {
		return ErrInvalidAmountBounds
	}

	if r.MerchantID == uuid.Nil {
		return ErrMerchantIDRequired
	}

	r.Environment = payment.NormalizeEnvironment(r.Environment)

	return nil
}

// UpdatePaymentLinkRequest covers the only fields that remain editable after
// creation. amount_type/amount are immutable — changing what a
// bookmarked/shared link means after the fact would be surprising;
// deactivating and creating a new link is the correct flow for that.
type UpdatePaymentLinkRequest struct {
	Title       *string    `json:"title,omitempty"`
	Description *string    `json:"description,omitempty"`
	IsActive    *bool      `json:"is_active,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	MinAmount   *int64     `json:"min_amount,omitempty"`
	MaxAmount   *int64     `json:"max_amount,omitempty"`
}

// InitiateCheckoutRequest is what the public pay-via-link endpoint accepts.
// Amount is only honored when the link is open-amount; for fixed links it
// is ignored server-side — never trust a client-supplied amount.
type InitiateCheckoutRequest struct {
	Amount        int64   `json:"amount,omitempty"`
	CustomerName  *string `json:"customer_name,omitempty"`
	CustomerEmail *string `json:"customer_email,omitempty"`
}

type PaymentLinkResponse struct {
	ID          uuid.UUID  `json:"id"`
	MerchantID  uuid.UUID  `json:"merchant_id"`
	Slug        string     `json:"slug"`
	PublicURL   string     `json:"public_url"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	AmountType  string     `json:"amount_type"`
	Amount      *int64     `json:"amount,omitempty"`
	Currency    string     `json:"currency"`
	MinAmount   *int64     `json:"min_amount,omitempty"`
	MaxAmount   *int64     `json:"max_amount,omitempty"`
	IsActive    bool       `json:"is_active"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Environment string     `json:"environment"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func ToPaymentLinkResponse(l *PaymentLink, baseURL string) *PaymentLinkResponse {
	return &PaymentLinkResponse{
		ID:          l.ID,
		MerchantID:  l.MerchantID,
		Slug:        l.Slug,
		PublicURL:   baseURL + "/l/" + l.Slug,
		Title:       l.Title,
		Description: l.Description,
		AmountType:  l.AmountType,
		Amount:      l.Amount,
		Currency:    l.Currency,
		MinAmount:   l.MinAmount,
		MaxAmount:   l.MaxAmount,
		IsActive:    l.IsActive,
		ExpiresAt:   l.ExpiresAt,
		Environment: l.Environment,
		CreatedAt:   l.CreatedAt,
		UpdatedAt:   l.UpdatedAt,
	}
}

// PublicPaymentLinkResponse is the safe subset for the unauthenticated pay
// page — no merchant_id or other internal identifiers.
type PublicPaymentLinkResponse struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	AmountType  string `json:"amount_type"`
	Amount      *int64 `json:"amount,omitempty"`
	Currency    string `json:"currency"`
	MinAmount   int64  `json:"min_amount,omitempty"`
	MaxAmount   int64  `json:"max_amount,omitempty"`
	IsAvailable bool   `json:"is_available"`
	Reason      string `json:"reason,omitempty"`
	Environment string `json:"environment"`
}

func ToPublicPaymentLinkResponse(l *PaymentLink, now time.Time) *PublicPaymentLinkResponse {
	available, reason := l.IsAvailable(now)
	resp := &PublicPaymentLinkResponse{
		Slug:        l.Slug,
		Title:       l.Title,
		Description: l.Description,
		AmountType:  l.AmountType,
		Amount:      l.Amount,
		Currency:    l.Currency,
		IsAvailable: available,
		Reason:      reason,
		Environment: l.Environment,
	}
	if l.AmountType == AmountTypeOpen {
		resp.MinAmount, resp.MaxAmount = l.EffectiveBounds()
	}
	return resp
}
