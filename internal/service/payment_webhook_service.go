package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"pg-aggregator/internal/domain/payment"
	"pg-aggregator/pkg/logger"
)

func (s *PaymentService) ProcessWebhook(ctx context.Context, providerName string, rawPayload []byte, signature string) error {
	logger.Infof("Processing webhook from provider: %s", providerName)

	if s.providerAdapter.GetName() != providerName {
		return fmt.Errorf("provider mismatch: expected %s, got %s", s.providerAdapter.GetName(), providerName)
	}

	if err := s.providerAdapter.ValidateWebhook(rawPayload, signature); err != nil {
		logger.Errorf("Webhook validation failed: %v", err)
		return payment.ErrWebhookValidationFailed
	}

	webhookPayload, err := s.providerAdapter.ParseWebhook(rawPayload)
	if err != nil {
		logger.Errorf("Failed to parse webhook: %v", err)
		return payment.ErrInvalidProviderReference
	}

	logger.Infof("Webhook parsed: provider_reference=%s, status=%s", webhookPayload.ProviderReference, webhookPayload.Status)

	p, err := s.paymentRepo.GetByProviderReference(ctx, webhookPayload.ProviderReference)
	if err != nil {
		logger.Errorf("Payment not found for provider reference: %s", webhookPayload.ProviderReference)
		return err
	}

	if payment.IsTerminalStatus(p.Status) {
		logger.Warnf("Payment %s already in terminal status: %s", p.Reference, p.Status)
		return payment.ErrPaymentAlreadyTerminal
	}

	if p.Status == webhookPayload.Status {
		logger.Infof("Payment %s status unchanged: %s (possible duplicate webhook)", p.Reference, p.Status)
		return payment.ErrDuplicateWebhook
	}

	if !payment.CanTransitionTo(p.Status, webhookPayload.Status) {
		logger.Errorf("Invalid status transition from %s to %s for payment %s", p.Status, webhookPayload.Status, p.Reference)
		return payment.ErrInvalidStatusTransition
	}

	logger.Infof("Updating payment %s status from %s to %s", p.Reference, p.Status, webhookPayload.Status)

	if err := s.paymentRepo.UpdateStatus(ctx, p.ID, webhookPayload.Status, webhookPayload.PaidAt); err != nil {
		logger.Errorf("Failed to update payment status: %v", err)
		return err
	}

	s.logWebhookEvent(ctx, p.Reference, webhookPayload, rawPayload)

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

func (s *PaymentService) logWebhookEvent(ctx context.Context, reference string, webhookPayload interface{}, rawPayload []byte) {
	webhookJSON, _ := json.Marshal(webhookPayload)
	logger.Infof("Webhook event for %s: %s (raw: %s)", reference, string(webhookJSON), string(rawPayload))
}
