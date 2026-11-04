// Package idempotency holds the types behind the Idempotency-Key header on
// payment creation. It deliberately imports nothing from the payment package
// (the service builds RequestFields) to avoid an import cycle.
package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// TTL is how long a key is remembered; older claims are treated as absent.
	TTL = 24 * time.Hour
	// AbandonedAfter is when a claim that never got a payment (process died, or
	// the request is still stuck) may be claimed again. It must exceed the
	// provider client timeout (30s) comfortably.
	AbandonedAfter = 2 * time.Minute

	maxKeyLen = 128
)

var (
	ErrKeyInvalid = errors.New("Idempotency-Key must be 1-128 visible ASCII characters")
	ErrKeyReused  = errors.New("Idempotency-Key was already used with a different request")
	ErrInProgress = errors.New("a request with this Idempotency-Key is still being processed; retry shortly")
	// ErrClaimLost means Complete found no claim with that id: it was taken
	// over (or purged) while its owner was still working.
	ErrClaimLost = errors.New("idempotency claim no longer exists; it was taken over by another request")
)

type Record struct {
	ID          uuid.UUID
	MerchantID  uuid.UUID
	Environment string
	Key         string
	RequestHash string
	PaymentID   *uuid.UUID
	CreatedAt   time.Time
}

// ValidateKey accepts 1-128 printable, non-space ASCII bytes (0x21-0x7E).
func ValidateKey(key string) error {
	if len(key) == 0 || len(key) > maxKeyLen {
		return ErrKeyInvalid
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return ErrKeyInvalid
		}
	}
	return nil
}

// RequestFields are the request values that define "the same payment". The
// struct (not a map) keeps JSON field order, and therefore the hash, stable.
type RequestFields struct {
	Amount                int64   `json:"amount"`
	Currency              string  `json:"currency"`
	PaymentMethod         string  `json:"payment_method"`
	Description           string  `json:"description"`
	CustomerName          *string `json:"customer_name"`
	CustomerEmail         *string `json:"customer_email"`
	CallbackURL           *string `json:"callback_url"`
	ExpiresInMinutes      int     `json:"expires_in_minutes"`
	UseCustomMerchantName bool    `json:"use_custom_merchant_name"`
}

func RequestHash(f RequestFields) string {
	b, _ := json.Marshal(f) // a struct of plain values cannot fail to marshal
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
