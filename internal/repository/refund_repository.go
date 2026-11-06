package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

type RefundRepository struct {
	db *sql.DB
}

func NewRefundRepository(db *sql.DB) *RefundRepository {
	return &RefundRepository{db: db}
}

// Refund records a refund of amount against a paid production payment and
// debits the merchant's ledger, in one transaction.
//
// The payment row is locked (FOR UPDATE) first; that serializes every refund
// of the same payment, so the "already refunded" sum cannot race. No merchant
// row lock is taken: payout Request locks the merchant row and touches no
// payments rows, so the two lock orders can never form a cycle. merchant_id
// comes from the locked payment row, and the refund row and the ledger row
// are written from the same variables (the DB does not enforce that they
// agree). When the refund exhausts the merchant net the payment becomes
// 'refunded' (paid_at is left untouched) and full is true.
//
// Errors are returned without further pool queries while the transaction is
// open (the deferred rollback releases the lock and the connection).
func (r *RefundRepository) Refund(ctx context.Context, paymentID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (refund *ledger.Refund, full bool, p *payment.Payment, err error) {
	refund, full, p, _, err = r.RefundWithRemaining(ctx, paymentID, amount, reason, adminID)
	return refund, full, p, err
}

// RefundWithRemaining is Refund that also reports how much of the merchant
// net is still refundable afterwards (read in the same transaction).
func (r *RefundRepository) RefundWithRemaining(ctx context.Context, paymentID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*ledger.Refund, bool, *payment.Payment, int64, error) {
	reason = strings.TrimSpace(reason)
	if amount <= 0 {
		return nil, false, nil, 0, ledger.ErrInvalidAmount
	}
	if reason == "" {
		return nil, false, nil, 0, ledger.ErrReasonRequired
	}
	if utf8.RuneCountInString(reason) > ledger.MaxReasonLen {
		return nil, false, nil, 0, ledger.ErrTextTooLong
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, nil, 0, fmt.Errorf("failed to begin refund: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	p := &payment.Payment{}
	err = scanPayment(tx.QueryRowContext(ctx,
		`SELECT `+paymentSelectCols+` FROM payments WHERE id = $1 FOR UPDATE`, paymentID), p)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil, 0, payment.ErrPaymentNotFound
	}
	if err != nil {
		return nil, false, nil, 0, fmt.Errorf("failed to lock payment for refund: %w", err)
	}
	// net <= 0 (tiny payments are never credited): nothing to refund.
	if p.Status != payment.StatusPaid || p.Environment != payment.EnvironmentProduction || p.MerchantNetAmount <= 0 {
		return nil, false, nil, 0, ledger.ErrPaymentNotRefundable
	}

	var already int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM payment_refunds WHERE payment_id = $1`, paymentID).Scan(&already); err != nil {
		return nil, false, nil, 0, fmt.Errorf("failed to sum refunds: %w", err)
	}
	remaining := p.MerchantNetAmount - already
	if amount > remaining {
		return nil, false, nil, 0, ledger.ErrRefundExceedsNet
	}

	// Both rows use merchantID from the locked payment row.
	merchantID := p.MerchantID
	refund := &ledger.Refund{ID: uuid.New(), PaymentID: paymentID, MerchantID: merchantID, Amount: amount, Reason: reason, CreatedBy: adminID}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO payment_refunds (id, payment_id, merchant_id, amount, reason, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		refund.ID, paymentID, merchantID, amount, reason, nullableUUID(adminID)).Scan(&refund.CreatedAt); err != nil {
		return nil, false, nil, 0, fmt.Errorf("failed to insert refund: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, payment_id, refund_id, note, created_by)
		VALUES ($1, $2, 'refund_debit', $3, $4, $5, $6, $7)`,
		uuid.New(), merchantID, -amount, paymentID, refund.ID, reason, nullableUUID(adminID)); err != nil {
		return nil, false, nil, 0, fmt.Errorf("failed to debit merchant ledger: %w", err)
	}

	remaining -= amount
	full := remaining == 0
	if full {
		// A dedicated statement: UpdateStatus always rewrites paid_at.
		if err := tx.QueryRowContext(ctx,
			`UPDATE payments SET status = 'refunded', updated_at = now() WHERE id = $1 AND status = 'paid' RETURNING updated_at`,
			paymentID).Scan(&p.UpdatedAt); err != nil {
			return nil, false, nil, 0, fmt.Errorf("failed to mark payment refunded: %w", err)
		}
		p.Status = payment.StatusRefunded
	}
	if err := tx.Commit(); err != nil {
		return nil, false, nil, 0, fmt.Errorf("failed to commit refund: %w", err)
	}
	return refund, full, p, remaining, nil
}

// ListRefunds returns a payment's refunds (newest first) with the refundable
// figures, read from one snapshot. payment.ErrPaymentNotFound when the
// payment does not exist.
func (r *RefundRepository) ListRefunds(ctx context.Context, paymentID uuid.UUID) (*ledger.RefundSummary, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("failed to begin refund listing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var status, env string
	var net int64
	err = tx.QueryRowContext(ctx,
		`SELECT status, environment, merchant_net_amount FROM payments WHERE id = $1`, paymentID).Scan(&status, &env, &net)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, payment.ErrPaymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load payment for refunds: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, amount, reason, created_at FROM payment_refunds WHERE payment_id = $1 ORDER BY created_at DESC, id DESC`, paymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to list refunds: %w", err)
	}
	defer rows.Close()
	out := &ledger.RefundSummary{Items: []ledger.RefundItem{}, MerchantNetAmount: net}
	for rows.Next() {
		var it ledger.RefundItem
		if err := rows.Scan(&it.ID, &it.Amount, &it.Reason, &it.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan refund: %w", err)
		}
		out.RefundedAmount += it.Amount
		out.Items = append(out.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read refunds: %w", err)
	}
	out.RemainingRefundable = net - out.RefundedAmount
	if out.RemainingRefundable < 0 {
		out.RemainingRefundable = 0
	}
	out.Refundable = env == payment.EnvironmentProduction && status == payment.StatusPaid && out.RemainingRefundable > 0
	return out, nil
}
