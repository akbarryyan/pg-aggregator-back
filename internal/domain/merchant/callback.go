package merchant

import (
	"time"

	"github.com/google/uuid"
)

const (
	CallbackStatusPending = "pending"
	CallbackStatusSuccess = "success"
	CallbackStatusFailed  = "failed"
	CallbackStatusSkipped = "skipped"
)

// CallbackDelivery is one outbound HTTP attempt to notify a merchant.
type CallbackDelivery struct {
	ID             uuid.UUID              `json:"id" db:"id"`
	PaymentID      uuid.UUID              `json:"payment_id" db:"payment_id"`
	MerchantID     uuid.UUID              `json:"merchant_id" db:"merchant_id"`
	EventType      string                 `json:"event_type" db:"event_type"`
	TargetURL      string                 `json:"target_url" db:"target_url"`
	RequestPayload map[string]interface{} `json:"request_payload" db:"request_payload"`
	AttemptNumber  int                    `json:"attempt_number" db:"attempt_number"`
	Status         string                 `json:"status" db:"status"`
	HTTPStatus     *int                   `json:"http_status,omitempty" db:"http_status"`
	ResponseBody   *string                `json:"response_body,omitempty" db:"response_body"`
	ErrorMessage   *string                `json:"error_message,omitempty" db:"error_message"`
	DeliveredAt    *time.Time             `json:"delivered_at,omitempty" db:"delivered_at"`
	NextRetryAt    *time.Time             `json:"next_retry_at,omitempty" db:"next_retry_at"`
	CreatedAt      time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at" db:"updated_at"`
}
