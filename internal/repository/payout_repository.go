package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

const (
	maxPayoutReferenceLen = 200
	maxPayoutReasonLen    = 500
)

type PayoutRepository struct {
	db *sql.DB
}

func NewPayoutRepository(db *sql.DB) *PayoutRepository {
	return &PayoutRepository{db: db}
}

const payoutCols = `id, merchant_id, amount, status, bank_name, account_number, account_holder,
	requested_by, requested_at, processed_by, processed_at, reference, reject_reason`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPayout(row rowScanner, withName bool) (*ledger.Payout, error) {
	p := &ledger.Payout{}
	var requestedBy, processedBy uuid.NullUUID
	var processedAt sql.NullTime
	var reference, reason sql.NullString
	dest := []any{&p.ID, &p.MerchantID, &p.Amount, &p.Status, &p.BankName, &p.AccountNumber, &p.AccountHolder,
		&requestedBy, &p.RequestedAt, &processedBy, &processedAt, &reference, &reason}
	if withName {
		dest = append(dest, &p.MerchantName)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	p.RequestedBy, p.ProcessedBy = nullUUIDPtr(requestedBy), nullUUIDPtr(processedBy)
	if processedAt.Valid {
		t := processedAt.Time
		p.ProcessedAt = &t
	}
	if reference.Valid {
		p.Reference = &reference.String
	}
	if reason.Valid {
		p.RejectReason = &reason.String
	}
	return p, nil
}

// Request creates a payout for merchantID and debits the ledger in one
// transaction. The merchant row is locked (FOR NO KEY UPDATE) first, which serializes
// concurrent requests of the same merchant so the balance check cannot race.
// NO KEY UPDATE rather than UPDATE because it does not conflict with the FOR
// KEY SHARE locks that foreign-key checks on merchant_ledger/payouts inserts
// (e.g. payment credits) take on the merchant row, so those are not blocked.
// The payout row and the ledger row are both written with the merchantID that
// was locked and validated here (the DB does not enforce that they agree).
func (r *PayoutRepository) Request(ctx context.Context, merchantID uuid.UUID, amount, minAmount int64, requestedBy *uuid.UUID) (*ledger.Payout, error) {
	if amount <= 0 || amount < minAmount {
		return nil, ledger.ErrBelowMinimumPayout
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin payout request: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var bank, number, holder sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT payout_bank_name, payout_account_number, payout_account_holder
		   FROM merchants WHERE id = $1 FOR NO KEY UPDATE`, merchantID).Scan(&bank, &number, &holder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, merchant.ErrMerchantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock merchant: %w", err)
	}
	if !bank.Valid || !number.Valid || !holder.Valid {
		return nil, ledger.ErrPayoutAccountMissing
	}

	var open bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM payouts WHERE merchant_id = $1 AND status = 'requested')`, merchantID).Scan(&open); err != nil {
		return nil, fmt.Errorf("failed to check open payout: %w", err)
	}
	if open {
		return nil, ledger.ErrOpenPayoutExists
	}

	var balance int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM merchant_ledger WHERE merchant_id = $1`, merchantID).Scan(&balance); err != nil {
		return nil, fmt.Errorf("failed to compute balance: %w", err)
	}
	if amount > balance {
		return nil, ledger.ErrInsufficientBalance
	}

	payoutID := uuid.New()
	p, err := scanPayout(tx.QueryRowContext(ctx,
		`INSERT INTO payouts (id, merchant_id, amount, status, bank_name, account_number, account_holder, requested_by)
		 VALUES ($1, $2, $3, 'requested', $4, $5, $6, $7)
		 RETURNING `+payoutCols,
		payoutID, merchantID, amount, bank.String, number.String, holder.String, nullableUUID(requestedBy)), false)
	if err != nil {
		if isOpenPayoutViolation(err) {
			return nil, ledger.ErrOpenPayoutExists
		}
		return nil, fmt.Errorf("failed to insert payout: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, payout_id, note, created_by)
		 VALUES ($1, $2, 'payout_debit', $3, $4, 'payout requested', $5)`,
		uuid.New(), merchantID, -amount, payoutID, nullableUUID(requestedBy)); err != nil {
		return nil, fmt.Errorf("failed to debit merchant ledger: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit payout request: %w", err)
	}
	return p, nil
}

// MarkPaid records that the payout was transferred. No ledger row is written:
// the debit was taken when it was requested.
func (r *PayoutRepository) MarkPaid(ctx context.Context, id uuid.UUID, reference string, adminID *uuid.UUID) (*ledger.Payout, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, ledger.ErrReasonRequired
	}
	if utf8.RuneCountInString(reference) > maxPayoutReferenceLen {
		return nil, ledger.ErrTextTooLong
	}
	p, err := scanPayout(r.db.QueryRowContext(ctx,
		`UPDATE payouts
		    SET status = 'paid', processed_at = now(), processed_by = $2, reference = $3
		  WHERE id = $1 AND status = 'requested'
		  RETURNING `+payoutCols, id, nullableUUID(adminID), reference), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, r.classifyUnprocessed(ctx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to mark payout paid: %w", err)
	}
	return p, nil
}

