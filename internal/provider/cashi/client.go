package cashi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

const cashiExpiresAtLayout = "2006-01-02 15:04:05"

type CashiAdapter struct {
	baseURL    string
	apiKey     string
	secretKey  string
	httpClient *http.Client
}

// NewCashiAdapter creates a Cashi QRIS adapter.
// Auth uses x-api-key (API key) and secret key for webhook HMAC only — no merchant_id.
func NewCashiAdapter(baseURL, apiKey, secretKey string) *CashiAdapter {
	return &CashiAdapter{
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiKey:    apiKey,
		secretKey: secretKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (a *CashiAdapter) GetName() string {
	return provider.ProviderCashi
}

func (a *CashiAdapter) CreatePayment(ctx context.Context, req *provider.ProviderPaymentRequest) (*provider.ProviderPaymentResponse, error) {
	cashiReq := &CreateOrderRequest{
		Amount:     req.Amount,
		OrderID:    req.InternalReference,
		QRISCustom: req.UseCustomMerchantName,
	}

	var cashiResp CreateOrderResponse
	if err := a.doRequest(ctx, http.MethodPost, "/api/create-order", cashiReq, &cashiResp); err != nil {
		return nil, fmt.Errorf("failed to create QRIS payment: %w", err)
	}

	if !cashiResp.Success {
		return nil, &CashiError{StatusCode: 400, Message: cashiResp.Message}
	}

	orderID := cashiResp.GetOrderID()

	expiresAt, err := time.Parse(cashiExpiresAtLayout, cashiResp.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse expires_at from cashi: %w", err)
	}

	rawResponse := map[string]interface{}{
		"order_id":     orderID,
		"amount":       cashiResp.Amount,
		"checkout_url": cashiResp.CheckoutURL,
		"qrUrl":        cashiResp.QRUrl,
		"expires_at":   cashiResp.ExpiresAt,
	}
	if req.UseCustomMerchantName {
		rawResponse["is_qris_custom"] = cashiResp.IsQRISCustom
		rawResponse["expected_net"] = cashiResp.ExpectedNet
	}

	checkoutURL := cashiResp.CheckoutURL
	qrURL := cashiResp.QRUrl

	return &provider.ProviderPaymentResponse{
		ProviderReference: orderID,
		ProviderName:      provider.ProviderCashi,
		Status:            "pending",
		Amount:            cashiResp.Amount,
		QRISData:          &qrURL,
		PaymentURL:        &checkoutURL,
		ExpiresAt:         expiresAt,
		RawResponse:       rawResponse,
	}, nil
}

func (a *CashiAdapter) GetPaymentStatus(ctx context.Context, providerReference string) (*provider.NormalizedPaymentStatus, error) {
	path := "/api/check-status/" + url.PathEscape(providerReference)

	var checkResp CheckStatusResponse
	if err := a.doRequest(ctx, http.MethodGet, path, nil, &checkResp); err != nil {
		return nil, fmt.Errorf("failed to check payment status: %w", err)
	}

	if !checkResp.Success {
		return nil, &CashiError{StatusCode: 400, Message: checkResp.Message}
	}

	return &provider.NormalizedPaymentStatus{
		Status:            a.NormalizeStatus(checkResp.Status),
		ProviderReference: checkResp.OrderID,
	}, nil
}

func (a *CashiAdapter) ValidateWebhook(rawPayload []byte, signature string) error {
	if signature == "" {
		return providerPkg.ErrInvalidWebhookSignature
	}

	expectedSignature := a.generateSignature(rawPayload)
	if !hmac.Equal([]byte(expectedSignature), []byte(signature)) {
		return providerPkg.ErrInvalidWebhookSignature
	}
	return nil
}

func (a *CashiAdapter) ParseWebhook(rawPayload []byte) (*provider.ProviderWebhookPayload, error) {
	var webhook WebhookPayload
	if err := json.Unmarshal(rawPayload, &webhook); err != nil {
		return nil, fmt.Errorf("failed to parse webhook payload: %w", err)
	}

	if strings.HasPrefix(webhook.Data.OrderID, "TEST-") {
		return nil, providerPkg.ErrTestWebhookEvent
	}

	rawMap := make(map[string]interface{})
	_ = json.Unmarshal(rawPayload, &rawMap)

	status := "pending"
	var paidAt *time.Time
	if webhook.Event == EventPaymentSettled && webhook.Data.Status == StatusSettled {
		status = "paid"
		now := time.Now()
		paidAt = &now
	}

	return &provider.ProviderWebhookPayload{
		ProviderName:      provider.ProviderCashi,
		ProviderReference: webhook.Data.OrderID,
		Status:            status,
		PaidAt:            paidAt,
		Amount:            webhook.Data.Amount,
		RawPayload:        rawMap,
	}, nil
}

func (a *CashiAdapter) NormalizeStatus(providerStatus string) string {
	if providerStatus == StatusSettled {
		return "paid"
	}
	return "pending"
}

func (a *CashiAdapter) doRequest(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	url := a.baseURL + path

	var bodyReader io.Reader
	if reqBody != nil {
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("failed to marshal request: %w", err)
		}
		bodyReader = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return &CashiError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
		}
	}

	if respBody != nil {
		if err := json.Unmarshal(body, respBody); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
	}

	return nil
}

func (a *CashiAdapter) generateSignature(payload []byte) string {
	h := hmac.New(sha256.New, []byte(a.secretKey))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
