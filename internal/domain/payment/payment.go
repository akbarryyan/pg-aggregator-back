package payment

import (
	"github.com/akbarryyan/pg-aggregator-back/internal/fee"
	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
	"net/url"
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
	StatusRefunded  = "refunded"
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
	ID         uuid.UUID `json:"id" db:"id"`
	Reference  string    `json:"reference" db:"reference"`
	MerchantID uuid.UUID `json:"merchant_id" db:"merchant_id"`
	Amount     int64     `json:"amount" db:"amount"`
	FeeAmount  int64     `json:"fee_amount" db:"fee_amount"`
	// MerchantNetAmount is what the merchant keeps once the payment is paid.
	// It is stored for every paid payment (sandbox included); only production
	// payments are credited to the ledger. 0 for payments that are not paid.
	MerchantNetAmount int64   `json:"merchant_net_amount" db:"merchant_net_amount"`
	Currency          string  `json:"currency" db:"currency"`
	PaymentMethod     string  `json:"payment_method" db:"payment_method"`
	ProviderName      string  `json:"provider_name" db:"provider_name"`
	ProviderReference *string `json:"provider_reference,omitempty" db:"provider_reference"`
	Status            string  `json:"status" db:"status"`
	Description       string  `json:"description" db:"description"`
	CustomerName      *string `json:"customer_name,omitempty" db:"customer_name"`
	CustomerEmail     *string `json:"customer_email,omitempty" db:"customer_email"`
	QRISData          *string `json:"qris_data,omitempty" db:"qris_data"`
	CallbackURL       *string `json:"callback_url,omitempty" db:"callback_url"`
	Environment       string  `json:"environment" db:"environment"`
	// PaymentLinkID is set when this payment was spawned by a customer
	// checking out through a Payment Link (see internal/domain/paymentlink).
	PaymentLinkID *uuid.UUID `json:"payment_link_id,omitempty" db:"payment_link_id"`
	ExpiresAt     time.Time  `json:"expires_at" db:"expires_at"`
	PaidAt        *time.Time `json:"paid_at,omitempty" db:"paid_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

// TotalAmount is what the customer pays: the merchant's nominal plus any
// provider fee added on top (zero unless the merchant has the customer bear it).
func (p *Payment) TotalAmount() int64 { return p.Amount + p.FeeAmount }

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
	// The upper bound keeps merchantNetFor (fee.MaxNominal) from failing at
	// settlement time for a payment the customer already paid.
	if r.Amount <= 0 || r.Amount > fee.MaxNominal {
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
	FeeAmount         int64      `json:"fee_amount"`
	TotalAmount       int64      `json:"total_amount"`
	Currency          string     `json:"currency"`
	PaymentMethod     string     `json:"payment_method"`
	ProviderName      string     `json:"provider_name"`
	ProviderReference *string    `json:"provider_reference,omitempty"`
	Status            string     `json:"status"`
	Description       string     `json:"description"`
	CustomerName      *string    `json:"customer_name,omitempty"`
	CustomerEmail     *string    `json:"customer_email,omitempty"`
	QRISData          *string    `json:"qris_data,omitempty"`
	QRImageURL        string     `json:"qr_image_url,omitempty"`
	CheckoutURL       string     `json:"checkout_url"`
	Environment       string     `json:"environment"`
	PaymentLinkID     *uuid.UUID `json:"payment_link_id,omitempty"`
	ExpiresAt         time.Time  `json:"expires_at"`
	PaidAt            *time.Time `json:"paid_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// SandboxQRPNGPrefix is how the sandbox adapter stores its QR image.
const SandboxQRPNGPrefix = "data:image/png;base64,"

// SandboxQRImageURL is the public URL of a sandbox payment's QR image (served
// by GET /public/payments/by-reference/{reference}/qr.png), or "" when the
// payment is not sandbox or has no stored PNG QR. apiBaseURL is app.url.
func SandboxQRImageURL(p *Payment, apiBaseURL string) string {
	if p == nil || p.Environment != EnvironmentSandbox || p.QRISData == nil ||
		!strings.HasPrefix(*p.QRISData, SandboxQRPNGPrefix) {
		return ""
	}
	return strings.TrimRight(apiBaseURL, "/") + "/api/v1/public/payments/by-reference/" +
		url.PathEscape(p.Reference) + "/qr.png"
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
		FeeAmount:         p.FeeAmount,
		TotalAmount:       p.TotalAmount(),
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
	ID          uuid.UUID  `json:"id"`
	Reference   string     `json:"reference"`
	Status      string     `json:"status"`
	Amount      int64      `json:"amount"`
	FeeAmount   int64      `json:"fee_amount"`
	TotalAmount int64      `json:"total_amount"`
	Currency    string     `json:"currency"`
	PaidAt      *time.Time `json:"paid_at,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
}

func ToPaymentStatusResponse(p *Payment) *PaymentStatusResponse {
	return &PaymentStatusResponse{
		ID:          p.ID,
		Reference:   p.Reference,
		Status:      p.Status,
		Amount:      p.Amount,
		FeeAmount:   p.FeeAmount,
		TotalAmount: p.TotalAmount(),
		Currency:    p.Currency,
		PaidAt:      p.PaidAt,
		ExpiresAt:   p.ExpiresAt,
	}
}

func IsTerminalStatus(status string) bool {
	return status == StatusPaid || status == StatusExpired || status == StatusFailed || status == StatusCancelled || status == StatusRefunded
}

func IsValidStatus(status string) bool {
	validStatuses := []string{StatusPending, StatusPaid, StatusExpired, StatusFailed, StatusCancelled, StatusRefunded}
	for _, s := range validStatuses {
		if s == status {
			return true
		}
	}
	return false
}

// AllowedSourceStatuses lists the statuses a payment may move to newStatus
// from. pending can go to any provider outcome (paid, expired, failed,
// cancelled). From an outcome there are only two ways on:
//   - expired → paid, for a settlement the provider reports after the payment
//     already expired locally — the customer really paid;
//   - paid → refunded, when an admin records a refund that returns the whole
//     merchant net (a partial refund leaves the payment paid). refunded is
//     terminal.
//
// Only provider-reported settlements (a verified webhook or reconcile) ever
// write paid; there is no manual "mark paid" path, so this cannot revive an
// abandoned payment on its own. The refunded transition is made by the refund
// repository inside its own transaction, not through UpdateStatus.
func AllowedSourceStatuses(newStatus string) []string {
	switch newStatus {
	case StatusPaid:
		return []string{StatusPending, StatusExpired}
	case StatusExpired, StatusFailed, StatusCancelled:
		return []string{StatusPending}
	case StatusRefunded:
		return []string{StatusPaid}
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
