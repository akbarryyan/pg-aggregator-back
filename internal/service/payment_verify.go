package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

// providerVerifyTimeout bounds every check-status call made to confirm a
// settlement (webhook or reconcile): well under the server's 15s
// WriteTimeout, and tighter than the Cashi HTTP client's own per-call limit
// (requestTimeout, 10s, in internal/provider/cashi/client.go).
const providerVerifyTimeout = 5 * time.Second

// checkProviderStatus asks the provider for the payment's current status
// within providerVerifyTimeout.
func (s *PaymentService) checkProviderStatus(ctx context.Context, prov providerPkg.PaymentProvider, providerReference string) (*domainProvider.NormalizedPaymentStatus, error) {
	callCtx, cancel := context.WithTimeout(ctx, providerVerifyTimeout)
	defer cancel()
	return prov.GetPaymentStatus(callCtx, providerReference)
}

// verifyProviderPayment decides whether a provider status report is enough to
// mark p paid: the provider must report it settled, and its amount — and the
// webhook's, when the webhook carries one — must equal the gross amount we
// stored. Provider fees (e.g. Cashi's 0.7%) are deducted at settlement and
// never change what the customer paid. An unknown provider amount fails
// closed.
//
// The two mismatch kinds are kept apart on purpose: only a provider-side
// mismatch (ErrAmountMismatch) takes the payment out of automatic reconcile.
// A webhook that disagrees with a provider who confirms the stored amount is
// rejected (ErrWebhookAmountMismatch), but reconcile may still settle the
// payment from check-status, the source of truth.
func verifyProviderPayment(p *payment.Payment, status *domainProvider.NormalizedPaymentStatus, webhookAmount *int64) error {
	if status == nil || status.Status != payment.StatusPaid {
		return payment.ErrNotSettledAtProvider
	}
	if status.Amount == nil || *status.Amount != p.Amount {
		return fmt.Errorf("%w: expected %d, provider %s, webhook %s",
			payment.ErrAmountMismatch, p.Amount, formatAmount(status.Amount), formatAmount(webhookAmount))
	}
	if webhookAmount != nil && *webhookAmount != p.Amount {
		return fmt.Errorf("%w: expected %d, provider %s, webhook %s",
			payment.ErrWebhookAmountMismatch, p.Amount, formatAmount(status.Amount), formatAmount(webhookAmount))
	}
	return nil
}

func formatAmount(v *int64) string {
	if v == nil {
		return "none"
	}
	return strconv.FormatInt(*v, 10)
}
