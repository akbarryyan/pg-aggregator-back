package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

// webhookEventTypePaidLate records a settlement that arrived after the
// payment had already expired.
const webhookEventTypePaidLate = "payment.paid.late"

func (s *PaymentService) ProcessWebhook(ctx context.Context, providerName string, rawPayload []byte, signature string) error {
	logger.InfofCtx(ctx, "Processing webhook from provider: %s", providerName)

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
		logger.ErrorfCtx(ctx, "Failed to persist raw webhook event: %v", createErr)
	}

	selectedProvider, err := s.getProviderByName(providerName)
	if err != nil {
		s.failWebhookEvent(ctx, event.ID, nil, "", "rejected", "rejected", err)
		return err
	}

	if err := selectedProvider.ValidateWebhook(rawPayload, signature); err != nil {
		logger.ErrorfCtx(ctx, "Webhook validation failed: %v", err)
		s.failWebhookEvent(ctx, event.ID, nil, "", "rejected", "rejected", payment.ErrWebhookValidationFailed)
		return payment.ErrWebhookValidationFailed
	}

	webhookPayload, err := selectedProvider.ParseWebhook(rawPayload)
	if err != nil {
		if errors.Is(err, providerPkg.ErrTestWebhookEvent) {
			logger.InfofCtx(ctx, "Ignoring test webhook event from provider: %s", providerName)
			s.failWebhookEvent(ctx, event.ID, nil, "", "ignored", "ignored", providerPkg.ErrTestWebhookEvent)
			return nil
		}
		logger.ErrorfCtx(ctx, "Failed to parse webhook: %v", err)
		s.failWebhookEvent(ctx, event.ID, nil, "", "rejected", "rejected", payment.ErrInvalidProviderReference)
		return payment.ErrInvalidProviderReference
	}

	logger.InfofCtx(ctx, "Webhook parsed: provider_reference=%s, status=%s", webhookPayload.ProviderReference, webhookPayload.Status)

	eventType := webhookEventType(webhookPayload.Status)

	p, err := s.paymentRepo.GetByProviderReference(ctx, webhookPayload.ProviderReference)
	if err != nil {
		logger.ErrorfCtx(ctx, "Payment not found for provider reference: %s", webhookPayload.ProviderReference)
		s.failWebhookEvent(ctx, event.ID, nil, webhookPayload.ProviderReference, eventType, webhookPayload.Status, err)
		return err
	}
	if !strings.EqualFold(p.ProviderName, selectedProvider.GetName()) {
		// Only the provider that owns the payment may confirm its settlement.
		logger.ErrorfCtx(ctx, "Webhook from provider %s for payment %s owned by %s", selectedProvider.GetName(), p.Reference, p.ProviderName)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, "rejected", payment.ErrProviderMismatch)
		return payment.ErrProviderMismatch
	}

	// Only a provider-reported settlement can move a payment; any other event
	// is acknowledged and ignored.
	if webhookPayload.Status != payment.StatusPaid {
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, payment.ErrWebhookIgnored)
		return payment.ErrWebhookIgnored
	}

	switch p.Status {
	case payment.StatusPaid:
		logger.InfofCtx(ctx, "Payment %s already paid (duplicate webhook)", p.Reference)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, payment.ErrDuplicateWebhook)
		return payment.ErrDuplicateWebhook
	case payment.StatusFailed, payment.StatusCancelled:
		logger.WarnfCtx(ctx, "Payment %s already in terminal status: %s", p.Reference, p.Status)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, payment.ErrPaymentAlreadyTerminal)
		return payment.ErrPaymentAlreadyTerminal
	}

	// The webhook is only a trigger. Confirm the settlement and its amount
	// with the provider's own status API before trusting it, so a forged or
	// replayed webhook (even one signed with a leaked secret) can't mark a
	// payment paid.
	providerStatus, verifyErr := s.checkProviderStatus(ctx, selectedProvider, webhookPayload.ProviderReference)
	if verifyErr == nil {
		verifyErr = verifyProviderPayment(p, providerStatus, webhookPayload.Amount)
	}
	if verifyErr != nil {
		if errors.Is(verifyErr, payment.ErrAmountMismatch) || errors.Is(verifyErr, payment.ErrWebhookAmountMismatch) {
			// Not something a retry fixes: acknowledge and flag it. Only the
			// provider-side kind also needs a human (it excludes the payment
			// from reconcile); the webhook-only kind leaves reconcile free to
			// settle it from check-status.
			logger.ErrorfCtx(ctx, "Payment %s: %v", p.Reference, verifyErr)
			s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, "rejected", verifyErr)
			return verifyErr
		}
		logger.WarnfCtx(ctx, "Payment %s: settlement not confirmed by provider: %v", p.Reference, verifyErr)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, "verification_failed", verifyErr)
		return fmt.Errorf("%w: %v", payment.ErrProviderVerificationFailed, verifyErr)
	}

	// From here the settlement is confirmed: finish the update, the event
	// bookkeeping and the merchant callback even if the provider hangs up.
	// Reconcile never revisits a paid payment, so a callback dropped here
	// would be lost for good.
	ctx, cancel := detached(ctx)
	defer cancel()

	paidAt := webhookPayload.PaidAt
	if paidAt == nil {
		now := time.Now()
		paidAt = &now
	}

	previous, err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusPaid, paidAt)
	if err != nil {
		if errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
			// Lost the race to a concurrent webhook or reconcile run that
			// already settled it; that caller notified the merchant.
			s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, payment.ErrDuplicateWebhook)
			return payment.ErrDuplicateWebhook
		}
		logger.ErrorfCtx(ctx, "Failed to update payment status: %v", err)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, err)
		return err
	}

	late := previous == payment.StatusExpired
	if late {
		eventType = webhookEventTypePaidLate
		logger.WarnfCtx(ctx, "Payment %s settled after it had expired (late payment)", p.Reference)
	}
	p.Status = payment.StatusPaid
	p.PaidAt = paidAt

	s.completeWebhookEvent(ctx, event.ID, p.ID, webhookPayload.ProviderReference, eventType, payment.StatusPaid)

	// Best-effort merchant notify (failures are recorded, not returned to provider).
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusPaid), late)

	logger.InfofCtx(ctx, "Webhook processed successfully for payment: %s", p.Reference)
	return nil
}

