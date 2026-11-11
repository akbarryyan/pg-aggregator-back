package repository

import (
	"context"
	"fmt"
	"time"
)

// MerchantStats is the raw aggregate behind the admin Merchants page summary:
// head counts, what the platform owes merchants (sum of the append-only
// ledger), and sign-ups per calendar day for the last N days.
type MerchantStats struct {
	Total                int64
	Active               int64
	TotalBalance         int64
	NegativeBalanceCount int64
	// SignupsByDay maps "2006-01-02" to the merchants created that day. Days
	// without sign-ups are absent (the service fills them with zero).
	SignupsByDay map[string]int64
}

// Stats returns the merchant aggregates in three light queries. days bounds the
// sign-up window to today and the days-1 days before it, using the same
// DATE(created_at) / CURRENT_DATE convention as the payment dashboard charts so
// both dashboards bucket days identically.
func (r *MerchantRepository) Stats(ctx context.Context, days int) (*MerchantStats, error) {
	if days < 1 {
		days = 1
	}
	s := &MerchantStats{SignupsByDay: make(map[string]int64)}

	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE is_active) FROM merchants`,
	).Scan(&s.Total, &s.Active); err != nil {
		return nil, fmt.Errorf("failed to count merchants: %w", err)
	}

	// Per-merchant ledger sums first, then totals: a merchant without ledger
	// rows counts as balance 0 (never negative).
	if err := r.db.QueryRowContext(ctx, `
		WITH b AS (
			SELECT COALESCE(SUM(l.amount), 0) AS balance
			  FROM merchants m
			  LEFT JOIN merchant_ledger l ON l.merchant_id = m.id
			 GROUP BY m.id)
		SELECT COALESCE(SUM(balance), 0), COUNT(*) FILTER (WHERE balance < 0) FROM b`,
	).Scan(&s.TotalBalance, &s.NegativeBalanceCount); err != nil {
		return nil, fmt.Errorf("failed to sum merchant balances: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT DATE(created_at) AS day, COUNT(*)
		  FROM merchants
		 WHERE created_at >= (CURRENT_DATE - ($1::int - 1) * INTERVAL '1 day')
		 GROUP BY day`, days)
	if err != nil {
		return nil, fmt.Errorf("failed to count merchant sign-ups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var n int64
		if err := rows.Scan(&day, &n); err != nil {
			return nil, fmt.Errorf("failed to scan merchant sign-ups: %w", err)
		}
		s.SignupsByDay[day.Format("2006-01-02")] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read merchant sign-ups: %w", err)
	}
	return s, nil
}
