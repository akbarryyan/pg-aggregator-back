package cashi

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
	Amount  int64  `json:"amount"`
	OrderID string `json:"order_id"`
	Message string `json:"message,omitempty"`
}

type WebhookPayload struct {
	Event string           `json:"event"`
	Data  WebhookEventData `json:"data"`
}

type WebhookEventData struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
	Amount  *int64 `json:"amount,omitempty"`
}

type CashiError struct {
	StatusCode int
	Message    string
}

func (e *CashiError) Error() string {
	return e.Message
}
