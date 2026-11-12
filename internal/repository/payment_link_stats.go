package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PaymentLinkDayAgg is the paid payments made through links on one calendar day.
type PaymentLinkDayAgg struct {
	PaidCount  int64
	PaidAmount int64
}

// PaymentLinkStats is the raw aggregate behind the merchant Payment Links page
// summary. Every number belongs to the one merchant and environment it was
// queried for.
type PaymentLinkStats struct {
	Total, Active, Fixed, Open int64
	// PaidCount/PaidAmount are the paid payments spawned by the merchant's
	// links in the requested environment.
	PaidCount, PaidAmount int64
	// DailyByDay maps "2006-01-02" (DATE(paid_at)) to that day's paid link payments.
	DailyByDay map[string]PaymentLinkDayAgg
}

// Stats returns the link counts and the paid-via-link totals of one merchant,
// all scoped to the given environment.
// days bounds the daily series to today and the days-1 days before it
// (DATE(paid_at) / CURRENT_DATE).
func (r *PaymentLinkRepository) Stats(ctx context.Context, merchantID uuid.UUID, environment string, days int) (*PaymentLinkStats, error) {
	if days < 1 {
		days = 1
	}
	s := &PaymentLinkStats{DailyByDay: make(map[string]PaymentLinkDayAgg)}

	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE is_active),
		       COUNT(*) FILTER (WHERE amount_type = 'fixed'),
		       COUNT(*) FILTER (WHERE amount_type = 'open')
		  FROM payment_links
		 WHERE merchant_id = $1 AND environment = $2`, merchantID, environment).
		Scan(&s.Total, &s.Active, &s.Fixed, &s.Open); err != nil {
		return nil, fmt.Errorf("failed to aggregate payment links: %w", err)
	}

	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(amount), 0)
		  FROM payments
		 WHERE merchant_id = $1 AND payment_link_id IS NOT NULL
		   AND status = 'paid' AND environment = $2`, merchantID, environment).
		Scan(&s.PaidCount, &s.PaidAmount); err != nil {
		return nil, fmt.Errorf("failed to aggregate link payments: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT DATE(paid_at) AS day, COUNT(*), COALESCE(SUM(amount), 0)
		  FROM payments
		 WHERE merchant_id = $1 AND payment_link_id IS NOT NULL
		   AND status = 'paid' AND environment = $2
		   AND paid_at >= (CURRENT_DATE - ($3::int - 1) * INTERVAL '1 day')
		 GROUP BY day`, merchantID, environment, days)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily link payments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var a PaymentLinkDayAgg
		if err := rows.Scan(&day, &a.PaidCount, &a.PaidAmount); err != nil {
			return nil, fmt.Errorf("failed to scan daily link payments: %w", err)
		}
		s.DailyByDay[day.Format("2006-01-02")] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read daily link payments: %w", err)
	}
	return s, nil
}
