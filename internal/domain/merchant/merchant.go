package merchant

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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
	WebhookSecret *string `json:"-" db:"webhook_secret"`
	IsActive      bool    `json:"is_active" db:"is_active"`
	// FeeBearer decides who pays the provider fee: see FeeBearerMerchant /
	// FeeBearerCustomer. Admin-only; never settable via UpdateMerchantRequest.
	FeeBearer string    `json:"fee_bearer" db:"fee_bearer"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

type CreateMerchantRequest struct {
	Name         string  `json:"name"`
	Email        string  `json:"email"`
	Phone        string  `json:"phone"`
	BusinessName string  `json:"business_name"`
	WebhookURL   *string `json:"webhook_url,omitempty"`
	// Password is only used by the admin create flow (AdminMerchantService
	// enforces it); self-registration leaves it empty. Never returned.
	Password string `json:"password,omitempty"`
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
	FeeBearer    string    `json:"fee_bearer"`
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
		FeeBearer:    m.FeeBearer,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

// PayoutAccount is the bank / e-wallet a merchant's payouts are sent to.
type PayoutAccount struct {
	BankName      string `json:"bank_name"`
	AccountNumber string `json:"account_number"`
	AccountHolder string `json:"account_holder"`
}

var payoutAccountNumberRe = regexp.MustCompile(`^[0-9]{5,30}$`)

// Normalize trims all fields and validates them, returning a fixed-text
// error that is safe to show to the client.
func (a *PayoutAccount) Normalize() error {
	a.BankName = strings.TrimSpace(a.BankName)
	a.AccountNumber = strings.TrimSpace(a.AccountNumber)
	a.AccountHolder = strings.TrimSpace(a.AccountHolder)
	if n := utf8.RuneCountInString(a.BankName); n < 2 || n > 100 {
		return ErrPayoutBankInvalid
	}
	if !payoutAccountNumberRe.MatchString(a.AccountNumber) {
		return ErrPayoutAccountNumberInvalid
	}
	if n := utf8.RuneCountInString(a.AccountHolder); n < 2 || n > 150 {
		return ErrPayoutHolderInvalid
	}
	return nil
}
