package provider

import (
	"time"

	"github.com/google/uuid"
)

const (
	ProviderCashi    = "cashi"
	ProviderMidtrans = "midtrans"
	ProviderXendit   = "xendit"
	ProviderDuitku   = "duitku"
)

const (
	HealthStatusHealthy   = "healthy"
	HealthStatusDegraded  = "degraded"
	HealthStatusUnhealthy = "unhealthy"
)

type ProviderPaymentRequest struct {
	InternalReference string
	Amount            int64
	Currency          string
	Description       string
	CustomerName      *string
	CustomerEmail     *string
	ExpiresAt         time.Time
	CallbackURL       string
	// UseCustomMerchantName requests that the provider display the
	// merchant's custom name on the payment QR/page instead of its
	// default account name, where supported (e.g. Cashi's QRIS Custom —
	// see docs/cashi-qris-custom.md). Providers that don't support this
	// silently ignore it.
	UseCustomMerchantName bool
}

type ProviderPaymentResponse struct {
	ProviderReference string
	ProviderName      string
	Status            string
	Amount            int64
	QRISData          *string
	PaymentURL        *string
	ExpiresAt         time.Time
	RawResponse       map[string]interface{}
}

type ProviderWebhookPayload struct {
	ProviderName      string
	ProviderReference string
	Status            string
	PaidAt            *time.Time
	Amount            *int64
	RawPayload        map[string]interface{}
}

type NormalizedPaymentStatus struct {
	Status            string
	ProviderReference string
	PaidAt            *time.Time
}

type ProviderConfig struct {
	Name      string
	BaseURL   string
	APIKey    string
	SecretKey string
}

type MerchantProviderConfig struct {
	ID              uuid.UUID `json:"id" db:"id"`
	MerchantID      uuid.UUID `json:"merchant_id" db:"merchant_id"`
	ProviderName    string    `json:"provider_name" db:"provider_name"`
	PaymentMethod   string    `json:"payment_method" db:"payment_method"`
	Priority        int       `json:"priority" db:"priority"`
	Weight          int       `json:"weight" db:"weight"`
	FailoverEnabled bool      `json:"failover_enabled" db:"failover_enabled"`
	IsEnabled       bool      `json:"is_enabled" db:"is_enabled"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time `json:"updated_at" db:"updated_at"`
}

type UpsertMerchantProviderConfigRequest struct {
	ProviderName    string `json:"provider_name"`
	PaymentMethod   string `json:"payment_method"`
	Priority        int    `json:"priority"`
	Weight          int    `json:"weight"`
	FailoverEnabled bool   `json:"failover_enabled"`
	IsEnabled       bool   `json:"is_enabled"`
}

func (r *UpsertMerchantProviderConfigRequest) Normalize() {
	if r.Priority <= 0 {
		r.Priority = 1
	}
	if r.Weight <= 0 {
		r.Weight = 100
	}
}

func (r *UpsertMerchantProviderConfigRequest) Validate() error {
	r.Normalize()
	if r.ProviderName == "" {
		return ErrProviderConfigProviderRequired
	}
	if r.PaymentMethod == "" {
		return ErrProviderConfigPaymentMethodRequired
	}
	return nil
}

type ProviderHealth struct {
	ProviderName string    `json:"provider_name"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type UpdateProviderHealthRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func (r *UpdateProviderHealthRequest) Validate() error {
	switch r.Status {
	case HealthStatusHealthy, HealthStatusDegraded, HealthStatusUnhealthy:
		return nil
	default:
		return ErrInvalidProviderHealthStatus
	}
}

type WebhookEvent struct {
	ID                uuid.UUID              `json:"id" db:"id"`
	PaymentID         *uuid.UUID             `json:"payment_id,omitempty" db:"payment_id"`
	ProviderName      string                 `json:"provider_name" db:"provider_name"`
	ProviderReference string                 `json:"provider_reference" db:"provider_reference"`
	EventType         string                 `json:"event_type" db:"event_type"`
	Status            string                 `json:"status" db:"status"`
	RawPayload        map[string]interface{} `json:"raw_payload" db:"raw_payload"`
	ProcessedAt       *time.Time             `json:"processed_at,omitempty" db:"processed_at"`
	IsProcessed       bool                   `json:"is_processed" db:"is_processed"`
	ProcessingError   *string                `json:"processing_error,omitempty" db:"processing_error"`
	CreatedAt         time.Time              `json:"created_at" db:"created_at"`
}

const (
	WebhookEventTypePaymentPaid    = "payment.paid"
	WebhookEventTypePaymentExpired = "payment.expired"
	WebhookEventTypePaymentFailed  = "payment.failed"
)
