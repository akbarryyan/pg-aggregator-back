package repository

import (
	"context"
	"fmt"
	"time"
)

// CallbackDayAgg is the callback deliveries created on one calendar day.
// Other = everything that is neither success nor failed (pending, sending,
// skipped).
type CallbackDayAgg struct {
	Success int64
	Failed  int64
	Other   int64
}

// CallbackStats is the raw aggregate behind the admin Callbacks page summary.
type CallbackStats struct {
	// Breakdown lists the statuses present with their counts, ordered by
	// status. Never nil.
	Breakdown []StatusCount
	// DailyByDay maps "2006-01-02" (DATE(created_at)) to that day's deliveries.
	DailyByDay map[string]CallbackDayAgg
}

// Stats returns the callback-delivery aggregates in two light queries. days
// bounds the daily series like the other stats (DATE(created_at) /
// CURRENT_DATE; created_at is a TIMESTAMP, compared against the same
// CURRENT_DATE-based lower bound).
func (r *MerchantCallbackRepository) Stats(ctx context.Context, days int) (*CallbackStats, error) {
	if days < 1 {
		days = 1
	}
	breakdown, err := queryStatusCounts(ctx, r.db, `
		SELECT status, COUNT(*) FROM merchant_callback_deliveries GROUP BY status ORDER BY status`,
		"callback deliveries")
	if err != nil {
		return nil, err
	}
	s := &CallbackStats{Breakdown: breakdown, DailyByDay: make(map[string]CallbackDayAgg)}

	rows, err := r.db.QueryContext(ctx, `
		SELECT DATE(created_at) AS day,
		       COUNT(*) FILTER (WHERE status = 'success'),
		       COUNT(*) FILTER (WHERE status = 'failed'),
		       COUNT(*) FILTER (WHERE status NOT IN ('success', 'failed'))
		  FROM merchant_callback_deliveries
		 WHERE created_at >= (CURRENT_DATE - ($1::int - 1) * INTERVAL '1 day')
		 GROUP BY day`, days)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily callbacks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var a CallbackDayAgg
		if err := rows.Scan(&day, &a.Success, &a.Failed, &a.Other); err != nil {
			return nil, fmt.Errorf("failed to scan daily callbacks: %w", err)
		}
		s.DailyByDay[day.Format("2006-01-02")] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read daily callbacks: %w", err)
	}
	return s, nil
}
