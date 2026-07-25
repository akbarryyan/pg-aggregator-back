package paymentlink

import "errors"

var (
	ErrPaymentLinkNotFound         = errors.New("payment link not found")
	ErrTitleRequired               = errors.New("title is required")
	ErrInvalidAmountType           = errors.New("amount_type must be 'fixed' or 'open'")
	ErrFixedAmountRequired         = errors.New("amount is required and must be greater than 0 for fixed amount links")
	ErrAmountNotAllowedForOpenLink = errors.New("amount must not be set for open amount links")
	ErrInvalidAmountBounds         = errors.New("min_amount/max_amount must be within platform limits and min_amount must not exceed max_amount")
	ErrAmountOutOfPlatformBounds   = errors.New("amount must be between 2,000 and 10,000,000 IDR")
	ErrMerchantIDRequired          = errors.New("merchant ID is required")
	ErrPaymentLinkInactive         = errors.New("payment link is inactive")
	ErrPaymentLinkExpired          = errors.New("payment link has expired")
	ErrCustomerAmountRequired      = errors.New("amount is required for this payment link")
	ErrCustomerAmountOutOfRange    = errors.New("amount is outside the allowed range for this payment link")
)