// expireGracePeriod delays local expiry past expires_at.
// why: a customer paying in the last seconds must not get the merchant an
// `expired` callback followed by a late `paid`; reconcile (every 2 min) gets
// a chance to see the settlement first.
const expireGracePeriod = 5 * time.Minute

// ExpirePayments marks pending payments whose expires_at passed more than
// expireGracePeriod ago as expired. It queries by status+expires_at directly
// (not a scan of the most recent N payments of any status), so it still finds
// old stragglers once payment volume grows past a single page.
func (s *PaymentService) ExpirePayments(ctx context.Context) error {
	const batchSize = 200

	payments, err := s.paymentRepo.ListExpiredPending(ctx, time.Now().Add(-expireGracePeriod), batchSize)
	if err != nil {
		return fmt.Errorf("failed to list expired pending payments: %w", err)
	}

	expiredCount := 0
	for _, p := range payments {
		logger.InfofCtx(ctx, "Expiring payment: %s", p.Reference)
		// Detached per payment so a shutdown mid-run can't commit the expiry
		// and then drop its merchant callback.
		pctx, cancel := detached(ctx)
		if _, err := s.paymentRepo.UpdateStatus(pctx, p.ID, payment.StatusExpired, nil); err != nil {
			cancel()
			if errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
				// Lost a race to a concurrent settlement: that caller owns
				// the merchant notification.
				logger.InfofCtx(ctx, "Payment %s already settled/changed concurrently; not expiring", p.Reference)
				continue
			}
			logger.ErrorfCtx(ctx, "Failed to expire payment %s: %v", p.Reference, err)
			continue
		}
		p.Status = payment.StatusExpired
		expiredCount++
		s.NotifyMerchantPaymentEvent(pctx, p, eventTypeForStatus(payment.StatusExpired), false)
		cancel()
	}

	if expiredCount > 0 {
		logger.InfofCtx(ctx, "Expired %d payments", expiredCount)
	}
	return nil
}

// failWebhookEvent finalizes a webhook_events row as not processed, keeping the
// raw payload captured at receipt time traceable together with the reason it
// was rejected (invalid signature, unknown reference, duplicate, etc).
func (s *PaymentService) failWebhookEvent(ctx context.Context, eventID uuid.UUID, paymentID *uuid.UUID, providerReference, eventType, status string, cause error) {
	errMsg := cause.Error()
	if err := s.webhookEventRepo.Finalize(ctx, eventID, paymentID, providerReference, eventType, status, false, &errMsg); err != nil {
		logger.ErrorfCtx(ctx, "Failed to finalize webhook event %s: %v", eventID, err)
	}
}

func (s *PaymentService) completeWebhookEvent(ctx context.Context, eventID, paymentID uuid.UUID, providerReference, eventType, status string) {
	pid := paymentID
	if err := s.webhookEventRepo.Finalize(ctx, eventID, &pid, providerReference, eventType, status, true, nil); err != nil {
		logger.ErrorfCtx(ctx, "Failed to finalize webhook event %s: %v", eventID, err)
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
