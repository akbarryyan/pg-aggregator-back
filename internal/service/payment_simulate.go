package service

import (
	"context"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

// sandboxProviderName is the sandbox adapter's name (provider/sandbox.ProviderName);
// repeated here so the service does not import the adapter package.
const sandboxProviderName = "sandbox"

// SimulateSandboxOutcome drives a pending sandbox payment to paid, failed or
// expired through the same repository writes and merchant callback as a real
// settlement or expiry, so merchants can test their integration end to end.
// Production payments (and sandbox-environment payments on a real provider)
// are never simulated. p is updated in place and returned.
func (s *PaymentService) SimulateSandboxOutcome(ctx context.Context, p *payment.Payment, outcome string) (*payment.Payment, error) {
	switch outcome {
	case payment.StatusPaid, payment.StatusFailed, payment.StatusExpired:
	default:
		return nil, payment.ErrInvalidSimulationOutcome
	}
	if p.Environment != payment.EnvironmentSandbox || p.ProviderName != sandboxProviderName {
		return nil, payment.ErrSimulationNotAllowed
	}
	if p.Status != payment.StatusPending {
		return nil, payment.ErrPaymentAlreadyTerminal
	}

	// Once the status is written the callback must follow, even if the
	// request is cancelled now (same reasoning as reconcile).
	ctx, cancel := detached(ctx)
	defer cancel()

	if outcome == payment.StatusPaid {
		net, err := merchantNetFor(p)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		if _, err := s.paymentRepo.MarkPaid(ctx, p.ID, &now, net); err != nil {
			return nil, err
		}
		p.PaidAt = &now
		p.MerchantNetAmount = net
	} else {
		if _, err := s.paymentRepo.UpdateStatus(ctx, p.ID, outcome, nil); err != nil {
			return nil, err
		}
	}
	p.Status = outcome

	s.recordSimulation(ctx, p, outcome)
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(outcome), false)
	return p, nil
}

// recordSimulation leaves a webhook_events row so the simulation shows in the
// admin payment timeline and logs. Best effort: a failure is only logged.
func (s *PaymentService) recordSimulation(ctx context.Context, p *payment.Payment, outcome string) {
	pid := p.ID
	ref := ""
	if p.ProviderReference != nil {
		ref = *p.ProviderReference
	}
	now := time.Now()
	ev := &provider.WebhookEvent{
		PaymentID:         &pid,
		ProviderName:      sandboxProviderName,
		ProviderReference: ref,
		EventType:         "sandbox.simulate." + outcome,
		Status:            outcome,
		RawPayload: map[string]interface{}{
			"source":  "sandbox_simulator",
			"outcome": outcome,
		},
		IsProcessed: true,
		ProcessedAt: &now,
	}
	if err := s.webhookEventRepo.Create(ctx, ev); err != nil {
		logger.ErrorfCtx(ctx, "Failed to record sandbox simulation for %s: %v", p.Reference, err)
	}
}
