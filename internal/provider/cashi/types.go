package cashi

import providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"

const (
	StatusSettled = "SETTLED"
)

const (
	EventPaymentSettled = "PAYMENT_SETTLED"
)

type CreateOrderRequest struct {
	Amount  int64  `json:"amount"`
	OrderID string `json:"order_id,omitempty"`
	// QRISCustom requests QRIS Custom mode (merchant display name override).
	// Only takes effect if the Cashi account has the feature enabled on
	// their end — see docs/cashi-qris-custom.md.
	QRISCustom bool `json:"QRIS_CUSTOM,omitempty"`
}

type CreateOrderResponse struct {
	Success bool `json:"success"`
	// OrderID is the documented field name (order_id). OrderIDCamel is a
	// defensive fallback: Cashi's own QRIS Custom example response uses
	// "orderId" instead — see docs/cashi-qris-custom.md for the
	// discrepancy. Use GetOrderID() rather than these fields directly.
	OrderID      string `json:"order_id"`
	OrderIDCamel string `json:"orderId,omitempty"`
	Amount       int64  `json:"amount"`
	CheckoutURL  string `json:"checkout_url"`
	QRUrl        string `json:"qrUrl"`
	ExpiresAt    string `json:"expires_at"`
	Message      string `json:"message,omitempty"`
	// ExpectedNet and IsQRISCustom are only present on QRIS Custom
	// responses (order_id.QRIS_CUSTOM: true in the request).
	ExpectedNet  int64 `json:"expected_net,omitempty"`
	IsQRISCustom bool  `json:"is_qris_custom,omitempty"`
}

// GetOrderID returns OrderID, falling back to OrderIDCamel — see the field
// comments on CreateOrderResponse for why both exist.
func (r CreateOrderResponse) GetOrderID() string {
	if r.OrderID != "" {
		return r.OrderID
	}
	return r.OrderIDCamel
}

type CheckStatusResponse struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
	// Amount is a pointer on purpose: for a settled order Cashi currently
	// answers {"success":true,"status":"SETTLED","provider_tx_id":null,
	// "is_final":true} WITHOUT an amount (or order_id). Absent must stay
	// distinguishable from 0 so the settlement check does not read a missing
	// amount as "Rp 0".
	Amount  *int64 `json:"amount,omitempty"`
	OrderID string `json:"order_id"`
	Message string `json:"message,omitempty"`
}

type WebhookPayload struct {
	Event string           `json:"event"`
	Data  WebhookEventData `json:"data"`
}

type WebhookEventData struct {
	OrderID   string `json:"order_id"`
	Status    string `json:"status"`
	Amount    *int64 `json:"amount,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type CashiError struct {
	StatusCode int
	Message    string
}

func (e *CashiError) Error() string {
	return e.Message
}

// Unwrap lets callers classify every Cashi error response (HTTP 4xx/5xx,
// success:false, missing order id) with errors.Is(err, ErrProviderAPIError):
// the provider answered, but with an error. Cashi's raw response body stays in
// the message for the logs and is never shown to API clients.
func (e *CashiError) Unwrap() error { return providerPkg.ErrProviderAPIError }
