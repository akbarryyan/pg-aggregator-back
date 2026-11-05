package provider

import "errors"

// FeeQuoter is implemented by providers that deduct a percentage fee at
// settlement and can tell, for a merchant-facing nominal amount, the total the
// customer must pay so the merchant still nets the nominal.
type FeeQuoter interface {
	QuoteTotal(nominal int64) (total, feeAmount int64, err error)
}

// ErrFeeNotSupported is returned when a merchant is configured so the customer
// bears the fee but the selected provider cannot quote one. Failing closed is
// deliberate: silently charging the bare nominal would leave the merchant short.
var ErrFeeNotSupported = errors.New("provider cannot quote a customer-borne fee")
