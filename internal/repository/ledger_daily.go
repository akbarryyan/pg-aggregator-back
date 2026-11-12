package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LedgerDayAgg is one day of a merchant's ledger movement: Credited is the sum
// of positive entries, Debited the absolute value of the sum of negative ones.
type LedgerDayAgg struct {
	Credited int64
	Debited  int64
}

// Daily returns one merchant's ledger movement per day (DATE(created_at)) for
// today and the days-1 days before it; days without entries are absent.
func (r *LedgerRepository) Daily(ctx context.Context, merchantID uuid.UUID, days int) (map[string]LedgerDayAgg, error) {
	if days < 1 {
		days = 1
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT DATE(created_at) AS day,
		       COALESCE(SUM(amount) FILTER (WHERE amount > 0), 0),
		       COALESCE(-SUM(amount) FILTER (WHERE amount < 0), 0)
		  FROM merchant_ledger
		 WHERE merchant_id = $1
		   AND created_at >= (CURRENT_DATE - ($2::int - 1) * INTERVAL '1 day')
		 GROUP BY day`, merchantID, days)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily ledger: %w", err)
	}
	defer rows.Close()
	out := make(map[string]LedgerDayAgg)
	for rows.Next() {
		var day time.Time
		var a LedgerDayAgg
		if err := rows.Scan(&day, &a.Credited, &a.Debited); err != nil {
			return nil, fmt.Errorf("failed to scan daily ledger: %w", err)
		}
		out[day.Format("2006-01-02")] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read daily ledger: %w", err)
	}
	return out, nil
}
