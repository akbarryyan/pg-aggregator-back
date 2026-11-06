package ledger

import "errors"

var (
	ErrInsufficientBalance    = errors.New("ledger: insufficient balance")
	ErrBelowMinimumPayout     = errors.New("ledger: amount below minimum payout")
	ErrPayoutAccountMissing   = errors.New("ledger: payout account not set")
	ErrOpenPayoutExists       = errors.New("ledger: an open payout already exists")
	ErrPayoutNotFound         = errors.New("ledger: payout not found")
	ErrPayoutAlreadyProcessed = errors.New("ledger: payout already processed")
	ErrRefundExceedsNet       = errors.New("ledger: refund exceeds merchant net amount")
	ErrPaymentNotRefundable   = errors.New("ledger: payment is not refundable")
	ErrInvalidAdjustment      = errors.New("ledger: invalid adjustment")
	ErrReasonRequired         = errors.New("ledger: reason is required")
	ErrInvalidAmount          = errors.New("ledger: amount must be greater than 0")
	ErrTextTooLong            = errors.New("ledger: reference or reason is too long")
)
