package provider

import (
	"time"

	"github.com/google/uuid"
)

const (
	ProviderKlikQris = "klikqris"
	ProviderMidtrans = "midtrans"
	ProviderXendit   = "xendit"
	ProviderDuitku   = "duitku"
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
}

type ProviderPaymentResponse struct {
	ProviderReference string
	ProviderName      string
	Status            string
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

type WebhookEvent struct {
	ID                uuid.UUID              `json:"id" db:"id"`
	PaymentID         uuid.UUID              `json:"payment_id" db:"payment_id"`
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
