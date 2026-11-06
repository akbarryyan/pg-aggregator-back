package service

import (
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/fee"
)

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
