package klikqris

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
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

type KlikQrisAdapter struct {
	baseURL    string
	apiKey     string
	secretKey  string
	merchantID string
	httpClient *http.Client
}

func NewKlikQrisAdapter(baseURL, apiKey, secretKey, merchantID string) *KlikQrisAdapter {
	return &KlikQrisAdapter{
		baseURL:    baseURL,
		apiKey:     apiKey,
		secretKey:  secretKey,
		merchantID: merchantID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (a *KlikQrisAdapter) GetName() string {
	return provider.ProviderKlikQris
}

func (a *KlikQrisAdapter) CreatePayment(ctx context.Context, req *provider.ProviderPaymentRequest) (*provider.ProviderPaymentResponse, error) {
	klikqrisReq := &CreateQRISRequest{
		MerchantID:   a.merchantID,
		Amount:       req.Amount,
		Currency:     req.Currency,
		ReferenceNo:  req.InternalReference,
		Description:  req.Description,
		CallbackURL:  req.CallbackURL,
		ExpiredAt:    req.ExpiresAt.Format(time.RFC3339),
	}

	if req.CustomerName != nil {
		klikqrisReq.CustomerName = *req.CustomerName
	}
	if req.CustomerEmail != nil {
		klikqrisReq.CustomerEmail = *req.CustomerEmail
	}

	var klikqrisResp CreateQRISResponse
	if err := a.doRequest(ctx, "POST", "/api/v1/qris/create", klikqrisReq, &klikqrisResp); err != nil {
		return nil, fmt.Errorf("failed to create QRIS payment: %w", err)
	}

	if klikqrisResp.Status != "success" || klikqrisResp.Data == nil {
		return nil, &KlikQrisError{
			StatusCode: 400,
			Message:    klikqrisResp.Message,
			Detail:     klikqrisResp.Error,
		}
	}

	rawResponse := map[string]interface{}{
		"transaction_id": klikqrisResp.Data.TransactionID,
		"qris_string":    klikqrisResp.Data.QRISString,
		"qris_image_url": klikqrisResp.Data.QRISImageURL,
		"status":         klikqrisResp.Data.Status,
	}

	return &provider.ProviderPaymentResponse{
		ProviderReference: klikqrisResp.Data.TransactionID,
		ProviderName:      provider.ProviderKlikQris,
		Status:            a.NormalizeStatus(klikqrisResp.Data.Status),
		QRISData:          &klikqrisResp.Data.QRISString,
		ExpiresAt:         klikqrisResp.Data.ExpiredAt,
		RawResponse:       rawResponse,
	}, nil
}

func (a *KlikQrisAdapter) GetPaymentStatus(ctx context.Context, providerReference string) (*provider.NormalizedPaymentStatus, error) {
	checkReq := &CheckStatusRequest{
		MerchantID:    a.merchantID,
		TransactionID: providerReference,
	}

	var checkResp CheckStatusResponse
	if err := a.doRequest(ctx, "POST", "/api/v1/qris/check-status", checkReq, &checkResp); err != nil {
		return nil, fmt.Errorf("failed to check payment status: %w", err)
	}

	if checkResp.Status != "success" || checkResp.Data == nil {
		return nil, &KlikQrisError{
			StatusCode: 400,
			Message:    checkResp.Message,
			Detail:     checkResp.Error,
		}
	}

	return &provider.NormalizedPaymentStatus{
		Status:            a.NormalizeStatus(checkResp.Data.Status),
		ProviderReference: checkResp.Data.TransactionID,
		PaidAt:            checkResp.Data.PaidAt,
	}, nil
}

func (a *KlikQrisAdapter) ValidateWebhook(rawPayload []byte, signature string) error {
	expectedSignature := a.generateSignature(rawPayload)
	if !hmac.Equal([]byte(expectedSignature), []byte(signature)) {
		return providerPkg.ErrInvalidWebhookSignature
	}
	return nil
}

func (a *KlikQrisAdapter) ParseWebhook(rawPayload []byte) (*provider.ProviderWebhookPayload, error) {
	var webhook WebhookPayload
	if err := json.Unmarshal(rawPayload, &webhook); err != nil {
		return nil, fmt.Errorf("failed to parse webhook payload: %w", err)
	}

	rawMap := make(map[string]interface{})
	json.Unmarshal(rawPayload, &rawMap)

	return &provider.ProviderWebhookPayload{
		ProviderName:      provider.ProviderKlikQris,
		ProviderReference: webhook.TransactionID,
		Status:            a.NormalizeStatus(webhook.Status),
		PaidAt:            &webhook.PaidAt,
		Amount:            &webhook.Amount,
		RawPayload:        rawMap,
	}, nil
}

func (a *KlikQrisAdapter) NormalizeStatus(providerStatus string) string {
	switch providerStatus {
	case StatusSuccess:
		return "paid"
	case StatusPending:
		return "pending"
	case StatusExpired:
		return "expired"
	case StatusFailed:
		return "failed"
	default:
		return "pending"
	}
}

func (a *KlikQrisAdapter) doRequest(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
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
	req.Header.Set("X-API-Key", a.apiKey)

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
		return &KlikQrisError{
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

func (a *KlikQrisAdapter) generateSignature(payload []byte) string {
	h := hmac.New(sha256.New, []byte(a.secretKey))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
