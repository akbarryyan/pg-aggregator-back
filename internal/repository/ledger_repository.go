package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type LedgerRepository struct {
	db *sql.DB
}

func NewLedgerRepository(db *sql.DB) *LedgerRepository {
	return &LedgerRepository{db: db}
}

// Summary aggregates a merchant's ledger in one query.
func (r *LedgerRepository) Summary(ctx context.Context, merchantID uuid.UUID) (*ledger.Summary, error) {
	const query = `
		SELECT COALESCE(SUM(amount), 0),
		       COALESCE(SUM(amount) FILTER (WHERE entry_type = 'payment_credit'), 0),
		       COALESCE(-SUM(amount) FILTER (WHERE entry_type IN ('payout_debit', 'payout_reversal')), 0),
		       COALESCE(-SUM(amount) FILTER (WHERE entry_type = 'refund_debit'), 0)
		  FROM merchant_ledger WHERE merchant_id = $1`
	s := &ledger.Summary{}
	if err := r.db.QueryRowContext(ctx, query, merchantID).
		Scan(&s.Balance, &s.TotalCredited, &s.TotalPaidOut, &s.TotalRefunded); err != nil {
		return nil, fmt.Errorf("failed to get ledger summary: %w", err)
	}
	return s, nil
}

// clampPage normalises paging: default limit 20, maximum 100, offset >= 0.
func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// List returns a page of entries (newest first) and the merchant's total count.
// The slice is never nil (it serializes to [] rather than null) and limit is
// capped at 100.
func (r *LedgerRepository) List(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Entry, int64, error) {
	limit, offset = clampPage(limit, offset)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM merchant_ledger WHERE merchant_id = $1`, merchantID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count ledger entries: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, merchant_id, entry_type, amount, payment_id, payout_id, refund_id, note, created_by, created_at
		  FROM merchant_ledger WHERE merchant_id = $1
		 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, merchantID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list ledger entries: %w", err)
	}
	defer rows.Close()

	entries := make([]*ledger.Entry, 0, limit)
	for rows.Next() {
		e := &ledger.Entry{}
		var paymentID, payoutID, refundID, createdBy uuid.NullUUID
		if err := rows.Scan(&e.ID, &e.MerchantID, &e.EntryType, &e.Amount,
			&paymentID, &payoutID, &refundID, &e.Note, &createdBy, &e.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("failed to scan ledger entry: %w", err)
		}
		e.PaymentID, e.PayoutID, e.RefundID, e.CreatedBy =
			nullUUIDPtr(paymentID), nullUUIDPtr(payoutID), nullUUIDPtr(refundID), nullUUIDPtr(createdBy)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read ledger entries: %w", err)
	}
	return entries, total, nil
}

func nullUUIDPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

// ListBalances returns every merchant's balance (highest first) with their
// open payout, if any. negativeOnly keeps only merchants whose balance is
// below zero. Returns the page and the total matching count.
func (r *LedgerRepository) ListBalances(ctx context.Context, negativeOnly bool, limit, offset int) ([]*ledger.MerchantBalance, int64, error) {
	limit, offset = clampPage(limit, offset)
	const cte = `
		WITH b AS (
			SELECT m.id, m.name,
			       COALESCE((SELECT SUM(l.amount) FROM merchant_ledger l WHERE l.merchant_id = m.id), 0) AS balance
			  FROM merchants m)`
	var total int64
	if err := r.db.QueryRowContext(ctx, cte+` SELECT COUNT(*) FROM b WHERE (NOT $1 OR b.balance < 0)`, negativeOnly).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count merchant balances: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, cte+`
		SELECT b.id, b.name, b.balance, p.id, COALESCE(p.amount, 0)
		  FROM b LEFT JOIN payouts p ON p.merchant_id = b.id AND p.status = 'requested'
		 WHERE (NOT $1 OR b.balance < 0)
		 ORDER BY b.balance DESC, b.name, b.id LIMIT $2 OFFSET $3`, negativeOnly, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list merchant balances: %w", err)
	}
	defer rows.Close()
	items := make([]*ledger.MerchantBalance, 0, limit)
	for rows.Next() {
		it := &ledger.MerchantBalance{}
		var openID uuid.NullUUID
		if err := rows.Scan(&it.MerchantID, &it.MerchantName, &it.Balance, &openID, &it.OpenPayoutAmount); err != nil {
			return nil, 0, fmt.Errorf("failed to scan merchant balance: %w", err)
		}
		it.OpenPayoutID = nullUUIDPtr(openID)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read merchant balances: %w", err)
	}
	return items, total, nil
}

// Adjust records an admin balance adjustment (positive credits, negative
// debits) as one append-only ledger row. The reason is mandatory and stored
// as the entry note; created_by is the acting admin. A merchant that does not
// exist is reported as merchant.ErrMerchantNotFound.
func (r *LedgerRepository) Adjust(ctx context.Context, merchantID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*ledger.Entry, error) {
	if amount == 0 || amount > ledger.MaxAdjustment || amount < -ledger.MaxAdjustment {
		return nil, ledger.ErrInvalidAdjustment
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, ledger.ErrReasonRequired
	}
	if utf8.RuneCountInString(reason) > ledger.MaxReasonLen {
		return nil, ledger.ErrTextTooLong
	}
	e := &ledger.Entry{ID: uuid.New(), MerchantID: merchantID, EntryType: ledger.EntryAdjustment, Amount: amount, Note: reason, CreatedBy: adminID}
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, note, created_by)
		VALUES ($1, $2, 'adjustment', $3, $4, $5) RETURNING created_at`,
		e.ID, merchantID, amount, reason, nullableUUID(adminID)).Scan(&e.CreatedAt)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23503" {
		return nil, merchant.ErrMerchantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to record adjustment: %w", err)
	}
	return e, nil
}
