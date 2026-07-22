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
}

type CreateOrderResponse struct {
	Success     bool   `json:"success"`
	OrderID     string `json:"order_id"`
	Amount      int64  `json:"amount"`
	CheckoutURL string `json:"checkout_url"`
	QRUrl       string `json:"qrUrl"`
	ExpiresAt   string `json:"expires_at"`
	Message     string `json:"message,omitempty"`
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
