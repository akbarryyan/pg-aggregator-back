package service

import (
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/fee"
)

// MerchantReceives is what the merchant gets from p: the stored net once the
// payment is paid (or refunded after being paid), otherwise the amount it will
// net if it is paid (same rule as merchantNetFor). It never errors for a
// stored payment: an out-of-range nominal falls back to the nominal amount.
func MerchantReceives(p *payment.Payment) int64 {
	if p.MerchantNetAmount > 0 {
		return p.MerchantNetAmount
	}
	net, err := merchantNetFor(p)
	if err != nil {
		return p.Amount
	}
	return net
}

// merchantNetFor returns what the merchant keeps from a payment, decided by
// the payment itself rather than the merchant's current fee bearer: a payment
// that carries a fee (customer paid it) nets the full nominal amount;
// otherwise the provider fee is deducted from the nominal amount.
func merchantNetFor(p *payment.Payment) (int64, error) {
	if p.FeeAmount > 0 {
		return p.Amount, nil
	}
	net, _, err := fee.MerchantNetBasisPoints(p.Amount, fee.ProviderBasisPoints)
	if err != nil {
		return 0, err
	}
	return net, nil
}
