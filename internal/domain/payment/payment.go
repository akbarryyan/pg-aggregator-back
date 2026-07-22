package payment

import (
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
	ID                uuid.UUID  `json:"id" db:"id"`
	Reference         string     `json:"reference" db:"reference"`
	MerchantID        uuid.UUID  `json:"merchant_id" db:"merchant_id"`
	Amount            int64      `json:"amount" db:"amount"`
	Currency          string     `json:"currency" db:"currency"`
	PaymentMethod     string     `json:"payment_method" db:"payment_method"`
	ProviderName      string     `json:"provider_name" db:"provider_name"`
	ProviderReference *string    `json:"provider_reference,omitempty" db:"provider_reference"`
	Status            string     `json:"status" db:"status"`
	Description       string     `json:"description" db:"description"`
	CustomerName      *string    `json:"customer_name,omitempty" db:"customer_name"`
	CustomerEmail     *string    `json:"customer_email,omitempty" db:"customer_email"`
	QRISData          *string    `json:"qris_data,omitempty" db:"qris_data"`
	CallbackURL       *string    `json:"callback_url,omitempty" db:"callback_url"`
	Environment       string     `json:"environment" db:"environment"`
	ExpiresAt         time.Time  `json:"expires_at" db:"expires_at"`
	PaidAt            *time.Time `json:"paid_at,omitempty" db:"paid_at"`
	CreatedAt         time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at" db:"updated_at"`
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

	r.Environment = NormalizeEnvironment(r.Environment)

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

func CanTransitionTo(currentStatus, newStatus string) bool {
	if currentStatus == newStatus {
		return false
	}

	if IsTerminalStatus(currentStatus) {
		return false
	}

	if currentStatus == StatusPending {
		return newStatus == StatusPaid || newStatus == StatusExpired || newStatus == StatusFailed || newStatus == StatusCancelled
	}

	return false
}
