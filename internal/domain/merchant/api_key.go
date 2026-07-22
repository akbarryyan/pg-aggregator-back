package merchant

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAPIKeyNotFound         = errors.New("api key not found")
	ErrAPIKeyInvalid          = errors.New("invalid api key")
	ErrAPIKeyRevoked          = errors.New("api key revoked")
	ErrAPIKeyMerchantInactive = errors.New("merchant is inactive")
	ErrAPIKeyPasswordRequired = errors.New("admin password is required")
)

// APIKey is the persisted key metadata (never includes full secret).
type APIKey struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	MerchantID uuid.UUID  `json:"merchant_id" db:"merchant_id"`
	Name       string     `json:"name" db:"name"`
	KeyPrefix  string     `json:"key_prefix" db:"key_prefix"`
	KeyHash    string     `json:"-" db:"key_hash"`
	IsActive   bool       `json:"is_active" db:"is_active"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty" db:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at" db:"updated_at"`
}

// APIKeyPublic is safe for admin list responses.
type APIKeyPublic struct {
	ID         uuid.UUID  `json:"id"`
	MerchantID uuid.UUID  `json:"merchant_id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	IsActive   bool       `json:"is_active"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func (k *APIKey) ToPublic() *APIKeyPublic {
	return &APIKeyPublic{
		ID:         k.ID,
		MerchantID: k.MerchantID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		IsActive:   k.IsActive,
		LastUsedAt: k.LastUsedAt,
		RevokedAt:  k.RevokedAt,
		CreatedAt:  k.CreatedAt,
		UpdatedAt:  k.UpdatedAt,
	}
}

// UpsertAPIKeyRequest rotates/creates the single key for an environment.
// Environment is sandbox | production (stored in Name).
type UpsertAPIKeyRequest struct {
	Environment string `json:"environment"` // sandbox | production
	Name        string `json:"name"`        // alias of environment (legacy)
	Password    string `json:"password"`    // admin password confirmation
}

// DeleteAPIKeyRequest requires admin password for destructive action.
type DeleteAPIKeyRequest struct {
	Password string `json:"password"`
}

// UpsertAPIKeyResponse includes the secret once after create/rotate.
type UpsertAPIKeyResponse struct {
	Key       *APIKeyPublic `json:"key"`
	Secret    string        `json:"secret"`
	Hint      string        `json:"hint"`
	Rotated   bool          `json:"rotated"` // true if previous key for env was replaced
	Environment string      `json:"environment"`
}
