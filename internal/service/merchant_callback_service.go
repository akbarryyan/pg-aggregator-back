package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

// maxCallbackAttempts caps auto-retry: once a delivery has failed this many
// times, NextRetryAt is left unset so RetryDueMerchantCallbacks stops
// picking it up. Admins can still retry it manually from the dashboard.
const maxCallbackAttempts = 5

// EnsureMerchantWebhookSecret returns the merchant's HMAC signing secret for
// outbound payment webhooks, generating and persisting one on first use.
// Mirrors CASHI_SECRET_KEY's role for inbound Cashi webhooks, just for the
// direction we control (see docs/cashi-api.md's webhook verification example).
func (s *PaymentService) EnsureMerchantWebhookSecret(ctx context.Context, merchantID uuid.UUID) (string, error) {
	if s.merchantRepo == nil {
		return "", fmt.Errorf("merchant repository not available")
	}
	m, err := s.merchantRepo.GetByID(ctx, merchantID)
	if err != nil {
		return "", err
	}
	if m.WebhookSecret != nil && *m.WebhookSecret != "" {
		return *m.WebhookSecret, nil
	}
	return s.RegenerateMerchantWebhookSecret(ctx, merchantID)
}

// RegenerateMerchantWebhookSecret issues a new secret, invalidating the old
// one immediately — any endpoint still verifying against the previous
// secret will start rejecting signatures until updated.
func (s *PaymentService) RegenerateMerchantWebhookSecret(ctx context.Context, merchantID uuid.UUID) (string, error) {
	if s.merchantRepo == nil {
		return "", fmt.Errorf("merchant repository not available")
	}
	secret, err := generateWebhookSecret()
	if err != nil {
		return "", fmt.Errorf("failed to generate webhook secret: %w", err)
	}
	if err := s.merchantRepo.SetWebhookSecret(ctx, merchantID, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func generateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func signWebhookPayload(secret string, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// NotifyMerchantPaymentEvent sends an outbound callback when payment status changes.
// Prefer payment.callback_url, else merchant.webhook_url. Skips if neither is set.
func (s *PaymentService) NotifyMerchantPaymentEvent(ctx context.Context, p *payment.Payment, eventType string) {
	if s.callbackRepo == nil || s.merchantRepo == nil || p == nil {
		return
	}

	targetURL := ""
	if p.CallbackURL != nil && strings.TrimSpace(*p.CallbackURL) != "" {
		targetURL = strings.TrimSpace(*p.CallbackURL)
	} else {
		m, err := s.merchantRepo.GetByID(ctx, p.MerchantID)
		if err != nil {
			logger.Warnf("Merchant callback skipped: merchant not found for payment %s: %v", p.Reference, err)
			return
		}
		if m.WebhookURL != nil {
			targetURL = strings.TrimSpace(*m.WebhookURL)
		}
	}

	if targetURL == "" {
		logger.Infof("Merchant callback skipped for payment %s: no webhook URL", p.Reference)
		return
	}

	payload := map[string]interface{}{
		"event":       eventType,
		"payment_id":  p.ID.String(),
		"reference":   p.Reference,
		"status":      p.Status,
		"amount":      p.Amount,
		"currency":    p.Currency,
		"merchant_id": p.MerchantID.String(),
		"provider":    p.ProviderName,
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
	}
	if p.ProviderReference != nil {
		payload["provider_reference"] = *p.ProviderReference
	}
	if p.PaidAt != nil {
		payload["paid_at"] = p.PaidAt.UTC().Format(time.RFC3339)
	}

	delivery := &merchant.CallbackDelivery{
		ID:             uuid.New(),
		PaymentID:      p.ID,
		MerchantID:     p.MerchantID,
		EventType:      eventType,
		TargetURL:      targetURL,
		RequestPayload: payload,
		AttemptNumber:  1,
		Status:         merchant.CallbackStatusPending,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	if err := s.callbackRepo.Create(ctx, delivery); err != nil {
		logger.Errorf("Failed to persist merchant callback for %s: %v", p.Reference, err)
		return
	}

	s.executeCallbackDelivery(ctx, delivery)
}

func (s *PaymentService) RetryMerchantCallback(ctx context.Context, id uuid.UUID) (*merchant.CallbackDelivery, error) {
	if s.callbackRepo == nil {
		return nil, fmt.Errorf("callback repository not available")
	}
	existing, err := s.callbackRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Create a new attempt row (keeps history) based on previous delivery.
	retry := &merchant.CallbackDelivery{
		ID:             uuid.New(),
		PaymentID:      existing.PaymentID,
		MerchantID:     existing.MerchantID,
		EventType:      existing.EventType,
		TargetURL:      existing.TargetURL,
		RequestPayload: existing.RequestPayload,
		AttemptNumber:  existing.AttemptNumber + 1,
		Status:         merchant.CallbackStatusPending,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := s.callbackRepo.Create(ctx, retry); err != nil {
		return nil, err
	}
	s.executeCallbackDelivery(ctx, retry)

	// A new attempt now exists for this failure — clear the source
	// delivery's retry schedule so RetryDueMerchantCallbacks doesn't pick
	// the same failure up again on the next tick.
	existing.NextRetryAt = nil
	_ = s.callbackRepo.UpdateResult(ctx, existing)

	return retry, nil
}

func (s *PaymentService) executeCallbackDelivery(ctx context.Context, d *merchant.CallbackDelivery) {
	body, err := json.Marshal(d.RequestPayload)
	if err != nil {
		msg := err.Error()
		d.Status = merchant.CallbackStatusFailed
		d.ErrorMessage = &msg
		_ = s.callbackRepo.UpdateResult(ctx, d)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TargetURL, bytes.NewReader(body))
	if err != nil {
		msg := err.Error()
		d.Status = merchant.CallbackStatusFailed
		d.ErrorMessage = &msg
		_ = s.callbackRepo.UpdateResult(ctx, d)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pg-aggregator-callback/1.0")
	req.Header.Set("X-PG-Event", d.EventType)

	if secret, secretErr := s.EnsureMerchantWebhookSecret(ctx, d.MerchantID); secretErr != nil {
		// Signing is best-effort: a transient secret-provisioning failure
		// shouldn't block the merchant from learning their payment status
		// changed. The delivery just goes out unsigned this one time.
		logger.Errorf("Failed to get webhook secret for merchant %s, sending unsigned: %v", d.MerchantID, secretErr)
	} else {
		req.Header.Set("X-PG-Signature", signWebhookPayload(secret, body))
	}

	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		d.Status = merchant.CallbackStatusFailed
		d.ErrorMessage = &msg
		d.NextRetryAt = nextRetryAt(d.AttemptNumber)
		_ = s.callbackRepo.UpdateResult(ctx, d)
		logger.Warnf("Merchant callback failed for %s → %s: %v", d.PaymentID, d.TargetURL, err)
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	respStr := string(respBytes)
	statusCode := resp.StatusCode
	d.HTTPStatus = &statusCode
	if respStr != "" {
		d.ResponseBody = &respStr
	}

	now := time.Now().UTC()
	if statusCode >= 200 && statusCode < 300 {
		d.Status = merchant.CallbackStatusSuccess
		d.DeliveredAt = &now
		d.ErrorMessage = nil
		d.NextRetryAt = nil
		logger.Infof("Merchant callback success (%d) for payment %s", statusCode, d.PaymentID)
	} else {
		d.Status = merchant.CallbackStatusFailed
		msg := fmt.Sprintf("unexpected HTTP status %d", statusCode)
		d.ErrorMessage = &msg
		d.NextRetryAt = nextRetryAt(d.AttemptNumber)
		logger.Warnf("Merchant callback HTTP %d for payment %s", statusCode, d.PaymentID)
	}
	_ = s.callbackRepo.UpdateResult(ctx, d)
}

// nextRetryAt returns when the next auto-retry should happen, or nil once
// attemptNumber has reached maxCallbackAttempts (stops ListDueForRetry from
// picking the delivery back up).
func nextRetryAt(attemptNumber int) *time.Time {
	if attemptNumber >= maxCallbackAttempts {
		return nil
	}
	next := time.Now().UTC().Add(5 * time.Minute)
	return &next
}

// RetryDueMerchantCallbacks re-attempts failed callback deliveries whose
// next_retry_at has passed. Best-effort per item: one delivery failing to
// retry does not stop the rest of the batch. Returns how many were attempted.
func (s *PaymentService) RetryDueMerchantCallbacks(ctx context.Context, limit int) (int, error) {
	if s.callbackRepo == nil {
		return 0, nil
	}

	due, err := s.callbackRepo.ListDueForRetry(ctx, time.Now().UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("failed to list due callback retries: %w", err)
	}

	for _, d := range due {
		if _, err := s.RetryMerchantCallback(ctx, d.ID); err != nil {
			logger.Errorf("Failed to auto-retry callback %s: %v", d.ID, err)
		}
	}

	return len(due), nil
}

func eventTypeForStatus(status string) string {
	switch status {
	case payment.StatusPaid:
		return "payment.paid"
	case payment.StatusExpired:
		return "payment.expired"
	case payment.StatusFailed:
		return "payment.failed"
	case payment.StatusCancelled:
		return "payment.cancelled"
	default:
		return "payment." + status
	}
}
