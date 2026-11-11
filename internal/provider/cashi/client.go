package cashi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/fee"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

const cashiExpiresAtLayout = "2006-01-02 15:04:05"

// cashiTimeLocation: Cashi's expires_at string has no timezone marker
// (e.g. "2024-01-01 10:00:00"), but Cashi is an Indonesian provider and
// this represents WIB wall-clock, not UTC. Parsing it with plain
// time.Parse silently defaults to UTC and produces a timestamp ~7 hours
// in the future — the same class of bug as the payments.expires_at
// TIMESTAMPTZ migration, but at the point we read Cashi's own response
// instead of our own DB. A fixed +07:00 offset (not time.LoadLocation)
// avoids depending on the IANA tzdata being installed on the deployment
// host; WIB has no DST so a fixed offset is always correct.
var cashiTimeLocation = time.FixedZone("WIB", 7*60*60)

// requestTimeout bounds one call to Cashi. It stays below the API server's
// 15s WriteTimeout so a normal attempt finishes inside the merchant's response
// window; a slow Cashi turns into a quick 502 the merchant retries with the
// same Idempotency-Key.
const requestTimeout = 10 * time.Second

// feeBasisPoints is Cashi's percentage fee (0.7%), deducted from the gross
// amount at settlement (seen in production: Rp 2.000 -> Rp 1.986 net).
const feeBasisPoints = fee.ProviderBasisPoints

// QuoteTotal tells the total a customer must pay so the merchant still nets
// the nominal after Cashi's fee (see internal/fee for the rounding rule).
func (a *CashiAdapter) QuoteTotal(nominal int64) (total, feeAmount int64, err error) {
	return fee.QuoteTotalBasisPoints(nominal, feeBasisPoints)
}

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
			Timeout: requestTimeout,
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
	if orderID == "" {
		// Without an order id the payment could not be mapped from webhooks
		// and would collide on the unique provider_reference index.
		return nil, &CashiError{StatusCode: 502, Message: "cashi create-order response has no order id"}
	}

	expiresAt, err := time.ParseInLocation(cashiExpiresAtLayout, cashiResp.ExpiresAt, cashiTimeLocation)
	if err != nil {
		return nil, fmt.Errorf("failed to parse expires_at from cashi: %w: %w", providerPkg.ErrProviderAPIError, err)
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

	// checkResp.Amount stays nil when Cashi omits it; the caller decides how
	// to verify a settlement without a provider-stated amount.
	return &provider.NormalizedPaymentStatus{
		Status:            a.NormalizeStatus(checkResp.Status),
		ProviderReference: checkResp.OrderID,
		Amount:            checkResp.Amount,
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
		paidAt = settledAt(webhook.Data.Timestamp)
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

// settledAt prefers Cashi's own settlement timestamp (RFC3339, e.g.
// "2025-12-25T19:46:40Z") so paid_at reflects when the customer paid, not
// when the webhook happened to arrive; it falls back to receipt time.
func settledAt(ts string) *time.Time {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return &t
	}
	now := time.Now()
	return &now
}

func (a *CashiAdapter) NormalizeStatus(providerStatus string) string {
	if providerStatus == StatusSettled {
		return "paid"
	}
	return "pending"
}

// classifyTransportError maps a failed HTTP round trip to exactly one provider
// sentinel: a timeout (the provider did not answer in time) or "not available"
// (connection refused, DNS failure, cancelled context, ...).
func classifyTransportError(err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return providerPkg.ErrProviderTimeout
	}
	return providerPkg.ErrProviderNotAvailable
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
		return fmt.Errorf("failed to execute request: %w: %w", classifyTransportError(err), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w: %w", classifyTransportError(err), err)
	}

	if resp.StatusCode >= 400 {
		return &CashiError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
		}
	}

	if respBody != nil {
		if err := json.Unmarshal(body, respBody); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w: %w", providerPkg.ErrProviderAPIError, err)
		}
	}

	return nil
}

func (a *CashiAdapter) generateSignature(payload []byte) string {
	h := hmac.New(sha256.New, []byte(a.secretKey))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
