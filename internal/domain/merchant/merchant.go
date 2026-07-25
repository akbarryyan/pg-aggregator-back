package merchant

import (
	"time"

	"github.com/google/uuid"
)

type Merchant struct {
	ID           uuid.UUID `json:"id" db:"id"`
	Name         string    `json:"name" db:"name"`
	Email        string    `json:"email" db:"email"`
	Phone        string    `json:"phone" db:"phone"`
	BusinessName string    `json:"business_name" db:"business_name"`
	WebhookURL   *string   `json:"webhook_url,omitempty" db:"webhook_url"`
	// WebhookSecret signs outbound payment webhooks (X-PG-Signature). Never
	// serialized as part of the general merchant representation — only the
	// dedicated GET/regenerate webhook-secret endpoints expose it.
	WebhookSecret *string   `json:"-" db:"webhook_secret"`
	IsActive      bool      `json:"is_active" db:"is_active"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" db:"updated_at"`
}

type CreateMerchantRequest struct {
	Name         string  `json:"name"`
	Email        string  `json:"email"`
	Phone        string  `json:"phone"`
	BusinessName string  `json:"business_name"`
	WebhookURL   *string `json:"webhook_url,omitempty"`
}

func (r *CreateMerchantRequest) Validate() error {
	if r.Name == "" {
		return ErrMerchantNameRequired
	}
	if r.Email == "" {
		return ErrMerchantEmailRequired
	}
	if r.BusinessName == "" {
		return ErrBusinessNameRequired
	}
	return nil
}

type UpdateMerchantRequest struct {
	Name         *string `json:"name,omitempty"`
	Phone        *string `json:"phone,omitempty"`
	BusinessName *string `json:"business_name,omitempty"`
	WebhookURL   *string `json:"webhook_url,omitempty"`
}

type MerchantResponse struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	Phone        string    `json:"phone"`
	BusinessName string    `json:"business_name"`
	WebhookURL   *string   `json:"webhook_url,omitempty"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func ToMerchantResponse(m *Merchant) *MerchantResponse {
	return &MerchantResponse{
		ID:           m.ID,
		Name:         m.Name,
		Email:        m.Email,
		Phone:        m.Phone,
		BusinessName: m.BusinessName,
		WebhookURL:   m.WebhookURL,
		IsActive:     m.IsActive,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}
