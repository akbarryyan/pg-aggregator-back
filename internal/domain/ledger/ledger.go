// Package ledger models the append-only merchant balance ledger: every
// movement of a merchant's money (payment credit, refund, payout) is a row.
package ledger

import (
	"time"

	"github.com/google/uuid"
)

const (
	EntryPaymentCredit  = "payment_credit"
	EntryRefundDebit    = "refund_debit"
	EntryPayoutDebit    = "payout_debit"
	EntryPayoutReversal = "payout_reversal"
	EntryAdjustment     = "adjustment"
)

// Entry is one ledger row. Amount is signed: credits positive, debits negative.
type Entry struct {
	ID         uuid.UUID  `json:"id"`
	MerchantID uuid.UUID  `json:"merchant_id"`
	EntryType  string     `json:"entry_type"`
	Amount     int64      `json:"amount"`
	PaymentID  *uuid.UUID `json:"payment_id,omitempty"`
	PayoutID   *uuid.UUID `json:"payout_id,omitempty"`
	RefundID   *uuid.UUID `json:"refund_id,omitempty"`
	Note       string     `json:"note"`
	CreatedBy  *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Summary aggregates a merchant's ledger. TotalPaidOut counts payouts that
// are requested or paid, net of reversals (rejected payouts).
//
// Balance is the sum of EVERY ledger row, adjustments included. The three
// totals deliberately exclude adjustments (they are neither credits, payouts
// nor refunds), so Balance != TotalCredited - TotalPaidOut - TotalRefunded
// whenever an adjustment exists.
type Summary struct {
	Balance       int64 `json:"balance"`
	TotalCredited int64 `json:"total_credited"`
	TotalPaidOut  int64 `json:"total_paid_out"`
	TotalRefunded int64 `json:"total_refunded"`
}

// Payout statuses.
const (
	PayoutRequested = "requested"
	PayoutPaid      = "paid"
	PayoutRejected  = "rejected"
)

// IsValidPayoutStatus reports whether s is one of the three payout statuses.
func IsValidPayoutStatus(s string) bool {
	return s == PayoutRequested || s == PayoutPaid || s == PayoutRejected
}

// Payout is a merchant's withdrawal request. The bank fields are a snapshot
// of the merchant's payout account at request time. MerchantName is only
// filled by admin listings.
type Payout struct {
	ID            uuid.UUID  `json:"id"`
	MerchantID    uuid.UUID  `json:"merchant_id"`
	MerchantName  string     `json:"merchant_name,omitempty"`
	Amount        int64      `json:"amount"`
	Status        string     `json:"status"`
	BankName      string     `json:"bank_name"`
	AccountNumber string     `json:"account_number"`
	AccountHolder string     `json:"account_holder"`
	RequestedBy   *uuid.UUID `json:"requested_by,omitempty"`
	RequestedAt   time.Time  `json:"requested_at"`
	ProcessedBy   *uuid.UUID `json:"processed_by,omitempty"`
	ProcessedAt   *time.Time `json:"processed_at,omitempty"`
	Reference     *string    `json:"reference,omitempty"`
	RejectReason  *string    `json:"reject_reason,omitempty"`
}

// MerchantBalance is one row of the admin balances overview.
type MerchantBalance struct {
	MerchantID       uuid.UUID  `json:"merchant_id"`
	MerchantName     string     `json:"merchant_name"`
	Balance          int64      `json:"balance"`
	OpenPayoutID     *uuid.UUID `json:"open_payout_id,omitempty"`
	OpenPayoutAmount int64      `json:"open_payout_amount"`
}

// MaxAdjustment bounds a single admin balance adjustment (absolute value).
const MaxAdjustment int64 = 1_000_000_000_000

// MaxReasonLen bounds refund reasons and adjustment notes (characters).
const MaxReasonLen = 500

// Refund is an admin-recorded refund of (part of) a payment. The money moves
// outside the platform; this row and its ledger debit are the record.
type Refund struct {
	ID         uuid.UUID  `json:"id"`
	PaymentID  uuid.UUID  `json:"payment_id"`
	MerchantID uuid.UUID  `json:"merchant_id"`
	Amount     int64      `json:"amount"`
	Reason     string     `json:"reason"`
	CreatedBy  *uuid.UUID `json:"created_by,omitempty"` // admin-only: never put in a merchant-facing response
	CreatedAt  time.Time  `json:"created_at"`
}

// RefundItem is the admin-facing view of one refund (no created_by).
type RefundItem struct {
	ID        uuid.UUID `json:"id"`
	Amount    int64     `json:"amount"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// RefundSummary is a payment's refund history plus the refundable figures.
type RefundSummary struct {
	Items               []RefundItem `json:"items"`
	MerchantNetAmount   int64        `json:"merchant_net_amount"`
	RefundedAmount      int64        `json:"refunded_amount"`
	RemainingRefundable int64        `json:"remaining_refundable"`
	Refundable          bool         `json:"refundable"`
}
