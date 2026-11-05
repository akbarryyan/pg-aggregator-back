package merchant

import "errors"

// Who bears the provider's percentage fee on a payment.
const (
	// FeeBearerMerchant: the customer pays the nominal; the fee comes out of
	// the merchant's proceeds (the behaviour before customer-borne fees).
	FeeBearerMerchant = "merchant"
	// FeeBearerCustomer: the fee is added on top, so the merchant receives the
	// full nominal.
	FeeBearerCustomer = "customer"
)

var ErrInvalidFeeBearer = errors.New("fee_bearer must be 'merchant' or 'customer'")

func IsValidFeeBearer(s string) bool { return s == FeeBearerMerchant || s == FeeBearerCustomer }
