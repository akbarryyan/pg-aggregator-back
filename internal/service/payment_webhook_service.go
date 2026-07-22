package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

func (s *PaymentService) ProcessWebhook(ctx context.Context, providerName string, rawPayload []byte, signature string) error {
	logger.Infof("Processing webhook from provider: %s", providerName)

	rawMap := make(map[string]interface{})
	if jsonErr := json.Unmarshal(rawPayload, &rawMap); jsonErr != nil {
		rawMap = map[string]interface{}{"raw": string(rawPayload)}
	}

	event := &domainProvider.WebhookEvent{
		ProviderName: providerName,
		EventType:    "received",
		Status:       "received",
		RawPayload:   rawMap,
	}
	if createErr := s.webhookEventRepo.Create(ctx, event); createErr != nil {
		logger.Errorf("Failed to persist raw webhook event: %v", createErr)
	}

	selectedProvider, err := s.getProviderByName(providerName)
	if err != nil {
		s.failWebhookEvent(ctx, event.ID, nil, "", "rejected", "rejected", err)
		return err
	}

	if err := selectedProvider.ValidateWebhook(rawPayload, signature); err != nil {
		logger.Errorf("Webhook validation failed: %v", err)
		s.failWebhookEvent(ctx, event.ID, nil, "", "rejected", "rejected", payment.ErrWebhookValidationFailed)
		return payment.ErrWebhookValidationFailed
	}

	webhookPayload, err := selectedProvider.ParseWebhook(rawPayload)
	if err != nil {
		if errors.Is(err, providerPkg.ErrTestWebhookEvent) {
			logger.Infof("Ignoring test webhook event from provider: %s", providerName)
			s.failWebhookEvent(ctx, event.ID, nil, "", "ignored", "ignored", providerPkg.ErrTestWebhookEvent)
			return nil
		}
		logger.Errorf("Failed to parse webhook: %v", err)
		s.failWebhookEvent(ctx, event.ID, nil, "", "rejected", "rejected", payment.ErrInvalidProviderReference)
		return payment.ErrInvalidProviderReference
	}

	logger.Infof("Webhook parsed: provider_reference=%s, status=%s", webhookPayload.ProviderReference, webhookPayload.Status)

	p, err := s.paymentRepo.GetByProviderReference(ctx, webhookPayload.ProviderReference)
	if err != nil {
		logger.Errorf("Payment not found for provider reference: %s", webhookPayload.ProviderReference)
		s.failWebhookEvent(ctx, event.ID, nil, webhookPayload.ProviderReference, webhookEventType(webhookPayload.Status), webhookPayload.Status, err)
		return err
	}

	if payment.IsTerminalStatus(p.Status) {
		logger.Warnf("Payment %s already in terminal status: %s", p.Reference, p.Status)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, webhookEventType(webhookPayload.Status), webhookPayload.Status, payment.ErrPaymentAlreadyTerminal)
		return payment.ErrPaymentAlreadyTerminal
	}

	if p.Status == webhookPayload.Status {
		logger.Infof("Payment %s status unchanged: %s (possible duplicate webhook)", p.Reference, p.Status)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, webhookEventType(webhookPayload.Status), webhookPayload.Status, payment.ErrDuplicateWebhook)
		return payment.ErrDuplicateWebhook
	}

	if !payment.CanTransitionTo(p.Status, webhookPayload.Status) {
		logger.Errorf("Invalid status transition from %s to %s for payment %s", p.Status, webhookPayload.Status, p.Reference)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, webhookEventType(webhookPayload.Status), webhookPayload.Status, payment.ErrInvalidStatusTransition)
		return payment.ErrInvalidStatusTransition
	}

	logger.Infof("Updating payment %s status from %s to %s", p.Reference, p.Status, webhookPayload.Status)

	if err := s.paymentRepo.UpdateStatus(ctx, p.ID, webhookPayload.Status, webhookPayload.PaidAt); err != nil {
		logger.Errorf("Failed to update payment status: %v", err)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, webhookEventType(webhookPayload.Status), webhookPayload.Status, err)
		return err
	}

	p.Status = webhookPayload.Status
	if webhookPayload.PaidAt != nil {
		p.PaidAt = webhookPayload.PaidAt
	}

	s.completeWebhookEvent(ctx, event.ID, p.ID, webhookPayload)

	// Best-effort merchant notify (failures are recorded, not returned to provider).
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(webhookPayload.Status))

	logger.Infof("Webhook processed successfully for payment: %s", p.Reference)
	return nil
}

func (s *PaymentService) ExpirePayments(ctx context.Context) error {
	logger.Info("Running payment expiration job")

	payments, err := s.paymentRepo.List(ctx, 100, 0)
	if err != nil {
		return fmt.Errorf("failed to list payments: %w", err)
	}

	expiredCount := 0
	for _, p := range payments {
		if p.Status == payment.StatusPending && time.Now().After(p.ExpiresAt) {
			logger.Infof("Expiring payment: %s", p.Reference)
			if err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); err != nil {
				logger.Errorf("Failed to expire payment %s: %v", p.Reference, err)
				continue
			}
			expiredCount++
		}
	}

	logger.Infof("Expired %d payments", expiredCount)
	return nil
}

// failWebhookEvent finalizes a webhook_events row as not processed, keeping the
// raw payload captured at receipt time traceable together with the reason it
// was rejected (invalid signature, unknown reference, duplicate, etc).
func (s *PaymentService) failWebhookEvent(ctx context.Context, eventID uuid.UUID, paymentID *uuid.UUID, providerReference, eventType, status string, cause error) {
	errMsg := cause.Error()
	if err := s.webhookEventRepo.Finalize(ctx, eventID, paymentID, providerReference, eventType, status, false, &errMsg); err != nil {
		logger.Errorf("Failed to finalize webhook event %s: %v", eventID, err)
	}
}

func (s *PaymentService) completeWebhookEvent(ctx context.Context, eventID, paymentID uuid.UUID, webhookPayload *domainProvider.ProviderWebhookPayload) {
	pid := paymentID
	eventType := webhookEventType(webhookPayload.Status)
	if err := s.webhookEventRepo.Finalize(ctx, eventID, &pid, webhookPayload.ProviderReference, eventType, webhookPayload.Status, true, nil); err != nil {
		logger.Errorf("Failed to finalize webhook event %s: %v", eventID, err)
	}
}

func webhookEventType(status string) string {
	switch status {
	case payment.StatusPaid:
		return domainProvider.WebhookEventTypePaymentPaid
	case payment.StatusExpired:
		return domainProvider.WebhookEventTypePaymentExpired
	case payment.StatusFailed:
		return domainProvider.WebhookEventTypePaymentFailed
	default:
		return "payment." + status
	}
}
