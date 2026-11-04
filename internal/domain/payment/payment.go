package payment

import (
	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	StatusPending   = "pending"
	StatusPaid      = "paid"
	StatusExpired   = "expired"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

const (
	PaymentMethodQRIS = "qris"
)

const (
	CurrencyIDR = "IDR"
)

const (
	EnvironmentSandbox    = "sandbox"
	EnvironmentProduction = "production"
)

type Payment struct {
	ID                uuid.UUID `json:"id" db:"id"`
	Reference         string    `json:"reference" db:"reference"`
	MerchantID        uuid.UUID `json:"merchant_id" db:"merchant_id"`
	Amount            int64     `json:"amount" db:"amount"`
	Currency          string    `json:"currency" db:"currency"`
	PaymentMethod     string    `json:"payment_method" db:"payment_method"`
	ProviderName      string    `json:"provider_name" db:"provider_name"`
	ProviderReference *string   `json:"provider_reference,omitempty" db:"provider_reference"`
	Status            string    `json:"status" db:"status"`
	Description       string    `json:"description" db:"description"`
	CustomerName      *string   `json:"customer_name,omitempty" db:"customer_name"`
	CustomerEmail     *string   `json:"customer_email,omitempty" db:"customer_email"`
	QRISData          *string   `json:"qris_data,omitempty" db:"qris_data"`
	CallbackURL       *string   `json:"callback_url,omitempty" db:"callback_url"`
	Environment       string    `json:"environment" db:"environment"`
	// PaymentLinkID is set when this payment was spawned by a customer
	// checking out through a Payment Link (see internal/domain/paymentlink).
	PaymentLinkID *uuid.UUID `json:"payment_link_id,omitempty" db:"payment_link_id"`
	ExpiresAt     time.Time  `json:"expires_at" db:"expires_at"`
	PaidAt        *time.Time `json:"paid_at,omitempty" db:"paid_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

// ReconcileCursor is a keyset position in the reconcile candidate order
// (created_at DESC, id DESC): the next batch starts strictly after it.
type ReconcileCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type CreatePaymentRequest struct {
	MerchantID       uuid.UUID `json:"merchant_id"`
	Amount           int64     `json:"amount"`
	Currency         string    `json:"currency"`
	PaymentMethod    string    `json:"payment_method"`
	Description      string    `json:"description"`
	CustomerName     *string   `json:"customer_name,omitempty"`
	CustomerEmail    *string   `json:"customer_email,omitempty"`
	CallbackURL      *string   `json:"callback_url,omitempty"`
	ExpiresInMinutes int       `json:"expires_in_minutes,omitempty"`
	// Environment: sandbox | production. Forced from API key when present.
	Environment string `json:"environment,omitempty"`
	// UseCustomMerchantName requests a custom merchant display name on the
	// QR/payment page, where the selected provider supports it (e.g.
	// Cashi's QRIS Custom — docs/cashi-qris-custom.md). Ignored by
	// providers/environments that don't support it (sandbox included).
	UseCustomMerchantName bool `json:"use_custom_merchant_name,omitempty"`
	// PaymentLinkID traces this payment back to the Payment Link that
	// spawned it. Deliberately excluded from JSON (json:"-") — only
	// PaymentLinkService may set this programmatically; a client can never
	// forge link attribution via the request body.
	PaymentLinkID *uuid.UUID `json:"-"`
}

// NormalizeEnvironment maps free-form values to sandbox|production.
func NormalizeEnvironment(raw string) string {
	switch raw {
	case EnvironmentSandbox, "test", "dev", "development":
		return EnvironmentSandbox
	case EnvironmentProduction, "prod", "live", "":
		return EnvironmentProduction
	default:
		// Accept already-normalized or fall back
		if raw == EnvironmentSandbox {
			return EnvironmentSandbox
		}
		return EnvironmentProduction
	}
}

// ParseEnvironment resolves the environment for anything being created
// (payments, payment links). An empty value means sandbox: a missing or
// mistyped environment must never silently charge real money through the
// production provider. Unknown values are rejected rather than guessed.
// NormalizeEnvironment stays for read/filter paths, whose existing
// "empty = production" behavior dashboards rely on.
func ParseEnvironment(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", EnvironmentSandbox, "test", "dev", "development":
		return EnvironmentSandbox, nil
	case EnvironmentProduction, "prod", "live":
		return EnvironmentProduction, nil
	default:
		return "", ErrInvalidEnvironment
	}
}

func (r *CreatePaymentRequest) Validate() error {
	if r.Amount <= 0 {
		return ErrInvalidAmount
	}

	if r.Currency == "" {
		r.Currency = CurrencyIDR
	}

	if r.Currency != CurrencyIDR {
		return ErrUnsupportedCurrency
	}

	if r.PaymentMethod == "" {
		return ErrPaymentMethodRequired
	}

	if r.PaymentMethod != PaymentMethodQRIS {
		return ErrUnsupportedPaymentMethod
	}

	if r.MerchantID == uuid.Nil {
		return ErrMerchantIDRequired
	}

	if r.Description == "" {
		return ErrDescriptionRequired
	}

	if r.ExpiresInMinutes <= 0 {
		r.ExpiresInMinutes = 30
	}

	if r.ExpiresInMinutes > 1440 {
		return ErrInvalidExpiration
	}

	env, err := ParseEnvironment(r.Environment)
	if err != nil {
		return err
	}
	r.Environment = env

	if r.CallbackURL != nil {
		trimmed := strings.TrimSpace(*r.CallbackURL)
		if trimmed == "" {
			r.CallbackURL = nil
		} else {
			// Sandbox payments may use plain http (tunnels, test servers);
			// the IP rules still apply to them — our server can't reach a
			// merchant's localhost, and allowing it would be SSRF.
			if err := netsafe.ValidateCallbackURL(trimmed, r.Environment == EnvironmentSandbox); err != nil {
				return err
			}
			r.CallbackURL = &trimmed
		}
	}

	return nil
}

type PaymentResponse struct {
	ID                uuid.UUID  `json:"id"`
	Reference         string     `json:"reference"`
	MerchantID        uuid.UUID  `json:"merchant_id"`
	Amount            int64      `json:"amount"`
	Currency          string     `json:"currency"`
	PaymentMethod     string     `json:"payment_method"`
	ProviderName      string     `json:"provider_name"`
	ProviderReference *string    `json:"provider_reference,omitempty"`
	Status            string     `json:"status"`
	Description       string     `json:"description"`
	CustomerName      *string    `json:"customer_name,omitempty"`
	CustomerEmail     *string    `json:"customer_email,omitempty"`
	QRISData          *string    `json:"qris_data,omitempty"`
	CheckoutURL       string     `json:"checkout_url"`
	Environment       string     `json:"environment"`
	PaymentLinkID     *uuid.UUID `json:"payment_link_id,omitempty"`
	ExpiresAt         time.Time  `json:"expires_at"`
	PaidAt            *time.Time `json:"paid_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func ToPaymentResponse(p *Payment, baseURL string) *PaymentResponse {
	env := p.Environment
	if env == "" {
		env = EnvironmentProduction
	}
	return &PaymentResponse{
		ID:                p.ID,
		Reference:         p.Reference,
		MerchantID:        p.MerchantID,
		Amount:            p.Amount,
		Currency:          p.Currency,
		PaymentMethod:     p.PaymentMethod,
		ProviderName:      p.ProviderName,
		ProviderReference: p.ProviderReference,
		Status:            p.Status,
		Description:       p.Description,
		CustomerName:      p.CustomerName,
		CustomerEmail:     p.CustomerEmail,
		QRISData:          p.QRISData,
		CheckoutURL:       baseURL + "/pay/" + p.Reference,
		Environment:       env,
		PaymentLinkID:     p.PaymentLinkID,
		ExpiresAt:         p.ExpiresAt,
		PaidAt:            p.PaidAt,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
	}
}

type PaymentStatusResponse struct {
	ID        uuid.UUID  `json:"id"`
	Reference string     `json:"reference"`
	Status    string     `json:"status"`
	Amount    int64      `json:"amount"`
	Currency  string     `json:"currency"`
	PaidAt    *time.Time `json:"paid_at,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
}

func ToPaymentStatusResponse(p *Payment) *PaymentStatusResponse {
	return &PaymentStatusResponse{
		ID:        p.ID,
		Reference: p.Reference,
		Status:    p.Status,
		Amount:    p.Amount,
		Currency:  p.Currency,
		PaidAt:    p.PaidAt,
		ExpiresAt: p.ExpiresAt,
	}
}

func IsTerminalStatus(status string) bool {
	return status == StatusPaid || status == StatusExpired || status == StatusFailed || status == StatusCancelled
}

func IsValidStatus(status string) bool {
	validStatuses := []string{StatusPending, StatusPaid, StatusExpired, StatusFailed, StatusCancelled}
	for _, s := range validStatuses {
		if s == status {
			return true
		}
	}
	return false
}

// AllowedSourceStatuses lists the statuses a payment may move to newStatus
// from. pending can go to any outcome; the only way out of an outcome is
// expired → paid, for a settlement the provider reports after the payment
// already expired locally — the customer really paid. Only
// provider-reported settlements (a verified webhook or reconcile) ever write
// paid; there is no manual "mark paid" path, so this cannot revive an
// abandoned payment on its own.
func AllowedSourceStatuses(newStatus string) []string {
	switch newStatus {
	case StatusPaid:
		return []string{StatusPending, StatusExpired}
	case StatusExpired, StatusFailed, StatusCancelled:
		return []string{StatusPending}
	default:
		return nil
	}
}

func CanTransitionTo(currentStatus, newStatus string) bool {
	for _, s := range AllowedSourceStatuses(newStatus) {
		if s == currentStatus {
			return true
		}
	}
	return false
}
