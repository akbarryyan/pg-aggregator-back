package repository

import (
	"context"
	"fmt"
	"time"
)

// PayoutStatusAgg is the number and total amount of payouts in one status.
type PayoutStatusAgg struct {
	Count  int64
	Amount int64
}

// PayoutDayAgg is the payouts requested on one calendar day.
type PayoutDayAgg struct {
	Count  int64
	Amount int64
}

// PayoutStats is the raw aggregate behind the admin Payouts page summary.
type PayoutStats struct {
	// ByStatus maps requested|paid|rejected to its count and amount; statuses
	// without rows are absent.
	ByStatus map[string]PayoutStatusAgg
	// Breakdown lists the statuses present, ordered by status. Never nil.
	Breakdown []StatusCount
	// DailyByDay maps "2006-01-02" (DATE(requested_at)) to that day's requests.
	DailyByDay map[string]PayoutDayAgg
}

// Stats returns the payout aggregates in two light queries. days bounds the
// daily series to today and the days-1 days before it (DATE(requested_at) /
// CURRENT_DATE, like the other dashboards).
func (r *PayoutRepository) Stats(ctx context.Context, days int) (*PayoutStats, error) {
	if days < 1 {
		days = 1
	}
	s := &PayoutStats{
		ByStatus:   make(map[string]PayoutStatusAgg),
		Breakdown:  []StatusCount{},
		DailyByDay: make(map[string]PayoutDayAgg),
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT status, COUNT(*), COALESCE(SUM(amount), 0)
		  FROM payouts
		 GROUP BY status
		 ORDER BY status`)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate payouts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var a PayoutStatusAgg
		if err := rows.Scan(&status, &a.Count, &a.Amount); err != nil {
			return nil, fmt.Errorf("failed to scan payout aggregate: %w", err)
		}
		s.ByStatus[status] = a
		s.Breakdown = append(s.Breakdown, StatusCount{Status: status, Count: a.Count})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read payout aggregate: %w", err)
	}

	drows, err := r.db.QueryContext(ctx, `
		SELECT DATE(requested_at) AS day, COUNT(*), COALESCE(SUM(amount), 0)
		  FROM payouts
		 WHERE requested_at >= (CURRENT_DATE - ($1::int - 1) * INTERVAL '1 day')
		 GROUP BY day`, days)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily payouts: %w", err)
	}
	defer drows.Close()
	for drows.Next() {
		var day time.Time
		var a PayoutDayAgg
		if err := drows.Scan(&day, &a.Count, &a.Amount); err != nil {
			return nil, fmt.Errorf("failed to scan daily payouts: %w", err)
		}
		s.DailyByDay[day.Format("2006-01-02")] = a
	}
	if err := drows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read daily payouts: %w", err)
	}
	return s, nil
}