// Reject refuses the payout and returns the money with a payout_reversal
// ledger row, atomically. The reversal's merchant_id comes from the payouts
// row itself, never from the caller.
func (r *PayoutRepository) Reject(ctx context.Context, id uuid.UUID, reason string, adminID *uuid.UUID) (*ledger.Payout, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, ledger.ErrReasonRequired
	}
	if utf8.RuneCountInString(reason) > maxPayoutReasonLen {
		return nil, ledger.ErrTextTooLong
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin payout rejection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	p, err := scanPayout(tx.QueryRowContext(ctx,
		`UPDATE payouts
		    SET status = 'rejected', processed_at = now(), processed_by = $2, reject_reason = $3
		  WHERE id = $1 AND status = 'requested'
		  RETURNING `+payoutCols, id, nullableUUID(adminID), reason), false)
	if errors.Is(err, sql.ErrNoRows) {
		// Release the connection before classifying: the follow-up query uses
		// another pooled connection and must not wait on one held here.
		_ = tx.Rollback()
		return nil, r.classifyUnprocessed(ctx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to reject payout: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, payout_id, note, created_by)
		 VALUES ($1, $2, 'payout_reversal', $3, $4, $5, $6)`,
		uuid.New(), p.MerchantID, p.Amount, p.ID, "payout rejected", nullableUUID(adminID)); err != nil {
		return nil, fmt.Errorf("failed to reverse payout debit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit payout rejection: %w", err)
	}
	return p, nil
}

// classifyUnprocessed explains why a guarded UPDATE touched no row. It must be
// called with no transaction open.
func (r *PayoutRepository) classifyUnprocessed(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM payouts WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check payout: %w", err)
	}
	if !exists {
		return ledger.ErrPayoutNotFound
	}
	return ledger.ErrPayoutAlreadyProcessed
}

// HasOpen reports whether the merchant has a payout waiting for processing.
func (r *PayoutRepository) HasOpen(ctx context.Context, merchantID uuid.UUID) (bool, error) {
	var open bool
	if err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM payouts WHERE merchant_id = $1 AND status = 'requested')`, merchantID).Scan(&open); err != nil {
		return false, fmt.Errorf("failed to check open payout: %w", err)
	}
	return open, nil
}

// PendingSummary returns how many payouts wait for processing and when the
// latest was requested (nil when none).
func (r *PayoutRepository) PendingSummary(ctx context.Context) (int64, *time.Time, error) {
	var count int64
	var latest sql.NullTime
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(requested_at) FROM payouts WHERE status = 'requested'`).Scan(&count, &latest); err != nil {
		return 0, nil, fmt.Errorf("failed to summarize pending payouts: %w", err)
	}
	if !latest.Valid {
		return count, nil, nil
	}
	t := latest.Time
	return count, &t, nil
}

// ListByMerchant returns a page of the merchant's payouts, newest first, and
// the merchant's total count. limit defaults to 20 and is capped at 100; the
// slice is never nil.
func (r *PayoutRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Payout, int64, error) {
	limit, offset = clampPage(limit, offset)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payouts WHERE merchant_id = $1`, merchantID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count payouts: %w", err)
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+payoutCols+` FROM payouts WHERE merchant_id = $1
		  ORDER BY requested_at DESC, id DESC LIMIT $2 OFFSET $3`, merchantID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list payouts: %w", err)
	}
	defer rows.Close()
	items := make([]*ledger.Payout, 0, limit)
	for rows.Next() {
		p, err := scanPayout(rows, false)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan payout: %w", err)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read payouts: %w", err)
	}
	return items, total, nil
}

// ListAdmin returns a page of payouts across all merchants (with the merchant
// name), newest first, optionally filtered by status ("" = all). limit
// defaults to 20 and is capped at 100; the slice is never nil.
func (r *PayoutRepository) ListAdmin(ctx context.Context, status string, limit, offset int) ([]*ledger.Payout, int64, error) {
	limit, offset = clampPage(limit, offset)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payouts WHERE ($1 = '' OR status = $1)`, status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count payouts: %w", err)
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT p.id, p.merchant_id, p.amount, p.status, p.bank_name, p.account_number, p.account_holder,
		        p.requested_by, p.requested_at, p.processed_by, p.processed_at, p.reference, p.reject_reason, m.name
		   FROM payouts p JOIN merchants m ON m.id = p.merchant_id
		  WHERE ($1 = '' OR p.status = $1)
		  ORDER BY p.requested_at DESC, p.id DESC LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list payouts: %w", err)
	}
	defer rows.Close()
	items := make([]*ledger.Payout, 0, limit)
	for rows.Next() {
		p, err := scanPayout(rows, true)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan payout: %w", err)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read payouts: %w", err)
	}
	return items, total, nil
}

func nullableUUID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return *id
}

// openPayoutConstraint is the partial unique index allowing one open payout
// per merchant.
const openPayoutConstraint = "uq_payouts_one_open_per_merchant"

// isOpenPayoutViolation reports a 23505 on exactly that index; any other
// unique violation is a different bug and must not be disguised.
func isOpenPayoutViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == openPayoutConstraint
}
