package klikqris

import "time"

const (
	StatusSuccess = "SUCCESS"
	StatusPending = "PENDING"
	StatusExpired = "EXPIRED"
	StatusFailed  = "FAILED"
)

type CreateQRISRequest struct {
	MerchantID    string `json:"merchant_id"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	ReferenceNo   string `json:"reference_no"`
	Description   string `json:"description"`
	CustomerName  string `json:"customer_name,omitempty"`
	CustomerEmail string `json:"customer_email,omitempty"`
	CallbackURL   string `json:"callback_url"`
	ExpiredAt     string `json:"expired_at"`
}

type CreateQRISResponse struct {
	Status  string               `json:"status"`
	Message string               `json:"message"`
	Data    *QRISPaymentData     `json:"data,omitempty"`
	Error   *KlikQrisErrorDetail `json:"error,omitempty"`
}

type QRISPaymentData struct {
	TransactionID string    `json:"transaction_id"`
	ReferenceNo   string    `json:"reference_no"`
	QRISString    string    `json:"qris_string"`
	QRISImageURL  string    `json:"qris_image_url"`
	Amount        int64     `json:"amount"`
	Status        string    `json:"status"`
	ExpiredAt     time.Time `json:"expired_at"`
	CreatedAt     time.Time `json:"created_at"`
}

type CheckStatusRequest struct {
	MerchantID    string `json:"merchant_id"`
	TransactionID string `json:"transaction_id"`
}

type CheckStatusResponse struct {
	Status  string                 `json:"status"`
	Message string                 `json:"message"`
	Data    *StatusData            `json:"data,omitempty"`
	Error   *KlikQrisErrorDetail   `json:"error,omitempty"`
}

type StatusData struct {
	TransactionID string     `json:"transaction_id"`
	ReferenceNo   string     `json:"reference_no"`
	Amount        int64      `json:"amount"`
	Status        string     `json:"status"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	ExpiredAt     time.Time  `json:"expired_at"`
}

type WebhookPayload struct {
	TransactionID string    `json:"transaction_id"`
	ReferenceNo   string    `json:"reference_no"`
	MerchantID    string    `json:"merchant_id"`
	Amount        int64     `json:"amount"`
	Status        string    `json:"status"`
	PaidAt        time.Time `json:"paid_at"`
	Signature     string    `json:"signature"`
}

type KlikQrisErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type KlikQrisError struct {
	StatusCode int
	Message    string
	Detail     *KlikQrisErrorDetail
}

func (e *KlikQrisError) Error() string {
	if e.Detail != nil {
		return e.Detail.Message
	}
	return e.Message
}
